package stage

import (
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// backgroundLoop 周期执行各类超时扫描与动态配额校正。
func (s *service) backgroundLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastEffective := map[uuid.UUID]int{}
	for range ticker.C {
		s.scanQueueExpiry()
		s.scanDisconnectGrace()
		s.scanReservationExpiry()
		s.enforceDynamicQuota(lastEffective)
	}
}

// scanQueueExpiry 申请队列条目 30 分钟无处理过期移除（docs 11 AC.2，用户可重新申请）。
// 仅过期 USER_APPLY / ADMIN 来源；CAPACITY_QUEUE 条目在禁说仍生效期间保留（见实现偏差说明）。
func (s *service) scanQueueExpiry() {
	deadline := time.Now().Add(-QueueEntryTTL)
	var expired []model.StageQueueEntry
	s.db.Where("requested_at < ? AND source <> ?", deadline, model.StageQueueSourceCapacity).Find(&expired)
	touched := map[uuid.UUID]uuid.UUID{} // channelID -> guildID
	for _, entry := range expired {
		unlock := s.lockChannel(entry.ChannelID)
		s.db.Delete(&model.StageQueueEntry{}, "id = ?", entry.ID)
		s.publishVoiceState(s.db, entry.GuildID, entry.ChannelID, entry.UserID)
		unlock()
		touched[entry.ChannelID] = entry.GuildID
	}
	for channelID, guildID := range touched {
		s.publishQueueUpdate(s.db, guildID, channelID)
	}
}

// scanDisconnectGrace 断线超过 60s：释放 SPEAKER 席位、队位与屏幕坑（docs 11 AC.3 / docs 14 BB.4）。
func (s *service) scanDisconnectGrace() {
	deadline := time.Now().Add(-DisconnectGrace)

	var speakers []model.StageSpeaker
	s.db.Where("disconnected_at IS NOT NULL AND disconnected_at < ?", deadline).Find(&speakers)
	for _, row := range speakers {
		unlock := s.lockChannel(row.ChannelID)
		s.removeSpeaker(s.db, row.GuildID, row.ChannelID, row.UserID, "timeout")
		unlock()
	}

	var entries []model.StageQueueEntry
	s.db.Where("disconnected_at IS NOT NULL AND disconnected_at < ?", deadline).Find(&entries)
	touched := map[uuid.UUID]uuid.UUID{}
	for _, entry := range entries {
		unlock := s.lockChannel(entry.ChannelID)
		s.db.Delete(&model.StageQueueEntry{}, "id = ?", entry.ID)
		unlock()
		touched[entry.ChannelID] = entry.GuildID
	}
	for channelID, guildID := range touched {
		s.publishQueueUpdate(s.db, guildID, channelID)
	}

	var slots []model.ScreenSlot
	s.db.Where("disconnected_at IS NOT NULL AND disconnected_at < ?", deadline).Find(&slots)
	for _, slot := range slots {
		unlock := s.lockChannel(slot.ChannelID)
		s.endScreenSlot(s.db, slot, "disconnect")
		unlock()
	}
}

// scanReservationExpiry RESERVED 占坑超时未确认发布 → 释放，防超卖（docs 14 AZ.4）。
func (s *service) scanReservationExpiry() {
	now := time.Now()
	var slots []model.ScreenSlot
	s.db.Where("state = ? AND reservation_expires_at IS NOT NULL AND reservation_expires_at < ?", model.ScreenSlotReserved, now).Find(&slots)
	for _, slot := range slots {
		unlock := s.lockChannel(slot.ChannelID)
		s.endScreenSlot(s.db, slot, "timeout")
		unlock()
	}
}

// enforceDynamicQuota 动态降额导致占用超过有效上限时：拒新由 start 路径天然实现；
// 温和策略开启时结束「最早开启」的共享直至回到上限内（docs 14 AZ.3）。
// 有效上限变化时广播 SCREEN_QUOTA_UPDATE 便于 UI 展示。
func (s *service) enforceDynamicQuota(lastEffective map[uuid.UUID]int) {
	setting := s.platformSetting(s.db)
	var guildIDs []uuid.UUID
	s.db.Model(&model.ScreenSlot{}).Distinct("guild_id").Pluck("guild_id", &guildIDs)
	for _, guildID := range guildIDs {
		view := s.screenQuota(s.db, guildID)
		if previous, seen := lastEffective[guildID]; !seen || previous != view.Effective {
			lastEffective[guildID] = view.Effective
			gid := guildID
			s.bus.Publish(eventbus.Event{
				Type:    eventbus.EventScreenQuotaUpdate,
				GuildID: &gid,
				Payload: map[string]any{"guild_id": guildID.String(), "quota": view},
			})
		}
		if !setting.DynamicEnabled || !setting.GentleEndOldest {
			continue
		}
		excess := view.Used - view.Effective
		if excess <= 0 {
			continue
		}
		// 结束最早开启的共享（ACTIVE 按 started_at，RESERVED 按 created_at 视为更晚）。
		var slots []model.ScreenSlot
		s.db.Where("guild_id = ?", guildID).
			Order("COALESCE(started_at, created_at) ASC, created_at ASC").
			Limit(excess).Find(&slots)
		for _, slot := range slots {
			unlock := s.lockChannel(slot.ChannelID)
			s.endScreenSlot(s.db, slot, "quota")
			s.publishVoiceState(s.db, slot.GuildID, slot.ChannelID, slot.UserID)
			unlock()
		}
	}
}

// confirmScreenActive SFU 上报轨道生效：RESERVED → ACTIVE，广播 SCREEN_SHARE_START（docs 14 BC.1）。
func (s *service) confirmScreenActive(channelID, userID uuid.UUID) {
	unlock := s.lockChannel(channelID)
	defer unlock()
	var slot model.ScreenSlot
	if err := s.db.First(&slot, "channel_id = ? AND user_id = ?", channelID, userID).Error; err != nil {
		return
	}
	if slot.State == model.ScreenSlotActive {
		return
	}
	now := time.Now()
	s.db.Model(&model.ScreenSlot{}).Where("id = ?", slot.ID).Updates(map[string]any{
		"state": model.ScreenSlotActive, "started_at": now, "reservation_expires_at": nil,
	})
	guildID := slot.GuildID
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventScreenShareStart,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload: map[string]any{
			"guild_id":   guildID.String(),
			"channel_id": channelID.String(),
			"user_id":    userID.String(),
			"quality":    slot.QualityTier,
		},
	})
	s.publishVoiceState(s.db, guildID, channelID, userID)
}
