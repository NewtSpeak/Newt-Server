package stage

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"gorm.io/gorm/clause"
)

// handlers REST 层：权限校验 + 参数解析，业务落在 service。
type handlers struct {
	svc         *service
	currentUser func(*gin.Context) model.User
}

type errorResponse struct {
	Error apiError `json:"error"`
}
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorResponse{apiError{code, message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// channelScope 频道请求的公共上下文。
type channelScope struct {
	user    model.User
	ctx     *perms.GuildContext
	channel model.Channel
	bits    rbac.Permission
}

// voiceChannelScope 解析 channelID 并完成可见性校验（不可见一律 404，docs 06 议题 8），
// 且要求是语音频道。
func (h *handlers) voiceChannelScope(c *gin.Context) (channelScope, bool) {
	var scope channelScope
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return scope, false
	}
	var channel model.Channel
	if err := h.svc.db.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return scope, false
	}
	user := h.currentUser(c)
	ctx, err := perms.LoadGuild(h.svc.db, user, channel.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return scope, false
	}
	channel, bits, err := ctx.ChannelPerms(h.svc.db, channelID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return scope, false
	}
	if channel.Type != model.ChannelVoice {
		fail(c, http.StatusBadRequest, "NOT_VOICE_CHANNEL", "该操作仅适用于语音频道")
		return scope, false
	}
	scope = channelScope{user: user, ctx: ctx, channel: channel, bits: bits}
	return scope, true
}

// inVoice 判断用户当前是否在该语音频道内（以 VoiceState 为准）。
func (h *handlers) inVoice(channelID, userID uuid.UUID) bool {
	var count int64
	h.svc.db.Model(&model.VoiceState{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
	return count > 0
}

// ---------- 舞台配置 ----------

type stageConfigRequest struct {
	Mode                  *string   `json:"mode"`
	MaxSpeakers           *int      `json:"max_speakers"`
	RequestToSpeakEnabled *bool     `json:"request_to_speak_enabled"`
	AllowCoModChangeMode  *bool     `json:"allow_co_mod_change_mode"`
	CoModeratorIDs        *[]string `json:"co_moderator_ids"`
	// MaxConcurrentScreens 频道屏幕并发上限（docs 14 AY.2，默认 2），随舞台配置一并管理。
	MaxConcurrentScreens *int `json:"max_concurrent_screens"`
}

// patchVoiceStage PATCH /channels/:channelID/voice-stage（docs 11 §2）。
func (h *handlers) patchVoiceStage(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	var input stageConfigRequest
	if !bind(c, &input) {
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	cfg := h.svc.channelConfig(db, scope.channel.GuildID, scope.channel.ID)

	// 权限：服管/MANAGE_CHANNELS 可改全部；协管仅在 allow_co_mod_change_mode 开启且持有
	// STAGE_CHANGE_MODE 节点时可改 mode（docs 11 Y.2）。
	canManage := rbac.Has(scope.bits, rbac.ManageChannels)
	canChangeMode := canManage ||
		(h.svc.isCoModerator(db, scope.channel.ID, scope.user.ID) &&
			rbac.Has(scope.bits, rbac.StageChangeMode) && cfg.AllowCoModChangeMode)
	touchesOthers := input.MaxSpeakers != nil || input.RequestToSpeakEnabled != nil ||
		input.AllowCoModChangeMode != nil || input.CoModeratorIDs != nil || input.MaxConcurrentScreens != nil
	if touchesOthers && !canManage {
		fail(c, http.StatusForbidden, "FORBIDDEN", "需要频道管理权限")
		return
	}
	if input.Mode != nil && !canChangeMode {
		fail(c, http.StatusForbidden, "FORBIDDEN", "无权切换频道模式")
		return
	}

	// 参数校验。
	if input.Mode != nil && *input.Mode != model.StageModeFree && *input.Mode != model.StageModeStage {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "mode 只能为 FREE_DISCUSSION 或 STAGE")
		return
	}
	if input.MaxSpeakers != nil && (*input.MaxSpeakers < 1 || *input.MaxSpeakers > HardMaxSpeakers) {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "max_speakers 取值范围 1..50（硬顶 50）")
		return
	}
	if input.MaxConcurrentScreens != nil && *input.MaxConcurrentScreens < 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "max_concurrent_screens 不能为负数")
		return
	}
	var coModIDs []uuid.UUID
	if input.CoModeratorIDs != nil {
		for _, raw := range *input.CoModeratorIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, "INVALID_REQUEST", "co_moderator_ids 含非法用户 ID")
				return
			}
			coModIDs = append(coModIDs, id)
		}
	}

	states := h.svc.inChannelStates(db, scope.channel.ID)
	oldMode := cfg.Mode
	if input.Mode != nil {
		// STAGE→FREE 且频道人数 >50：禁止（docs 11 Y.4 / 场景 10.3）。
		if oldMode == model.StageModeStage && *input.Mode == model.StageModeFree && len(states) > CapacityWindow {
			fail(c, http.StatusForbidden, "STAGE_REQUIRED_BY_CAPACITY", "频道人数超过 50，必须保持舞台模式")
			return
		}
		cfg.Mode = *input.Mode
	}
	if input.MaxSpeakers != nil {
		cfg.MaxSpeakers = *input.MaxSpeakers
	}
	if input.RequestToSpeakEnabled != nil {
		cfg.RequestToSpeakEnabled = *input.RequestToSpeakEnabled
	}
	if input.AllowCoModChangeMode != nil {
		cfg.AllowCoModChangeMode = *input.AllowCoModChangeMode
	}
	if err := h.svc.saveConfig(db, &cfg); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存舞台配置失败")
		return
	}
	if input.CoModeratorIDs != nil {
		db.Where("channel_id = ?", scope.channel.ID).Delete(&model.StageCoModerator{})
		for _, id := range coModIDs {
			db.Create(&model.StageCoModerator{ID: uuid.New(), GuildID: scope.channel.GuildID, ChannelID: scope.channel.ID, UserID: id})
		}
	}
	if input.MaxConcurrentScreens != nil {
		db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}}, DoUpdates: clause.AssignmentColumns([]string{"max_concurrent_screens", "updated_at"})}).
			Create(&model.ScreenChannelQuota{ChannelID: scope.channel.ID, GuildID: scope.channel.GuildID, MaxConcurrentScreens: *input.MaxConcurrentScreens})
	}

	now := time.Now()
	switch {
	case oldMode == model.StageModeFree && cfg.Mode == model.StageModeStage:
		// FREE→STAGE：可说话更久者优先留台（按进房时间近似，docs 11 Y.3）。
		h.svc.retainSpeakersOnSwitch(db, cfg, states, now)
	case oldMode == model.StageModeStage && cfg.Mode == model.StageModeFree:
		// STAGE→FREE：清除申请队列与席位（docs 11 Y.4；FREE 无 SPEAKER 槽位概念 AA.5）。
		db.Where("channel_id = ?", scope.channel.ID).Delete(&model.StageQueueEntry{})
		db.Where("channel_id = ?", scope.channel.ID).Delete(&model.StageSpeaker{})
		h.svc.publishQueueUpdate(db, scope.channel.GuildID, scope.channel.ID)
	case cfg.Mode == model.StageModeStage:
		// STAGE 下调 max_speakers：按「更久者优先留台」裁剪超额席位。
		var rows []model.StageSpeaker
		db.Where("channel_id = ?", scope.channel.ID).Find(&rows)
		seats := make([]Seat, 0, len(rows))
		for _, row := range rows {
			seats = append(seats, Seat{UserID: row.UserID, Since: row.GrantedAt})
		}
		_, demoted := TrimSpeakers(seats, cfg.MaxSpeakers)
		for _, seat := range demoted {
			h.svc.removeSpeaker(db, scope.channel.GuildID, scope.channel.ID, seat.UserID, "bring_down")
		}
	}

	h.svc.publishInstanceUpdate(cfg)
	h.svc.publishCapsDirty(scope.channel.GuildID, scope.channel.ID, uuid.Nil, "stage_config_changed")
	h.svc.reconcileChannelLocked(scope.channel.GuildID, scope.channel.ID)
	audit.Log(db, audit.Entry{
		ActorID: &scope.user.ID, GuildID: &scope.channel.GuildID,
		Action: "stage.config_update", TargetType: "channel", TargetID: scope.channel.ID.String(),
		Detail: map[string]any{"mode": cfg.Mode, "max_speakers": cfg.MaxSpeakers},
	})
	c.JSON(http.StatusOK, cfg)
}

// ---------- 申请队列 ----------

// apply POST /channels/:channelID/stage/apply（docs 11 AB.2）。
func (h *handlers) apply(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	cfg := h.svc.channelConfig(db, scope.channel.GuildID, scope.channel.ID)
	var queueLen int64
	db.Model(&model.StageQueueEntry{}).Where("channel_id = ?", scope.channel.ID).Count(&queueLen)
	denies := restriction.Denies(scope.user.ID, scope.channel.GuildID, &scope.channel.ID, model.ChannelVoice)
	errCode, idempotent := DecideApply(ApplyInput{
		Mode:           cfg.Mode,
		RequestEnabled: cfg.RequestToSpeakEnabled,
		InChannel:      h.inVoice(scope.channel.ID, scope.user.ID),
		IsSpeaker:      h.svc.isSpeaker(db, scope.channel.ID, scope.user.ID),
		Restricted:     denies.SpeakVoice,
		AlreadyQueued:  h.svc.isQueued(db, scope.channel.ID, scope.user.ID),
		QueueLength:    int(queueLen),
	})
	if errCode != "" {
		status := http.StatusForbidden
		if errCode == "NOT_IN_VOICE" || errCode == "STAGE_NOT_ACTIVE" {
			status = http.StatusBadRequest
		}
		fail(c, status, errCode, applyErrMessage(errCode))
		return
	}
	if !idempotent {
		db.Create(&model.StageQueueEntry{
			ID: uuid.New(), GuildID: scope.channel.GuildID, ChannelID: scope.channel.ID,
			UserID: scope.user.ID, Source: model.StageQueueSourceUserApply, RequestedAt: time.Now(),
		})
		h.svc.publishQueueUpdate(db, scope.channel.GuildID, scope.channel.ID)
		h.svc.publishVoiceState(db, scope.channel.GuildID, scope.channel.ID, scope.user.ID)
	}
	c.JSON(http.StatusOK, gin.H{"status": "QUEUED", "idempotent": idempotent})
}

func applyErrMessage(code string) string {
	switch code {
	case "STAGE_NOT_ACTIVE":
		return "频道未处于舞台模式"
	case "NOT_IN_VOICE":
		return "请先加入该语音频道"
	case "STAGE_ALREADY_SPEAKER":
		return "你已在台上"
	case "RESTRICTED":
		return "你当前被禁说，无法申请上麦"
	case "STAGE_REQUEST_DISABLED":
		return "本频道已关闭申请上麦，仅可由管理抱上"
	case "STAGE_QUEUE_FULL":
		return "申请队列已满（上限 100）"
	}
	return "申请失败"
}

// cancelApply DELETE /channels/:channelID/stage/apply。幂等：不在队列也返回成功。
func (h *handlers) cancelApply(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	result := db.Where("channel_id = ? AND user_id = ?", scope.channel.ID, scope.user.ID).Delete(&model.StageQueueEntry{})
	if result.RowsAffected > 0 {
		h.svc.publishQueueUpdate(db, scope.channel.GuildID, scope.channel.ID)
		h.svc.publishVoiceState(db, scope.channel.GuildID, scope.channel.ID, scope.user.ID)
	}
	c.Status(http.StatusNoContent)
}

// getQueue GET /channels/:channelID/stage/queue：全员可见简表（docs 11 AE.1）；
// 具备队列管理相关节点者附带扩展字段。
func (h *handlers) getQueue(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	db := h.svc.db
	entries := h.svc.queueEntries(db, scope.channel.ID)
	response := gin.H{
		"channel_id": scope.channel.ID.String(),
		"queue":      h.svc.queueBriefs(db, scope.channel.GuildID, entries),
	}
	if rbac.Has(scope.bits, rbac.StageManageQueue) || rbac.Has(scope.bits, rbac.StageBringUp) {
		extended := make([]gin.H, 0, len(entries))
		for i, entry := range entries {
			extended = append(extended, gin.H{
				"position":     i + 1,
				"user_id":      entry.UserID.String(),
				"source":       entry.Source,
				"requested_at": entry.RequestedAt,
			})
		}
		response["queue_extended"] = extended
	}
	c.JSON(http.StatusOK, response)
}

// ---------- 抱麦 ----------

type targetUserRequest struct {
	UserID string `json:"user_id" binding:"required"`
}

// bringUp POST /channels/:channelID/stage/bring-up（docs 11 AB.4/AB.9：可直抱，无需在队）。
func (h *handlers) bringUp(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	if !rbac.Has(scope.bits, rbac.StageBringUp) {
		fail(c, http.StatusForbidden, "FORBIDDEN", "无抱上麦权限")
		return
	}
	var input targetUserRequest
	if !bind(c, &input) {
		return
	}
	targetID, err := uuid.Parse(input.UserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_id 非法")
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	cfg := h.svc.channelConfig(db, scope.channel.GuildID, scope.channel.ID)
	var speakerCount int64
	db.Model(&model.StageSpeaker{}).Where("channel_id = ?", scope.channel.ID).Count(&speakerCount)
	denies := restriction.Denies(targetID, scope.channel.GuildID, &scope.channel.ID, model.ChannelVoice)
	errCode := DecideBringUp(BringUpInput{
		Mode:           cfg.Mode,
		InChannel:      h.inVoice(scope.channel.ID, targetID),
		AlreadySpeaker: h.svc.isSpeaker(db, scope.channel.ID, targetID),
		Restricted:     denies.SpeakVoice,
		SpeakerCount:   int(speakerCount),
		MaxSpeakers:    cfg.MaxSpeakers,
	})
	switch errCode {
	case "":
	case "STAGE_ALREADY_SPEAKER":
		c.JSON(http.StatusOK, gin.H{"status": "SPEAKER", "idempotent": true})
		return
	default:
		status := http.StatusForbidden
		if errCode == "NOT_IN_VOICE" || errCode == "STAGE_NOT_ACTIVE" {
			status = http.StatusBadRequest
		}
		fail(c, status, errCode, bringUpErrMessage(errCode))
		return
	}

	// 抱上：占槽、移出队列、解除容量禁说（docs 11 AB.4 / Z.3「除非被抱上」）。
	db.Create(&model.StageSpeaker{
		ID: uuid.New(), GuildID: scope.channel.GuildID, ChannelID: scope.channel.ID,
		UserID: targetID, GrantedAt: time.Now(),
	})
	db.Where("channel_id = ? AND user_id = ?", scope.channel.ID, targetID).Delete(&model.StageQueueEntry{})
	db.Where("channel_id = ? AND user_id = ?", scope.channel.ID, targetID).Delete(&model.StageCapacityMute{})
	h.svc.publishQueueUpdate(db, scope.channel.GuildID, scope.channel.ID)
	h.svc.publishVoiceState(db, scope.channel.GuildID, scope.channel.ID, targetID)
	h.svc.publishCapsDirty(scope.channel.GuildID, scope.channel.ID, targetID, "bring_up")
	c.JSON(http.StatusOK, gin.H{"status": "SPEAKER"})
}

func bringUpErrMessage(code string) string {
	switch code {
	case "STAGE_NOT_ACTIVE":
		return "频道未处于舞台模式"
	case "NOT_IN_VOICE":
		return "目标用户不在该语音频道"
	case "STAGE_FULL":
		return "台上席位已满，请先抱下他人"
	case "RESTRICTED":
		return "目标用户被禁说，不可上台"
	}
	return "抱上失败"
}

// bringDown POST /channels/:channelID/stage/bring-down（docs 11 AB.5：抱下不回队列）。
func (h *handlers) bringDown(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	if !rbac.Has(scope.bits, rbac.StageBringDown) {
		fail(c, http.StatusForbidden, "FORBIDDEN", "无抱下麦权限")
		return
	}
	var input targetUserRequest
	if !bind(c, &input) {
		return
	}
	targetID, err := uuid.Parse(input.UserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_id 非法")
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	if !h.svc.isSpeaker(db, scope.channel.ID, targetID) {
		fail(c, http.StatusBadRequest, "STAGE_NOT_SPEAKER", "目标用户不在台上")
		return
	}
	h.svc.removeSpeaker(db, scope.channel.GuildID, scope.channel.ID, targetID, "bring_down")
	audit.Log(db, audit.Entry{
		ActorID: &scope.user.ID, GuildID: &scope.channel.GuildID,
		Action: "stage.bring_down", TargetType: "user", TargetID: targetID.String(),
		Detail: map[string]any{"channel_id": scope.channel.ID.String()},
	})
	c.JSON(http.StatusOK, gin.H{"status": "AUDIENCE"})
}

// selfLeave POST /channels/:channelID/stage/self-leave（docs 11 AB.6：主动下麦）。幂等。
func (h *handlers) selfLeave(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	if h.svc.isSpeaker(db, scope.channel.ID, scope.user.ID) {
		h.svc.removeSpeaker(db, scope.channel.GuildID, scope.channel.ID, scope.user.ID, "self")
	}
	c.JSON(http.StatusOK, gin.H{"status": "AUDIENCE"})
}

// ---------- 屏幕共享 ----------

type screenStartRequest struct {
	Quality string `json:"quality"` // 480p / 720p / 1080p，缺省用平台默认档
}

// screenStart POST /channels/:channelID/voice/screen/start（docs 14 §7.1，错误码 §9）。
func (h *handlers) screenStart(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	var input screenStartRequest
	if c.Request.ContentLength > 0 && !bind(c, &input) {
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()

	// 1. 须当前在该语音频道（docs 14 AX.3）。
	if !h.inVoice(scope.channel.ID, scope.user.ID) {
		fail(c, http.StatusBadRequest, "NOT_IN_VOICE", "请先加入该语音频道")
		return
	}
	// 2. RBAC STREAM 位（docs 14 AX.2）。
	if !rbac.Has(scope.bits, rbac.Stream) {
		fail(c, http.StatusForbidden, "STREAM_PERMISSION", "无屏幕共享权限")
		return
	}
	// 3. STAGE 下须为 SPEAKER（docs 14 AX.1）。
	cfg := h.svc.channelConfig(db, scope.channel.GuildID, scope.channel.ID)
	if cfg.Mode == model.StageModeStage && !h.svc.isSpeaker(db, scope.channel.ID, scope.user.ID) {
		fail(c, http.StatusForbidden, "STAGE_SPEAKER_REQUIRED", "舞台模式下仅台上用户可共享")
		return
	}
	// 4. Restriction 未禁说（docs 14 AX.1）。
	denies := restriction.Denies(scope.user.ID, scope.channel.GuildID, &scope.channel.ID, model.ChannelVoice)
	if denies.SpeakVoice {
		fail(c, http.StatusForbidden, "RESTRICTED", "你当前被禁说，无法共享屏幕")
		return
	}
	// 5. 每用户同时 1 路（docs 14 AX.4）。
	var existing int64
	db.Model(&model.ScreenSlot{}).Where("channel_id = ? AND user_id = ?", scope.channel.ID, scope.user.ID).Count(&existing)
	if existing > 0 {
		fail(c, http.StatusConflict, "SCREEN_ALREADY_ACTIVE", "你已有一路屏幕共享")
		return
	}
	// 6. 质量档校验（docs 14 BA / §9）：STREAM_QUALITY 才可选高于平台默认档。
	setting := h.svc.platformSetting(db)
	quality := input.Quality
	if quality == "" {
		quality = setting.DefaultQuality
	}
	maxAllowed := setting.DefaultQuality
	if rbac.Has(scope.bits, rbac.StreamQuality) {
		maxAllowed = setting.MaxQuality
	}
	if !QualityAllowed(quality, maxAllowed) {
		fail(c, http.StatusForbidden, "SCREEN_QUALITY_NOT_ALLOWED", "超过允许的清晰度档位")
		return
	}
	// 7. 配额 = min(频道剩余, 服有效剩余)（docs 14 AY.2）。RESERVED 也计入占用，防超卖（AZ.4）。
	quota := h.svc.screenQuota(db, scope.channel.GuildID)
	var channelUsed int64
	db.Model(&model.ScreenSlot{}).Where("channel_id = ?", scope.channel.ID).Count(&channelUsed)
	channelCap := h.svc.channelScreenCap(db, scope.channel.ID)
	if RemainingScreens(int(channelUsed), channelCap, quota.Used, quota.Effective) <= 0 {
		fail(c, http.StatusConflict, "SCREEN_QUOTA_EXCEEDED", "屏幕共享路数已达上限")
		return
	}

	// 占坑 RESERVED，等待 SFU 上报轨道生效后转 ACTIVE；超时由后台扫释放（docs 14 AZ.4）。
	now := time.Now()
	expires := now.Add(ReservationTTL)
	slot := model.ScreenSlot{
		ID: uuid.New(), GuildID: scope.channel.GuildID, ChannelID: scope.channel.ID,
		UserID: scope.user.ID, State: model.ScreenSlotReserved, QualityTier: quality,
		ReservationExpiresAt: &expires,
	}
	if err := db.Create(&slot).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "预留屏幕配额失败")
		return
	}
	// 通知语音编排为该用户签发 publish_screen caps（docs 14 BC.1 步骤 3）。
	h.svc.publishCapsDirty(scope.channel.GuildID, scope.channel.ID, scope.user.ID, "screen_reserved")
	c.JSON(http.StatusOK, gin.H{
		"slot_id":                slot.ID.String(),
		"state":                  slot.State,
		"quality":                slot.QualityTier,
		"reservation_expires_at": expires,
	})
}

// screenStop POST /channels/:channelID/voice/screen/stop：结束自己的共享。幂等。
func (h *handlers) screenStop(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	var slots []model.ScreenSlot
	db.Where("channel_id = ? AND user_id = ?", scope.channel.ID, scope.user.ID).Find(&slots)
	for _, slot := range slots {
		h.svc.endScreenSlot(db, slot, "self")
		h.svc.publishVoiceState(db, scope.channel.GuildID, scope.channel.ID, scope.user.ID)
	}
	c.Status(http.StatusNoContent)
}

// screenStopUser POST /channels/:channelID/voice/screen/stop-user：STREAM_END_OTHERS 强制结束他人共享（docs 14 AZ.2，审计）。
func (h *handlers) screenStopUser(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	if !rbac.Has(scope.bits, rbac.StreamEndOthers) {
		fail(c, http.StatusForbidden, "FORBIDDEN", "无结束他人共享权限")
		return
	}
	var input targetUserRequest
	if !bind(c, &input) {
		return
	}
	targetID, err := uuid.Parse(input.UserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_id 非法")
		return
	}
	db := h.svc.db
	unlock := h.svc.lockChannel(scope.channel.ID)
	defer unlock()
	var slots []model.ScreenSlot
	db.Where("channel_id = ? AND user_id = ?", scope.channel.ID, targetID).Find(&slots)
	if len(slots) == 0 {
		fail(c, http.StatusNotFound, "SCREEN_NOT_FOUND", "目标用户没有进行中的屏幕共享")
		return
	}
	for _, slot := range slots {
		h.svc.endScreenSlot(db, slot, "admin")
		h.svc.publishVoiceState(db, scope.channel.GuildID, scope.channel.ID, targetID)
	}
	audit.Log(db, audit.Entry{
		ActorID: &scope.user.ID, GuildID: &scope.channel.GuildID,
		Action: "screen.stop_user", TargetType: "user", TargetID: targetID.String(),
		Detail: map[string]any{"channel_id": scope.channel.ID.String()},
	})
	c.Status(http.StatusNoContent)
}

// ---------- 配额查询与系统管接口 ----------

// guildScreenQuota GET /guilds/:guildID/screen-quota（docs 14 §7.2）。
func (h *handlers) guildScreenQuota(c *gin.Context) {
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	user := h.currentUser(c)
	if _, err := perms.LoadGuild(h.svc.db, user, guildID); err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	c.JSON(http.StatusOK, h.svc.screenQuota(h.svc.db, guildID))
}

// requireSystemAdmin 系统管理员专用中间件。
func (h *handlers) requireSystemAdmin(c *gin.Context) {
	if !h.currentUser(c).SystemAdmin {
		fail(c, http.StatusForbidden, "FORBIDDEN", "仅系统管理员可操作")
		c.Abort()
		return
	}
	c.Next()
}

type adminGuildQuotaRequest struct {
	MaxConcurrentScreens int `json:"max_concurrent_screens" binding:"gte=0"`
}

// adminGuildQuota PATCH /admin/guilds/:guildID/screen-quota：系统管调整服基准（docs 14 AY.1）。
func (h *handlers) adminGuildQuota(c *gin.Context) {
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	db := h.svc.db
	var guild model.Guild
	if err := db.First(&guild, "id = ?", guildID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	var input adminGuildQuotaRequest
	if !bind(c, &input) {
		return
	}
	quota := model.ScreenGuildQuota{GuildID: guildID, MaxConcurrentScreens: input.MaxConcurrentScreens}
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "guild_id"}}, DoUpdates: clause.AssignmentColumns([]string{"max_concurrent_screens", "updated_at"})}).
		Create(&quota).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存配额失败")
		return
	}
	user := h.currentUser(c)
	audit.Log(db, audit.Entry{
		ActorID: &user.ID, ActorType: "system_admin", GuildID: &guildID,
		Action: "screen.guild_quota_update", TargetType: "guild", TargetID: guildID.String(),
		Detail: map[string]any{"max_concurrent_screens": input.MaxConcurrentScreens},
	})
	view := h.svc.screenQuota(db, guildID)
	h.svc.bus.Publish(eventbus.Event{
		Type:    eventbus.EventScreenQuotaUpdate,
		GuildID: &guildID,
		Payload: gin.H{"guild_id": guildID.String(), "quota": view},
	})
	c.JSON(http.StatusOK, view)
}

type adminSettingsRequest struct {
	DynamicEnabled  *bool    `json:"dynamic_screen_quota_enabled"`
	GentleEndOldest *bool    `json:"gentle_end_oldest"`
	DefaultQuality  *string  `json:"default_quality"`
	MaxQuality      *string  `json:"max_quality"`
	Weight480p      *float64 `json:"weight_480p"`
	Weight720p      *float64 `json:"weight_720p"`
	Weight1080p     *float64 `json:"weight_1080p"`
}

// adminSettings PATCH /admin/screen-quota/settings：平台动态开关与权重（docs 14 AY.4 / BD）。
func (h *handlers) adminSettings(c *gin.Context) {
	var input adminSettingsRequest
	if !bind(c, &input) {
		return
	}
	for _, quality := range []*string{input.DefaultQuality, input.MaxQuality} {
		if quality != nil {
			if _, ok := qualityRank[*quality]; !ok {
				fail(c, http.StatusBadRequest, "INVALID_REQUEST", "清晰度档位只能为 480p/720p/1080p")
				return
			}
		}
	}
	db := h.svc.db
	setting := h.svc.platformSetting(db)
	if input.DynamicEnabled != nil {
		setting.DynamicEnabled = *input.DynamicEnabled
	}
	if input.GentleEndOldest != nil {
		setting.GentleEndOldest = *input.GentleEndOldest
	}
	if input.DefaultQuality != nil {
		setting.DefaultQuality = *input.DefaultQuality
	}
	if input.MaxQuality != nil {
		setting.MaxQuality = *input.MaxQuality
	}
	if input.Weight480p != nil {
		setting.Weight480p = *input.Weight480p
	}
	if input.Weight720p != nil {
		setting.Weight720p = *input.Weight720p
	}
	if input.Weight1080p != nil {
		setting.Weight1080p = *input.Weight1080p
	}
	setting.ID = 1
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).Create(&setting).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存平台设置失败")
		return
	}
	user := h.currentUser(c)
	audit.Log(db, audit.Entry{
		ActorID: &user.ID, ActorType: "system_admin",
		Action: "screen.platform_settings_update", TargetType: "platform", TargetID: "screen_quota",
		Detail: map[string]any{"dynamic_enabled": setting.DynamicEnabled, "gentle_end_oldest": setting.GentleEndOldest},
	})
	c.JSON(http.StatusOK, setting)
}
