package message

import (
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// PostPrivateSystem 向私信频道写入系统灰条并定向广播（Server-16 BN.5）。
// authorID 为操作者（邀请人/离开者/改名者），content 为客户端可读文案。
func PostPrivateSystem(
	db *gorm.DB,
	bus *eventbus.Bus,
	channelID, authorID uuid.UUID,
	typ model.MessageType,
	content string,
) {
	if db == nil || content == "" {
		return
	}
	now := time.Now().UTC()
	msg := model.Message{
		ID:        sharedIDs.Next(),
		GuildID:   uuid.Nil,
		ChannelID: channelID,
		AuthorID:  authorID,
		Type:      typ,
		Content:   content,
		CreatedAt: now,
	}
	if err := db.Create(&msg).Error; err != nil {
		return
	}
	authorUsername := ""
	var user model.User
	if err := db.Select("username", "display_name").First(&user, "id = ?", authorID).Error; err == nil {
		authorUsername = user.Username
	}
	// 与 messageView JSON 形态对齐（id 为字符串）
	view := map[string]any{
		"id":               strconv.FormatInt(msg.ID, 10),
		"guild_id":         msg.GuildID,
		"channel_id":       msg.ChannelID,
		"author_id":        msg.AuthorID,
		"type":             string(msg.Type),
		"content":          msg.Content,
		"created_at":       msg.CreatedAt,
		"attachments":      []any{},
		"reactions":        []any{},
		"author_username":  authorUsername,
		"mentions":         []any{},
		"mention_roles":    []any{},
		"mention_everyone": false,
	}

	var userIDs []uuid.UUID
	_ = db.Model(&model.ChannelRecipient{}).
		Where("channel_id = ?", channelID).
		Pluck("user_id", &userIDs).Error
	if bus == nil || len(userIDs) == 0 {
		return
	}
	bus.Publish(eventbus.Event{
		Type:      eventbus.EventMessageCreate,
		ChannelID: &channelID,
		UserIDs:   userIDs,
		Payload:   view,
	})
}
