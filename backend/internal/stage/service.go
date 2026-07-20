package stage

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// service 舞台/屏幕共享的领域服务，持有依赖并串联 DB 状态与事件发布。
type service struct {
	db  *gorm.DB
	bus *eventbus.Bus
	// locks 按频道加锁，避免并发进出房/抱麦对同一频道的状态竞争（单实例部署假设）。
	locks sync.Map // channelID -> *sync.Mutex
}

func (s *service) lockChannel(channelID uuid.UUID) func() {
	value, _ := s.locks.LoadOrStore(channelID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ---------- 配置与角色 ----------

// channelConfig 读取频道舞台配置；无记录时返回默认值（不落库）。
func (s *service) channelConfig(db *gorm.DB, guildID, channelID uuid.UUID) model.StageChannelConfig {
	var cfg model.StageChannelConfig
	if err := db.First(&cfg, "channel_id = ?", channelID).Error; err != nil {
		return model.StageChannelConfig{
			ChannelID:             channelID,
			GuildID:               guildID,
			Mode:                  model.StageModeFree,
			MaxSpeakers:           DefaultMaxSpeakers,
			RequestToSpeakEnabled: true,
			AllowCoModChangeMode:  true,
		}
	}
	return cfg
}

// saveConfig 落库舞台配置。配置可能从未持久化（channelConfig 返回默认值），
// 必须用 upsert 而非 Save（Save 对不存在的行只会 UPDATE 0 行）。
func (s *service) saveConfig(db *gorm.DB, cfg *model.StageChannelConfig) error {
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}}, UpdateAll: true}).Create(cfg).Error
}

func (s *service) isSpeaker(db *gorm.DB, channelID, userID uuid.UUID) bool {
	var count int64
	db.Model(&model.StageSpeaker{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
	return count > 0
}

func (s *service) isQueued(db *gorm.DB, channelID, userID uuid.UUID) bool {
	var count int64
	db.Model(&model.StageQueueEntry{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
	return count > 0
}

func (s *service) isCapacityMuted(db *gorm.DB, channelID, userID uuid.UUID) bool {
	var count int64
	db.Model(&model.StageCapacityMute{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
	return count > 0
}

func (s *service) isCoModerator(db *gorm.DB, channelID, userID uuid.UUID) bool {
	var count int64
	db.Model(&model.StageCoModerator{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
	return count > 0
}

// roleOf 返回舞台角色；FREE 模式返回 RoleNone（docs 11 §6.2）。
func (s *service) roleOf(db *gorm.DB, guildID, channelID, userID uuid.UUID) string {
	cfg := s.channelConfig(db, guildID, channelID)
	if cfg.Mode != model.StageModeStage {
		return RoleNone
	}
	if s.isSpeaker(db, channelID, userID) {
		return RoleSpeaker
	}
	if s.isQueued(db, channelID, userID) {
		return RoleQueued
	}
	return RoleAudience
}

// guildOfChannel 查频道所属 guild（钩子函数只拿到 channelID 时使用）。
func (s *service) guildOfChannel(db *gorm.DB, channelID uuid.UUID) (uuid.UUID, bool) {
	var channel model.Channel
	if err := db.Select("guild_id").First(&channel, "id = ?", channelID).Error; err != nil {
		return uuid.Nil, false
	}
	return channel.GuildID, true
}

// ---------- 事件发布 ----------

// stageVoiceStatePayload 舞台侧发布的 VOICE_STATE_UPDATE 增量载荷（带 stage_role，docs 11 §8.2）。
// Gateway 消费后合并到语音状态下发；本包订阅时据此识别并跳过自己发布的事件，避免自触发循环。
type stageVoiceStatePayload struct {
	GuildID       string `json:"guild_id"`
	ChannelID     string `json:"channel_id"`
	UserID        string `json:"user_id"`
	StageRole     string `json:"stage_role"`
	CapacityMuted bool   `json:"capacity_muted"`
	SelfStream    bool   `json:"self_stream"`
}

func (s *service) publishVoiceState(db *gorm.DB, guildID, channelID, userID uuid.UUID) {
	role := s.roleOf(db, guildID, channelID, userID)
	var slotCount int64
	db.Model(&model.ScreenSlot{}).Where("channel_id = ? AND user_id = ? AND state = ?", channelID, userID, model.ScreenSlotActive).Count(&slotCount)
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventVoiceStateUpdate,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload: stageVoiceStatePayload{
			GuildID:       guildID.String(),
			ChannelID:     channelID.String(),
			UserID:        userID.String(),
			StageRole:     role,
			CapacityMuted: s.isCapacityMuted(db, channelID, userID),
			SelfStream:    slotCount > 0,
		},
	})
}

// publishCapsDirty 通知语音编排重算 caps（userID 为 uuid.Nil 表示整频道重算）。
func (s *service) publishCapsDirty(guildID, channelID, userID uuid.UUID, reason string) {
	payload := eventbus.CapsDirtyPayload{GuildID: guildID.String(), ChannelID: channelID.String(), Reason: reason}
	if userID != uuid.Nil {
		payload.UserID = userID.String()
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.InternalCapsDirty,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload:   payload,
	})
}

// queueBrief 队列全员简表条目（docs 11 AE.1：id/昵称/序）。
type queueBrief struct {
	Position int    `json:"position"`
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
}

// queueEntries 按 FIFO 顺序取队列。
func (s *service) queueEntries(db *gorm.DB, channelID uuid.UUID) []model.StageQueueEntry {
	var entries []model.StageQueueEntry
	db.Where("channel_id = ?", channelID).Order("requested_at ASC, id ASC").Find(&entries)
	return entries
}

// queueBriefs 组装简表（昵称优先成员昵称，回落用户名）。
func (s *service) queueBriefs(db *gorm.DB, guildID uuid.UUID, entries []model.StageQueueEntry) []queueBrief {
	briefs := make([]queueBrief, 0, len(entries))
	for i, entry := range entries {
		name := ""
		var member model.Member
		if err := db.Select("nickname").First(&member, "guild_id = ? AND user_id = ?", guildID, entry.UserID).Error; err == nil {
			name = member.Nickname
		}
		if name == "" {
			var user model.User
			if err := db.Select("username").First(&user, "id = ?", entry.UserID).Error; err == nil {
				name = user.Username
			}
		}
		briefs = append(briefs, queueBrief{Position: i + 1, UserID: entry.UserID.String(), Name: name})
	}
	return briefs
}

func (s *service) publishQueueUpdate(db *gorm.DB, guildID, channelID uuid.UUID) {
	entries := s.queueEntries(db, channelID)
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventStageQueueUpdate,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload: map[string]any{
			"guild_id":   guildID.String(),
			"channel_id": channelID.String(),
			"queue":      s.queueBriefs(db, guildID, entries),
		},
	})
}

func (s *service) publishInstanceUpdate(cfg model.StageChannelConfig) {
	guildID, channelID := cfg.GuildID, cfg.ChannelID
	// PATCH voice-stage 可改协管名单与频道屏幕并发上限：载荷补齐两项，
	// 客户端无需回源 GET /voice-stage 即可全量更新本地配置（实时同步专项）。
	coModIDs := []string{}
	var coMods []model.StageCoModerator
	if err := s.db.Where("channel_id = ?", channelID).Find(&coMods).Error; err == nil {
		for _, mod := range coMods {
			coModIDs = append(coModIDs, mod.UserID.String())
		}
	}
	maxScreens := -1 // -1 = 未独立配置（跟随默认），与 GET /voice-stage 语义一致
	var quota model.ScreenChannelQuota
	if err := s.db.First(&quota, "channel_id = ?", channelID).Error; err == nil {
		maxScreens = quota.MaxConcurrentScreens
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventStageInstanceUpdate,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload: map[string]any{
			"guild_id":                 guildID.String(),
			"channel_id":               channelID.String(),
			"mode":                     cfg.Mode,
			"max_speakers":             cfg.MaxSpeakers,
			"request_to_speak_enabled": cfg.RequestToSpeakEnabled,
			"allow_co_mod_change_mode": cfg.AllowCoModChangeMode,
			"co_moderator_ids":         coModIDs,
			"max_concurrent_screens":   maxScreens,
		},
	})
}

// ---------- 频道人数与容量禁说 ----------

// inChannelStates 频道内全部 VoiceState（含 pending / 断线宽限中的行，docs 11 Z.7），按进房时间升序。
func (s *service) inChannelStates(db *gorm.DB, channelID uuid.UUID) []model.VoiceState {
	var states []model.VoiceState
	db.Where("channel_id = ?", channelID).Order("joined_at ASC NULLS LAST, created_at ASC").Find(&states)
	return states
}

// reconcileChannel 以 VoiceState 为准全量校正频道舞台状态：
//  1. >50 强制 STAGE（docs 11 Z.2）；
//  2. 已离房用户释放席位/队位/屏幕坑；断线用户打宽限标记，重连清除；
//  3. 按进房序重算容量禁说 FIFO 标记/解除（docs 11 Z.3–Z.6）。
//
// 幂等：无变化时不发布任何事件，因此订阅自身发布的 VOICE_STATE_UPDATE 不会造成循环。
func (s *service) reconcileChannel(guildID, channelID uuid.UUID) {
	unlock := s.lockChannel(channelID)
	defer unlock()
	s.reconcileChannelLocked(guildID, channelID)
}

func (s *service) reconcileChannelLocked(guildID, channelID uuid.UUID) {
	db := s.db
	now := time.Now()
	states := s.inChannelStates(db, channelID)
	cfg := s.channelConfig(db, guildID, channelID)

	// 1. >50 强制 STAGE。人数回落 ≤50 不自动回 FREE（docs 11 Y.5）。
	if len(states) > CapacityWindow && cfg.Mode != model.StageModeStage {
		cfg.Mode = model.StageModeStage
		if err := s.saveConfig(db, &cfg); err != nil {
			log.Printf("stage: 强制 STAGE 落库失败 channel=%s err=%v", channelID, err)
			return
		}
		// FREE→STAGE 留台裁剪：可说话更久者优先留台（按进房时间近似，docs 11 Y.3）。
		s.retainSpeakersOnSwitch(db, cfg, states, now)
		s.publishInstanceUpdate(cfg)
		s.publishCapsDirty(guildID, channelID, uuid.Nil, "forced_stage_by_capacity")
	}

	// 2. 在房/断线/离房三种存在状态同步。
	present := make(map[uuid.UUID]bool, len(states))
	connected := make(map[uuid.UUID]bool, len(states))
	for _, state := range states {
		present[state.UserID] = true
		connected[state.UserID] = state.Connected
	}
	s.syncPresence(db, guildID, channelID, present, connected, now)

	// 3. 容量禁说 FIFO 重算（窗口 50；SPEAKER 免疫）。
	ordered := make([]uuid.UUID, 0, len(states))
	for _, state := range states {
		ordered = append(ordered, state.UserID)
	}
	speakers := map[uuid.UUID]bool{}
	var speakerRows []model.StageSpeaker
	db.Where("channel_id = ?", channelID).Find(&speakerRows)
	for _, row := range speakerRows {
		speakers[row.UserID] = true
	}
	muted := map[uuid.UUID]bool{}
	var muteRows []model.StageCapacityMute
	db.Where("channel_id = ?", channelID).Find(&muteRows)
	for _, row := range muteRows {
		muted[row.UserID] = true
	}
	toMute, toUnmute := DiffCapacityMutes(ordered, speakers, muted, CapacityWindow)

	queueChanged := false
	for _, userID := range toMute {
		db.Create(&model.StageCapacityMute{ID: uuid.New(), GuildID: guildID, ChannelID: channelID, UserID: userID, MutedAt: now})
		// 被容量禁说者自动进申请队尾（docs 11 Z.6），队满则只禁说不入队。
		if cfg.Mode == model.StageModeStage && !s.isQueued(db, channelID, userID) {
			var queueLen int64
			db.Model(&model.StageQueueEntry{}).Where("channel_id = ?", channelID).Count(&queueLen)
			if queueLen < MaxQueueLength {
				db.Create(&model.StageQueueEntry{
					ID: uuid.New(), GuildID: guildID, ChannelID: channelID, UserID: userID,
					Source: model.StageQueueSourceCapacity, RequestedAt: now,
				})
				queueChanged = true
			}
		}
		s.publishVoiceState(db, guildID, channelID, userID)
		s.publishCapsDirty(guildID, channelID, userID, "capacity_muted")
	}
	for _, userID := range toUnmute {
		db.Where("channel_id = ? AND user_id = ?", channelID, userID).Delete(&model.StageCapacityMute{})
		if present[userID] {
			s.publishVoiceState(db, guildID, channelID, userID)
			s.publishCapsDirty(guildID, channelID, userID, "capacity_unmuted")
		}
	}
	if queueChanged {
		s.publishQueueUpdate(db, guildID, channelID)
	}
}

// syncPresence 同步席位/队位/屏幕坑与 VoiceState 的存在关系：
//   - 用户已不在频道 → 立即释放（离房，docs 11 场景；屏幕共享 reason=disconnect）；
//   - 用户在频道但未连接 → 打断线宽限标记（60s 后由后台扫释放，docs 11 AC.3 / docs 14 BB.4）；
//   - 用户已连接 → 清除宽限标记（重连恢复原状态）。
func (s *service) syncPresence(db *gorm.DB, guildID, channelID uuid.UUID, present, connected map[uuid.UUID]bool, now time.Time) {
	queueChanged := false

	var speakerRows []model.StageSpeaker
	db.Where("channel_id = ?", channelID).Find(&speakerRows)
	for _, row := range speakerRows {
		switch {
		case !present[row.UserID]:
			s.removeSpeaker(db, guildID, channelID, row.UserID, "leave")
		case !connected[row.UserID] && row.DisconnectedAt == nil:
			db.Model(&model.StageSpeaker{}).Where("id = ?", row.ID).Update("disconnected_at", now)
		case connected[row.UserID] && row.DisconnectedAt != nil:
			db.Model(&model.StageSpeaker{}).Where("id = ?", row.ID).Update("disconnected_at", nil)
			// 重连恢复：重发 caps 让 SFU 恢复 publish（docs 11 AC.3）。
			s.publishCapsDirty(guildID, channelID, row.UserID, "speaker_reconnected")
		}
	}

	var queueRows []model.StageQueueEntry
	db.Where("channel_id = ?", channelID).Find(&queueRows)
	for _, row := range queueRows {
		switch {
		case !present[row.UserID]:
			db.Delete(&model.StageQueueEntry{}, "id = ?", row.ID)
			queueChanged = true
		case !connected[row.UserID] && row.DisconnectedAt == nil:
			db.Model(&model.StageQueueEntry{}).Where("id = ?", row.ID).Update("disconnected_at", now)
		case connected[row.UserID] && row.DisconnectedAt != nil:
			db.Model(&model.StageQueueEntry{}).Where("id = ?", row.ID).Update("disconnected_at", nil)
		}
	}

	var slotRows []model.ScreenSlot
	db.Where("channel_id = ?", channelID).Find(&slotRows)
	for _, row := range slotRows {
		switch {
		case !present[row.UserID]:
			s.endScreenSlot(db, row, "disconnect")
		case !connected[row.UserID] && row.DisconnectedAt == nil:
			db.Model(&model.ScreenSlot{}).Where("id = ?", row.ID).Update("disconnected_at", now)
		case connected[row.UserID] && row.DisconnectedAt != nil:
			db.Model(&model.ScreenSlot{}).Where("id = ?", row.ID).Update("disconnected_at", nil)
			s.publishCapsDirty(guildID, channelID, row.UserID, "screen_reconnected")
		}
	}

	if queueChanged {
		s.publishQueueUpdate(db, guildID, channelID)
	}
}

// retainSpeakersOnSwitch FREE→STAGE 切换时的留台：按进房时间升序（近似「可说话更久」）授予前
// max_speakers 名 SPEAKER 席位；容量禁说与 Restriction 禁说者不授予（docs 11 Y.3 / AD.1）。
// 注：文档语义为「开麦/成为可说状态更久」，控制面无逐用户开麦时长数据，以进房时间近似。
func (s *service) retainSpeakersOnSwitch(db *gorm.DB, cfg model.StageChannelConfig, states []model.VoiceState, now time.Time) {
	granted := 0
	for _, state := range states {
		if granted >= cfg.MaxSpeakers {
			break
		}
		if s.isCapacityMuted(db, cfg.ChannelID, state.UserID) {
			continue
		}
		denies := restriction.Denies(state.UserID, cfg.GuildID, &cfg.ChannelID, model.ChannelVoice)
		if denies.SpeakVoice {
			continue
		}
		if !s.isSpeaker(db, cfg.ChannelID, state.UserID) {
			// 席位授予时间取进房时间，保证后续裁剪仍按「更久者优先」排序。
			since := now
			if state.JoinedAt != nil {
				since = *state.JoinedAt
			}
			db.Create(&model.StageSpeaker{
				ID: uuid.New(), GuildID: cfg.GuildID, ChannelID: cfg.ChannelID, UserID: state.UserID, GrantedAt: since,
			})
		}
		granted++
	}
}

// ---------- 席位变更 ----------

// removeSpeaker 移除 SPEAKER 席位（抱下/下麦/离房共用），并自动结束其屏幕共享（docs 14 BB.1）。
// reason：bring_down / self / leave / timeout。
func (s *service) removeSpeaker(db *gorm.DB, guildID, channelID, userID uuid.UUID, reason string) {
	db.Where("channel_id = ? AND user_id = ?", channelID, userID).Delete(&model.StageSpeaker{})
	var slots []model.ScreenSlot
	db.Where("channel_id = ? AND user_id = ?", channelID, userID).Find(&slots)
	slotReason := "demote"
	if reason == "leave" || reason == "timeout" {
		slotReason = "disconnect"
	}
	for _, slot := range slots {
		s.endScreenSlot(db, slot, slotReason)
	}
	s.publishCapsDirty(guildID, channelID, userID, "speaker_removed_"+reason)
	s.publishVoiceState(db, guildID, channelID, userID)
}

// ---------- 屏幕共享 ----------

// endScreenSlot 释放屏幕坑并广播结束事件。reason：self / admin / demote / quota / disconnect / timeout。
func (s *service) endScreenSlot(db *gorm.DB, slot model.ScreenSlot, reason string) {
	db.Delete(&model.ScreenSlot{}, "id = ?", slot.ID)
	guildID, channelID := slot.GuildID, slot.ChannelID
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventScreenShareStop,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload: map[string]any{
			"guild_id":   guildID.String(),
			"channel_id": channelID.String(),
			"user_id":    slot.UserID.String(),
			"reason":     reason,
		},
	})
	s.publishCapsDirty(guildID, channelID, slot.UserID, "screen_stopped_"+reason)
}

// guildScreenBase 服基准上限（无记录默认 3，docs 14 AY.7）。
func (s *service) guildScreenBase(db *gorm.DB, guildID uuid.UUID) int {
	var quota model.ScreenGuildQuota
	if err := db.First(&quota, "guild_id = ?", guildID).Error; err != nil {
		return DefaultGuildScreens
	}
	return quota.MaxConcurrentScreens
}

// channelScreenCap 频道上限（无记录默认 2，docs 14 AY.2）。
func (s *service) channelScreenCap(db *gorm.DB, channelID uuid.UUID) int {
	var quota model.ScreenChannelQuota
	if err := db.First(&quota, "channel_id = ?", channelID).Error; err != nil {
		return DefaultChannelScreens
	}
	return quota.MaxConcurrentScreens
}

// platformSetting 平台设置（无记录返回默认值）。
func (s *service) platformSetting(db *gorm.DB) model.ScreenPlatformSetting {
	var setting model.ScreenPlatformSetting
	if err := db.First(&setting, "id = 1").Error; err != nil {
		return model.ScreenPlatformSetting{
			ID: 1, DynamicEnabled: true, GentleEndOldest: true,
			DefaultQuality: "720p", MaxQuality: "1080p",
			Weight480p: 1.0, Weight720p: 1.5, Weight1080p: 2.5,
		}
	}
	return setting
}

// screenQuotaView 聚合某服的配额视图：基准 / 动态 cap / 有效上限 / 已占用。
type screenQuotaView struct {
	Base           int  `json:"base_limit"`
	DynamicEnabled bool `json:"dynamic_enabled"`
	DynamicCap     int  `json:"dynamic_cap"`
	Effective      int  `json:"effective_limit"`
	Used           int  `json:"used"` // RESERVED + ACTIVE（占坑即计数，防超卖）
}

func (s *service) screenQuota(db *gorm.DB, guildID uuid.UUID) screenQuotaView {
	base := s.guildScreenBase(db, guildID)
	setting := s.platformSetting(db)
	dynamicCap := base
	if setting.DynamicEnabled {
		dynamicCap = DynamicScreenCap(s.poolLoads(guildID), base)
	}
	var used int64
	db.Model(&model.ScreenSlot{}).Where("guild_id = ?", guildID).Count(&used)
	return screenQuotaView{
		Base:           base,
		DynamicEnabled: setting.DynamicEnabled,
		DynamicCap:     dynamicCap,
		Effective:      EffectiveGuildCap(base, setting.DynamicEnabled, dynamicCap),
		Used:           int(used),
	}
}

// poolLoads 从 sfuctl 目录取服节点池负载快照（映射为纯逻辑输入）。
func (s *service) poolLoads(guildID uuid.UUID) []NodeLoad {
	nodes, err := sfuDirPoolNodes(guildID)
	if err != nil {
		log.Printf("stage: 读取节点池负载失败 guild=%s err=%v", guildID, err)
		return nil
	}
	loads := make([]NodeLoad, 0, len(nodes))
	for _, node := range nodes {
		if !node.Online {
			continue
		}
		loads = append(loads, NodeLoad{
			CPUPercent:   node.CPUPercent,
			MaxUsers:     node.MaxUsers,
			CurrentUsers: node.CurrentUsers,
			ScreenTracks: node.ScreenTracks,
		})
	}
	return loads
}
