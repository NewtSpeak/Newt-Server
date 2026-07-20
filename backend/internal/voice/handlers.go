package voice

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/message"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

func nowUTC() time.Time { return time.Now().UTC() }

// fail 统一错误响应 {"error":{"code","message"}}（仓库约定）。
func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// GET /voice/public-key
// ---------------------------------------------------------------------------

// handlePublicKey Media Token 验签公钥（调试/运维用，无需登录）。
// 公钥来自 internal/mediatoken（ClusterSecret 持久化），与 SFU enroll/RegisterAck
// 下发的验签公钥同源；返回全部 kid 以支持轮换。
func (s *Service) handlePublicKey(c *gin.Context) {
	keys := s.tokens.PublicKeys()
	views := make([]gin.H, 0, len(keys))
	for _, key := range keys {
		views = append(views, gin.H{
			"kid":               key.Kid,
			"public_key_base64": base64.StdEncoding.EncodeToString(key.Key),
			"public_key_pem":    publicKeyPEM(key.Key),
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"algorithm":         "EdDSA",
		"keys":              views,
		"token_ttl_seconds": int(s.tokens.TTL().Seconds()),
	})
}

// publicKeyPEM Ed25519 公钥 → PKIX PEM（便于人工核对与外部工具使用）。
func publicKeyPEM(key ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// ---------------------------------------------------------------------------
// POST /voice/join（docs 05 §3）
// ---------------------------------------------------------------------------

type joinRequest struct {
	GuildID   uuid.UUID `json:"guild_id" binding:"required"`
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
	SelfMute  bool      `json:"self_mute"`
	SelfDeaf  bool      `json:"self_deaf"`
}

func (s *Service) handleJoin(c *gin.Context) {
	user := s.currentUser(c)
	var input joinRequest
	if !bind(c, &input) {
		return
	}
	// RBAC：无 VIEW（含非成员 / 服不存在 / 频道不可见）→ 404 防扫频（docs 06 议题 8）。
	ctx, err := perms.LoadGuild(s.db, user, input.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	channel, bits, err := ctx.ChannelPerms(s.db, input.ChannelID)
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	if channel.Type != model.ChannelVoice {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "频道不存在或不是语音频道")
		return
	}
	if !rbac.Has(bits, rbac.Connect) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSIONS", "缺少 CONNECT 权限")
		return
	}
	// Restriction 禁听 = 禁 join（docs 12）。
	if restriction.Denies(user.ID, input.GuildID, &input.ChannelID, model.ChannelVoice).ListenVoice {
		fail(c, http.StatusForbidden, "RESTRICTED", "你当前被限制收听该语音频道")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 频道硬顶 200 人（docs 06；不含自己，避免重进/切换被自己占位挡住）。
	var occupied int64
	if err := s.db.Model(&model.VoiceState{}).
		Where("channel_id = ? AND user_id <> ?", input.ChannelID, user.ID).
		Count(&occupied).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询频道人数失败")
		return
	}
	if occupied >= channelHardCap {
		fail(c, http.StatusForbidden, "CHANNEL_FULL", "语音频道已满员")
		return
	}

	vs, exists, err := s.loadVoiceState(input.GuildID, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取语音状态失败")
		return
	}
	// 同 guild 同时仅一个语音频道：已在任何频道先内部 leave（docs 05 §6；
	// 重进同频道也先摘旧会话，避免调度换点后旧节点残留参与者）。
	var previousChannelID *uuid.UUID
	move := false
	if vs.ChannelID != nil {
		prev := *vs.ChannelID
		if prev != input.ChannelID {
			previousChannelID = &prev
			move = true
		}
		if err := s.internalLeave(&vs, "MOVED", ""); err != nil {
			fail(c, http.StatusInternalServerError, "LEAVE_FAILED", "离开原语音频道失败")
			return
		}
	}

	// 调度：池内硬过滤 + JOIN 打分（docs 10）。
	candidates, err := s.buildCandidates(input.GuildID, input.ChannelID)
	if err != nil || len(candidates) == 0 {
		fail(c, http.StatusServiceUnavailable, "NO_NODE_IN_POOL", "服务器节点池为空或不可用")
		return
	}
	result, ok := schedule(candidates, scheduleParams{
		Mode:         ModeJoin,
		UserID:       user.ID,
		ClientRegion: c.Query("region"),
		RTTMs:        s.rtt.Samples(user.ID, time.Now()),
		Config:       s.sched,
		Jitter:       defaultJitter,
	})
	if !ok {
		fail(c, http.StatusServiceUnavailable, "NO_SFU_CAPACITY", "暂无可用 SFU 容量")
		return
	}

	// primary 失败依次尝试 fallbacks（仅 Server 内部使用，docs 10 X.3）。
	targets := append([]uuid.UUID{result.Primary}, result.Fallbacks...)
	var joined joinResult
	var lastErr error
	success := false
	for _, nodeID := range targets {
		resvID := s.resv.Reserve(nodeID, s.sched.ReservationTTL)
		joined, lastErr = s.placeOnNode(&vs, exists, input.GuildID, input.ChannelID, nodeID, bits, input.SelfMute, input.SelfDeaf)
		if lastErr == nil {
			exists = true
			success = true
			break
		}
		s.resv.Release(resvID)
	}
	if !success {
		fail(c, http.StatusServiceUnavailable, "NO_SFU_CAPACITY", "SFU 入房编排失败: "+lastErr.Error())
		return
	}

	// 入场语音包（docs 07 5A.1）：仅在「从未连麦 → 进入语音频道」时触发，
	// 切换频道（move）不触发，避免每次切频都播放。
	if !move && previousChannelID == nil {
		message.PlayVoicePack(s.db, s.bus, input.GuildID, input.ChannelID, user.ID)
	}

	// advertise_wss_url 为规范字段（节点心跳/Register 上报的客户端 WSS 信令端点）；
	// sfu_endpoint 为同值兼容别名。
	response := gin.H{
		"token": joined.Token, "node_id": joined.NodeID, "room_id": joined.RoomID,
		"advertise_wss_url": joined.SFUEndpoint, "sfu_endpoint": joined.SFUEndpoint,
		"caps": joined.Caps, "session_id": joined.SessionID,
		"ice_servers": joined.IceServers, "expires_at": joined.ExpiresAt,
	}
	if move {
		response["move"] = true
		response["previous_channel_id"] = previousChannelID
		response["force_reconnect"] = true
	}
	c.JSON(http.StatusOK, response)
}

// ---------------------------------------------------------------------------
// POST /voice/leave（docs 05 §5）
// ---------------------------------------------------------------------------

type guildScopedRequest struct {
	GuildID uuid.UUID `json:"guild_id" binding:"required"`
}

func (s *Service) handleLeave(c *gin.Context) {
	user := s.currentUser(c)
	var input guildScopedRequest
	if !bind(c, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, exists, err := s.loadVoiceState(input.GuildID, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取语音状态失败")
		return
	}
	if !exists || vs.ChannelID == nil {
		c.JSON(http.StatusOK, gin.H{"left": false})
		return
	}
	if err := s.internalLeave(&vs, "LEAVE", ""); err != nil {
		fail(c, http.StatusInternalServerError, "LEAVE_FAILED", "离开语音频道失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"left": true})
}

// ---------------------------------------------------------------------------
// POST /voice/refresh-token（docs 05 §4：重算 caps，新 jti/exp）
// ---------------------------------------------------------------------------

func (s *Service) handleRefreshToken(c *gin.Context) {
	user := s.currentUser(c)
	var input guildScopedRequest
	if !bind(c, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, exists, err := s.loadVoiceState(input.GuildID, user.ID)
	if err != nil || !exists || vs.ChannelID == nil || vs.NodeID == nil || vs.VoiceSessionID == nil {
		fail(c, http.StatusBadRequest, "NOT_IN_VOICE", "当前不在语音频道内")
		return
	}
	caps, err := s.capsFor(input.GuildID, *vs.ChannelID, user.ID, vs.ServerMute)
	if err != nil {
		// caps 已无 join → 走踢出流程（docs 05 §4）。
		_ = s.internalLeave(&vs, "PERMISSION", "PERMISSION_REVOKED")
		fail(c, http.StatusForbidden, "MISSING_PERMISSIONS", "已无权停留在该语音频道")
		return
	}
	// 续会保持同一 sid（会话不变），仅换新 jti/exp（docs 05 §4、协议 §2.3 在位更新）。
	token, expiresAt, err := s.tokens.Sign(mediatoken.Claims{
		UID: user.ID.String(), GID: input.GuildID.String(), CID: vs.ChannelID.String(),
		NID: vs.NodeID.String(), RID: vs.ChannelID.String(), SID: vs.VoiceSessionID.String(),
		Caps: caps, Bot: user.IsBot,
		Hidden: StealthPredicate(input.GuildID, user.ID),
		Audit:  AuditPredicate(input.GuildID, *vs.ChannelID),
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "签发 Media Token 失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "caps": caps, "expires_at": expiresAt.Unix()})
}

// ---------------------------------------------------------------------------
// PATCH /voice/state（docs 05 §9 自我状态）
// ---------------------------------------------------------------------------

type selfStateRequest struct {
	GuildID  uuid.UUID `json:"guild_id" binding:"required"`
	SelfMute *bool     `json:"self_mute"`
	SelfDeaf *bool     `json:"self_deaf"`
}

func (s *Service) handleSelfState(c *gin.Context) {
	user := s.currentUser(c)
	var input selfStateRequest
	if !bind(c, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, exists, err := s.loadVoiceState(input.GuildID, user.ID)
	if err != nil || !exists || vs.ChannelID == nil {
		fail(c, http.StatusBadRequest, "NOT_IN_VOICE", "当前不在语音频道内")
		return
	}
	updates := map[string]any{}
	if input.SelfMute != nil {
		vs.SelfMute = *input.SelfMute
		updates["self_mute"] = *input.SelfMute
	}
	if input.SelfDeaf != nil {
		vs.SelfDeaf = *input.SelfDeaf
		updates["self_deaf"] = *input.SelfDeaf
	}
	if len(updates) > 0 {
		if err := s.db.Model(&model.VoiceState{}).Where("id = ?", vs.ID).Updates(updates).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新语音状态失败")
			return
		}
		// self_mute/self_deaf 以客户端本地停采/停放为主，SFU 双保险可选（docs 05 §9），
		// 这里仅更新状态并广播。
		s.publishState(vs, "")
	}
	c.JSON(http.StatusOK, vs)
}

// ---------------------------------------------------------------------------
// POST /voice/rtt（docs 10 §7 RTT 上报）
// ---------------------------------------------------------------------------

type rttReportRequest struct {
	Samples []struct {
		NodeID uuid.UUID `json:"node_id" binding:"required"`
		RTTMs  float64   `json:"rtt_ms" binding:"required"`
	} `json:"samples" binding:"required"`
}

func (s *Service) handleRTTReport(c *gin.Context) {
	user := s.currentUser(c)
	var input rttReportRequest
	if !bind(c, &input) {
		return
	}
	now := time.Now()
	stored := 0
	for _, sample := range input.Samples {
		if sample.RTTMs <= 0 {
			continue
		}
		s.rtt.Report(user.ID, sample.NodeID, sample.RTTMs, now)
		stored++
	}
	c.JSON(http.StatusOK, gin.H{"stored": stored, "ttl_seconds": int(s.sched.RTTSampleTTL.Seconds())})
}

// ---------------------------------------------------------------------------
// POST /voice/ice-failed（docs 15 §5 BI.2 ②：客户端侧 ICE/连接失败上报）
// ---------------------------------------------------------------------------

// iceFailedThrottle 同一用户上报节流窗口（轻量端点防刷；信号本身以 origin 去重，
// 重复上报只刷新时间戳，不叠加计数）。
const iceFailedThrottle = 5 * time.Second

// handleIceFailed 客户端发现与当前语音节点的媒体/信令连接失败时上报。
// 作为 BI.3 提前判死的独立信号源之一（origin 按用户去重）送入控制面聚合器；
// 判死权威仍仅 Server（BI.4），单条上报不产生任何直接动作。
func (s *Service) handleIceFailed(c *gin.Context) {
	user := s.currentUser(c)
	var input guildScopedRequest
	if !bind(c, &input) {
		return
	}
	s.mu.Lock()
	if s.iceFailedAt == nil {
		s.iceFailedAt = map[uuid.UUID]time.Time{}
	}
	last, seen := s.iceFailedAt[user.ID]
	throttled := seen && time.Since(last) < iceFailedThrottle
	if !throttled {
		s.iceFailedAt[user.ID] = time.Now()
	}
	s.mu.Unlock()
	if throttled {
		c.JSON(http.StatusOK, gin.H{"reported": false, "reason": "THROTTLED"})
		return
	}
	vs, exists, err := s.loadVoiceState(input.GuildID, user.ID)
	if err != nil || !exists || vs.ChannelID == nil || vs.NodeID == nil {
		fail(c, http.StatusBadRequest, "NOT_IN_VOICE", "当前不在语音频道内")
		return
	}
	sfuctl.ReportSuspect(*vs.NodeID, "client_ice:"+user.ID.String())
	c.JSON(http.StatusOK, gin.H{"reported": true})
}

// ---------------------------------------------------------------------------
// POST /guilds/:guildID/voice/disconnect（docs 05 §8.1 管理员踢出语音）
// ---------------------------------------------------------------------------

type disconnectRequest struct {
	UserID uuid.UUID `json:"user_id" binding:"required"`
}

func (s *Service) handleAdminDisconnect(c *gin.Context) {
	actor := s.currentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	var input disconnectRequest
	if !bind(c, &input) {
		return
	}
	ctx, err := perms.LoadGuild(s.db, actor, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	if !ctx.Has(rbac.MoveMembers) && !ctx.Has(rbac.MuteMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSIONS", "需要 MOVE_MEMBERS 或 MUTE_MEMBERS 权限")
		return
	}
	if !s.actorOutranks(ctx, input.UserID) {
		fail(c, http.StatusForbidden, "ROLE_HIERARCHY", "角色层级不足，无法操作该成员")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, exists, err := s.loadVoiceState(guildID, input.UserID)
	if err != nil || !exists || vs.ChannelID == nil {
		fail(c, http.StatusNotFound, "NOT_IN_VOICE", "目标用户不在语音频道内")
		return
	}
	channelID := *vs.ChannelID
	if err := s.internalLeave(&vs, "ADMIN", "ADMIN_DISCONNECT"); err != nil {
		fail(c, http.StatusInternalServerError, "DISCONNECT_FAILED", "踢出语音失败")
		return
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: actorType(actor, ctx), GuildID: &guildID,
		Action: "voice.admin_disconnect", TargetType: "user", TargetID: input.UserID.String(),
		Detail: map[string]any{"channel_id": channelID.String()},
	})
	c.JSON(http.StatusOK, gin.H{"disconnected": true})
}

// ---------------------------------------------------------------------------
// PATCH /guilds/:guildID/voice/states/:userID（服务器静音/耳聋）
// ---------------------------------------------------------------------------

type serverStateRequest struct {
	ServerMute *bool `json:"server_mute"`
	ServerDeaf *bool `json:"server_deaf"`
}

func (s *Service) handleServerState(c *gin.Context) {
	actor := s.currentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	targetUserID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	var input serverStateRequest
	if !bind(c, &input) {
		return
	}
	if input.ServerMute == nil && input.ServerDeaf == nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "server_mute / server_deaf 至少提供一项")
		return
	}
	ctx, err := perms.LoadGuild(s.db, actor, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	if input.ServerMute != nil && !ctx.Has(rbac.MuteMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSIONS", "需要 MUTE_MEMBERS 权限")
		return
	}
	if input.ServerDeaf != nil && !ctx.Has(rbac.DeafenMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSIONS", "需要 DEAFEN_MEMBERS 权限")
		return
	}
	if !s.actorOutranks(ctx, targetUserID) {
		fail(c, http.StatusForbidden, "ROLE_HIERARCHY", "角色层级不足，无法操作该成员")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, exists, err := s.loadVoiceState(guildID, targetUserID)
	if err != nil || !exists || vs.ChannelID == nil {
		fail(c, http.StatusNotFound, "NOT_IN_VOICE", "目标用户不在语音频道内")
		return
	}
	updates := map[string]any{}
	if input.ServerMute != nil {
		vs.ServerMute = *input.ServerMute
		updates["server_mute"] = *input.ServerMute
	}
	if input.ServerDeaf != nil {
		vs.ServerDeaf = *input.ServerDeaf
		updates["server_deaf"] = *input.ServerDeaf
	}
	if err := s.db.Model(&model.VoiceState{}).Where("id = ?", vs.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新语音状态失败")
		return
	}
	// 重算 caps → 控制通道强推 SFU → Gateway 通知（docs 05 §7.2）。
	channelID := *vs.ChannelID
	caps, err := s.capsFor(guildID, channelID, targetUserID, vs.ServerMute)
	if err == nil && vs.NodeID != nil {
		_ = sfuctl.Ctl().UpdateParticipantCaps(*vs.NodeID, channelID, targetUserID, caps)
	}
	s.publishState(vs, "")
	if err == nil {
		s.publishToUser(eventbus.EventVoiceCapsUpdate, targetUserID, guildID, voiceCapsUpdatePayload{
			GuildID: guildID, ChannelID: channelID, UserID: targetUserID, Caps: caps,
		})
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: actorType(actor, ctx), GuildID: &guildID,
		Action: "voice.server_state_update", TargetType: "user", TargetID: targetUserID.String(),
		Detail: map[string]any{"server_mute": vs.ServerMute, "server_deaf": vs.ServerDeaf},
	})
	c.JSON(http.StatusOK, vs)
}

// ---------------------------------------------------------------------------
// GET /guilds/:guildID/channels/:channelID/voice-states
// ---------------------------------------------------------------------------

func (s *Service) handleListVoiceStates(c *gin.Context) {
	user := s.currentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	ctx, err := perms.LoadGuild(s.db, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	if _, _, err := ctx.ChannelPerms(s.db, channelID); err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	var states []model.VoiceState
	if err := s.db.Where("guild_id = ? AND channel_id = ?", guildID, channelID).
		Order("joined_at ASC").Find(&states).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询语音状态失败")
		return
	}
	// 隐身临场（adminpresence）：非系统管理员看不到隐身管理员的语音会话。
	if !user.SystemAdmin {
		filtered := states[:0]
		for _, vs := range states {
			if StealthPredicate(guildID, vs.UserID) {
				continue
			}
			filtered = append(filtered, vs)
		}
		states = filtered
	}
	c.JSON(http.StatusOK, gin.H{"voice_states": states})
}

// ---------------------------------------------------------------------------
// POST /voice/migrations/:migrationID/ack（docs 09 §7 客户端确认）
// ---------------------------------------------------------------------------

func (s *Service) handleMigrationAck(c *gin.Context) {
	user := s.currentUser(c)
	migrationID, err := uuid.Parse(c.Param("migrationID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "迁移任务不存在")
		return
	}
	if err := s.engine.ack(migrationID, user.ID); err != nil {
		fail(c, http.StatusBadRequest, "MIGRATION_ACK_REJECTED", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"acknowledged": true})
}

// ---------------------------------------------------------------------------
// POST /admin/voice/migrations（docs 09 I.7 手动迁移）
// ---------------------------------------------------------------------------

type manualMigrationRequest struct {
	GuildID  uuid.UUID  `json:"guild_id" binding:"required"`
	UserID   uuid.UUID  `json:"user_id" binding:"required"`
	ToNodeID *uuid.UUID `json:"to_node_id"`
}

func (s *Service) handleManualMigration(c *gin.Context) {
	actor := s.currentUser(c)
	var input manualMigrationRequest
	if !bind(c, &input) {
		return
	}
	// 系统管理员任意；服务器管理员（MANAGE_GUILD）仅本服会话（docs 09 §3.6）。
	actorRole := "system_admin"
	if !actor.SystemAdmin {
		ctx, err := perms.LoadGuild(s.db, actor, input.GuildID)
		if err != nil {
			fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
			return
		}
		if !ctx.Has(rbac.ManageGuild) {
			fail(c, http.StatusForbidden, "MISSING_PERMISSIONS", "需要系统管理员或 MANAGE_GUILD 权限")
			return
		}
		actorRole = "guild_admin"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, exists, err := s.loadVoiceState(input.GuildID, input.UserID)
	if err != nil || !exists || vs.ChannelID == nil || vs.NodeID == nil {
		fail(c, http.StatusNotFound, "NOT_IN_VOICE", "目标用户不在语音频道内")
		return
	}
	if input.ToNodeID != nil {
		if *input.ToNodeID == *vs.NodeID {
			fail(c, http.StatusBadRequest, "INVALID_TARGET", "目标节点不能是当前节点")
			return
		}
		if !s.nodeInPool(input.GuildID, *input.ToNodeID) {
			fail(c, http.StatusBadRequest, "INVALID_TARGET", "目标节点不在该服务器节点池内")
			return
		}
	}
	job, err := s.engine.createJob(model.VoiceMigrationJob{
		Reason: model.MigrationReasonManual, UserID: input.UserID, GuildID: input.GuildID,
		ChannelID: *vs.ChannelID, FromNodeID: *vs.NodeID, ToNodeID: input.ToNodeID,
		ActorID: &actor.ID, ActorType: actorRole,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "MIGRATION_CREATE_FAILED", "创建迁移任务失败")
		return
	}
	c.JSON(http.StatusAccepted, job)
}

func actorType(actor model.User, ctx *perms.GuildContext) string {
	if actor.SystemAdmin {
		return "system_admin"
	}
	if ctx != nil && (ctx.Owner || ctx.Has(rbac.ManageGuild)) {
		return "guild_admin"
	}
	return "user"
}
