package message

import (
	"encoding/json"
	"log"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
)

// ephemeral 与差异化按钮的定向/分组分发（设计文档 2026-07-26）。
//
// hub 对每个事件只序列化一次 payload、所有接收者共享（gateway/hub.go）。
// 因此「同一条消息不同人看到不同按钮」通过发布多次事件实现：每个可见性组
// 一次 UserIDs 定向 Publish；事件携带 GuildID+ChannelID，hub 的 UserIDs 路径
// 会对上锁频道补充 CanAccessChannelContent 过滤（防内容推进未解锁频道）。

// ephemeralTargets ephemeral 事件接收者：可见名单 ∪ 作者（去重）。
func ephemeralTargets(message model.Message) []uuid.UUID {
	targets := make([]uuid.UUID, 0, len(message.VisibleTo)+1)
	seen := make(map[uuid.UUID]struct{}, len(message.VisibleTo)+1)
	for _, id := range append(model.UUIDList{message.AuthorID}, message.VisibleTo...) {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
	}
	return targets
}

// publishEphemeralScopedEvent 以 ephemeral 目标集定向发布任意 payload
//（MESSAGE_DELETE 等无 view 的轻载荷事件用）。
func (s *service) publishEphemeralScopedEvent(eventType string, message model.Message, payload any) {
	event := eventbus.Event{
		Type:      eventType,
		ChannelID: &message.ChannelID,
		UserIDs:   ephemeralTargets(message),
		Payload:   payload,
	}
	if message.GuildID != uuid.Nil {
		guildID := message.GuildID
		event.GuildID = &guildID
	}
	s.bus.Publish(event)
}

// publishEphemeralMessageEvent ephemeral 消息事件：目标 ≤ 21 人。
// 无差异化按钮时全体共享同一 payload；有则逐组裁剪（作者恒见全量）。
func (s *service) publishEphemeralMessageEvent(eventType string, message model.Message, view messageView) {
	if !cardNeedsTrim(message.Card) {
		s.publishEphemeralScopedEvent(eventType, message, view)
		return
	}
	buttons, err := parseCardButtons(*message.Card)
	if err != nil || len(buttons) == 0 {
		s.publishEphemeralScopedEvent(eventType, message, view)
		return
	}
	s.publishTrimmedGroups(eventType, message, view, buttons, ephemeralTargets(message))
}

// publishGroupedMessageEvent 含差异化按钮的公开消息：接收者 = 频道可见成员
//（DM 为 recipients），按可见位图分组后逐组定向推送。
// view 为「无 viewer 视图」（restricted 按钮已被安全裁剪），仅在解析异常时兜底使用。
func (s *service) publishGroupedMessageEvent(eventType string, message model.Message, view messageView) {
	buttons, err := parseCardButtons(*message.Card)
	if err != nil || len(buttons) == 0 {
		// 理论不可达（发送期已校验）：退回安全裁剪后的广播视图。
		s.publishChannelScopedEvent(eventType, message.GuildID, message.ChannelID, view)
		return
	}
	var viewers []uuid.UUID
	if message.GuildID == uuid.Nil {
		viewers = s.loadDMRecipientIDs(message.ChannelID)
	} else {
		viewers, err = snapshot.ChannelViewers(s.db, message.GuildID, message.ChannelID)
		if err != nil {
			log.Printf("message: 计算频道可见成员失败（message=%d）: %v", message.ID, err)
			return
		}
	}
	s.publishTrimmedGroups(eventType, message, view, buttons, viewers)
}

// publishTrimmedGroups 计算每个接收者的按钮可见位图并按位图分组：
// 每组只裁剪/序列化一次；作者恒为全量位图组。
func (s *service) publishTrimmedGroups(eventType string, message model.Message, view messageView, buttons []cardButton, recipients []uuid.UUID) {
	if len(recipients) == 0 {
		return
	}
	userRoles := s.recipientRoles(message.GuildID, recipients, buttons)
	full := fullVisibilityBitmap(len(buttons))
	groups := make(map[uint32][]uuid.UUID, 2)
	for _, userID := range recipients {
		bitmap := buttonVisibilityBitmap(buttons, userID, userRoles[userID])
		if userID == message.AuthorID {
			bitmap = full
		}
		groups[bitmap] = append(groups[bitmap], userID)
	}
	for bitmap, userIDs := range groups {
		groupView := view
		groupView.Card = json.RawMessage(trimCardButtons(*message.Card, buttons, bitmap))
		event := eventbus.Event{
			Type:      eventType,
			ChannelID: &message.ChannelID,
			UserIDs:   userIDs,
			Payload:   groupView,
		}
		if message.GuildID != uuid.Nil {
			guildID := message.GuildID
			event.GuildID = &guildID
		}
		s.bus.Publish(event)
	}
}

// recipientRoles 批量取接收者在本服的角色集合（仅当按钮引用了角色时才查询；
// 只取按钮引用到的角色，DM 域恒为空）。
func (s *service) recipientRoles(guildID uuid.UUID, recipients []uuid.UUID, buttons []cardButton) map[uuid.UUID]map[uuid.UUID]bool {
	if guildID == uuid.Nil {
		return nil
	}
	referenced := make([]uuid.UUID, 0, 4)
	seen := make(map[uuid.UUID]struct{}, 4)
	for _, button := range buttons {
		if button.VisibleTo == nil {
			continue
		}
		for _, roleID := range button.VisibleTo.Roles {
			if _, ok := seen[roleID]; !ok {
				seen[roleID] = struct{}{}
				referenced = append(referenced, roleID)
			}
		}
	}
	if len(referenced) == 0 {
		return nil
	}
	var rows []struct {
		UserID uuid.UUID
		RoleID uuid.UUID
	}
	err := s.db.Raw(`SELECT members.user_id, member_roles.role_id FROM members
		JOIN member_roles ON member_roles.member_id = members.id
		WHERE members.guild_id = ? AND members.user_id IN ? AND member_roles.role_id IN ?`,
		guildID, recipients, referenced).Scan(&rows).Error
	if err != nil {
		log.Printf("message: 批量读取接收者角色失败（guild=%s）: %v", guildID, err)
		return nil
	}
	result := make(map[uuid.UUID]map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		if result[row.UserID] == nil {
			result[row.UserID] = make(map[uuid.UUID]bool, 2)
		}
		result[row.UserID][row.RoleID] = true
	}
	return result
}
