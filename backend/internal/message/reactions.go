package message

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 表情反应（docs 13 AV）：PUT/DELETE .../reactions/{emoji}/@me，幂等。

// putReaction PUT /channels/{id}/messages/{mid}/reactions/{emoji}/@me（需 ADD_REACTIONS）。
func (s *service) putReaction(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	_, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	if !rbac.Has(bits, rbac.AddReactions) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少添加反应权限")
		return
	}
	emoji := c.Param("emoji")
	if err := validateEmoji(emoji); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_EMOJI", err.Error())
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	reaction := model.MessageReaction{
		ID:        uuid.New(),
		MessageID: message.ID,
		UserID:    user.ID,
		Emoji:     emoji,
		CreatedAt: time.Now().UTC(),
	}
	// 唯一约束 + DO NOTHING 保证重复 PUT 幂等。
	result := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&reaction)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "添加反应失败")
		return
	}
	if result.RowsAffected > 0 {
		s.publishReactionEvent(eventbus.EventMessageReactionAdd, message, user.ID, emoji)
	}
	c.Status(http.StatusNoContent)
}

// deleteReaction DELETE /channels/{id}/messages/{mid}/reactions/{emoji}/@me（撤销自己的反应）。
func (s *service) deleteReaction(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	_, channel, _, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	emoji := c.Param("emoji")
	if err := validateEmoji(emoji); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_EMOJI", err.Error())
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	result := s.db.Where("message_id = ? AND user_id = ? AND emoji = ?", message.ID, user.ID, emoji).
		Delete(&model.MessageReaction{})
	if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "移除反应失败")
		return
	}
	if result.RowsAffected > 0 {
		s.publishReactionEvent(eventbus.EventMessageReactionRemove, message, user.ID, emoji)
	}
	c.Status(http.StatusNoContent)
}

func (s *service) publishReactionEvent(eventType string, message model.Message, userID uuid.UUID, emoji string) {
	s.bus.Publish(eventbus.Event{
		Type:      eventType,
		GuildID:   &message.GuildID,
		ChannelID: &message.ChannelID,
		Payload: gin.H{
			"message_id": strconv.FormatInt(message.ID, 10),
			"channel_id": message.ChannelID,
			"guild_id":   message.GuildID,
			"user_id":    userID,
			"emoji":      emoji,
		},
	})
}
