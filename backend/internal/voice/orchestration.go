package voice

import (
	"errors"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"github.com/owlspeak/owl-server/backend/internal/stage"
)

// errCannotJoin 表示用户已不再具备停留在该语音频道的资格（无 VIEW/CONNECT 或被禁听）。
var errCannotJoin = errors.New("用户已无权停留在该语音频道")

// capsFor 重算某在房用户的 caps；若用户已无 join 资格返回 errCannotJoin（触发踢出流程）。
func (s *Service) capsFor(guildID, channelID, userID uuid.UUID, serverMute bool) ([]string, error) {
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return nil, errCannotJoin
	}
	ctx, err := perms.LoadGuild(s.db, user, guildID)
	if err != nil {
		return nil, errCannotJoin
	}
	_, bits, err := ctx.ChannelPerms(s.db, channelID)
	if err != nil {
		return nil, errCannotJoin
	}
	if !rbac.Has(bits, rbac.Connect) {
		return nil, errCannotJoin
	}
	if restriction.Denies(userID, guildID, &channelID, model.ChannelVoice).ListenVoice {
		return nil, errCannotJoin
	}
	return computeCaps(s.db, bits, guildID, channelID, userID, serverMute), nil
}

// internalLeave 内部离房：取消在途迁移 → SFU 摘人 → 清 VoiceState → 级联收尾 → 广播。
// 必须已持有 s.mu；disconnectReason 传给 SFU，eventReason 下发客户端（如 ADMIN_DISCONNECT）。
func (s *Service) internalLeave(vs *model.VoiceState, disconnectReason, eventReason string) error {
	if vs.ChannelID == nil {
		return nil
	}
	s.engine.cancelActive(vs.GuildID, vs.UserID)
	roomID := *vs.ChannelID
	var nodeID uuid.UUID
	if vs.NodeID != nil {
		nodeID = *vs.NodeID
		_ = sfuctl.Ctl().DisconnectUser(nodeID, roomID, vs.UserID, disconnectReason)
	}
	if err := s.db.Model(&model.VoiceState{}).Where("id = ?", vs.ID).Updates(map[string]any{
		"channel_id": nil, "node_id": nil, "room_id": nil, "voice_session_id": nil,
		"connected": false, "joined_at": nil,
	}).Error; err != nil {
		return err
	}
	s.cascadeAfterLeave(vs.GuildID, roomID, nodeID)
	vs.ChannelID, vs.NodeID, vs.RoomID, vs.VoiceSessionID = nil, nil, nil, nil
	vs.Connected = false
	vs.JoinedAt = nil
	s.publishState(*vs, eventReason)
	s.publishChannelStatus(vs.GuildID, roomID)
	// 舞台联动（docs 11 Z.5）：FIFO 解除最早容量禁说者、释放席位/队位/屏幕坑。
	stage.OnVoiceLeave(s.db, s.bus, vs.GuildID, roomID, vs.UserID)
	return nil
}

// joinResult 进房编排结果（响应与 VOICE_SERVER_UPDATE 复用）。
// SFUEndpoint = 节点经控制通道 Register 自报的 advertise_wss_url（客户端 WSS 信令端点）。
type joinResult struct {
	Token       string    `json:"token"`
	NodeID      uuid.UUID `json:"node_id"`
	RoomID      uuid.UUID `json:"room_id"`
	SFUEndpoint string    `json:"sfu_endpoint"`
	Caps        []string  `json:"caps"`
	SessionID   uuid.UUID `json:"session_id"`
	IceServers  []any     `json:"ice_servers"`
	ExpiresAt   int64     `json:"expires_at"`
}

// placeOnNode 在指定节点上完成入房编排：EnsureRoom → 级联挂边 → caps → token → VoiceState。
// 必须已持有 s.mu。
func (s *Service) placeOnNode(vs *model.VoiceState, exists bool, guildID, channelID, nodeID uuid.UUID,
	bits rbac.Permission, selfMute, selfDeaf bool) (joinResult, error) {

	if err := sfuctl.Ctl().EnsureRoom(nodeID, channelID); err != nil {
		return joinResult{}, err
	}
	if err := s.ensureCascade(guildID, channelID, nodeID); err != nil {
		return joinResult{}, err
	}
	caps := computeCaps(s.db, bits, guildID, channelID, vs.UserID, vs.ServerMute)
	// 先生成 voice_session_id 再签 token：claims 必须携带 sid（SFU 会话键，15 BJ.2）。
	sessionID := uuid.New()
	token, expiresAt, err := s.tokens.Sign(mediatoken.Claims{
		UID: vs.UserID.String(), GID: guildID.String(), CID: channelID.String(),
		NID: nodeID.String(), RID: channelID.String(), SID: sessionID.String(),
		Caps: caps, Bot: s.userIsBot(vs.UserID),
		Hidden: StealthPredicate(guildID, vs.UserID),
		Audit:  AuditPredicate(guildID, channelID),
	})
	if err != nil {
		return joinResult{}, err
	}
	info, err := sfuctl.Dir().Node(nodeID)
	if err != nil {
		return joinResult{}, err
	}

	now := nowUTC()
	vs.ChannelID = &channelID
	vs.NodeID = &nodeID
	vs.RoomID = &channelID // logical_room_id = channel_id（docs 08 A.1）
	vs.VoiceSessionID = &sessionID
	vs.SelfMute = selfMute
	vs.SelfDeaf = selfDeaf
	vs.Connected = false // SFU ParticipantJoined 事件后校正（docs 05 §3.1 步骤 13）
	vs.JoinedAt = &now
	if exists {
		err = s.db.Save(vs).Error
	} else {
		err = s.db.Create(vs).Error
	}
	if err != nil {
		return joinResult{}, err
	}
	s.publishState(*vs, "")
	s.publishChannelStatus(guildID, channelID)
	// 频道音频审计提示（adminpresence 专项）：审计开启且需提示时，仅向进房者下发
	// CHANNEL_AUDIT_NOTICE（notify=false 即静默审计，不下发提示）。
	if AuditPredicate(guildID, channelID) && AuditNotifyPredicate(guildID, channelID) {
		s.publishToUser(eventbus.EventChannelAuditNotice, vs.UserID, guildID, map[string]any{
			"guild_id": guildID, "channel_id": channelID, "audited": true,
		})
	}
	// 舞台联动（docs 11 Z）：>50 强制 STAGE、第 51+ 人容量禁说 + 自动入队（同步处理，事件订阅为兜底）。
	stage.OnVoiceJoin(s.db, s.bus, guildID, channelID, vs.UserID)
	return joinResult{
		Token: token, NodeID: nodeID, RoomID: channelID, SFUEndpoint: info.WebRTCEndpoint,
		Caps: caps, SessionID: sessionID, IceServers: []any{}, ExpiresAt: expiresAt.Unix(),
	}, nil
}

// recomputeCapsDirty 处理 InternalCapsDirty：重算目标用户（或整频道）caps，
// 推送 SFU（UpdateParticipantCaps）与客户端（VOICE_CAPS_UPDATE）；已无 join 资格则踢出（docs 05 §7.2）。
func (s *Service) recomputeCapsDirty(payload eventbus.CapsDirtyPayload) {
	guildID, err := uuid.Parse(payload.GuildID)
	if err != nil {
		return
	}
	channelID, err := uuid.Parse(payload.ChannelID)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var states []model.VoiceState
	query := s.db.Where("guild_id = ? AND channel_id = ?", guildID, channelID)
	if payload.UserID != "" {
		userID, err := uuid.Parse(payload.UserID)
		if err != nil {
			return
		}
		query = query.Where("user_id = ?", userID)
	}
	if err := query.Find(&states).Error; err != nil {
		return
	}
	for i := range states {
		vs := states[i]
		caps, err := s.capsFor(guildID, channelID, vs.UserID, vs.ServerMute)
		if errors.Is(err, errCannotJoin) {
			_ = s.internalLeave(&vs, "PERMISSION", "PERMISSION_REVOKED")
			continue
		}
		if err != nil {
			continue
		}
		if vs.NodeID != nil {
			_ = sfuctl.Ctl().UpdateParticipantCaps(*vs.NodeID, channelID, vs.UserID, caps)
		}
		s.publishToUser(eventbus.EventVoiceCapsUpdate, vs.UserID, guildID, voiceCapsUpdatePayload{
			GuildID: guildID, ChannelID: channelID, UserID: vs.UserID, Caps: caps,
		})
	}
}

// actorOutranks 管理动作层级校验（docs 02 §8、05 §8.1）：
// 系统管理员 / 服务器所有者放行；不得作用于所有者；否则要求 actor 最高角色 position 严格更高。
func (s *Service) actorOutranks(ctx *perms.GuildContext, targetUserID uuid.UUID) bool {
	if ctx.SystemAdmin || ctx.Owner {
		return true
	}
	if ctx.Guild.OwnerUserID == targetUserID {
		return false
	}
	var positions []int
	s.db.Raw(`SELECT roles.position FROM roles
		JOIN member_roles ON member_roles.role_id = roles.id
		JOIN members ON members.id = member_roles.member_id
		WHERE members.guild_id = ? AND members.user_id = ?`, ctx.Guild.ID, targetUserID).Scan(&positions)
	targetHighest := 0
	for _, p := range positions {
		if p > targetHighest {
			targetHighest = p
		}
	}
	return ctx.HighestRole > targetHighest
}
