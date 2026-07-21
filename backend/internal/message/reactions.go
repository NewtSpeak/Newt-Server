package message

import (
	"net/http"
	"net/url"
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

// emojiParam 读取路径中的 emoji：Gin 通常已解码，若仍含 %XX 则再 PathUnescape 一次，
// 避免客户端 encodeURIComponent 后落库/查询键不一致导致刷新后对不上。
func emojiParam(c *gin.Context) string {
	raw := c.Param("emoji")
	if unescaped, err := url.PathUnescape(raw); err == nil && unescaped != "" {
		return unescaped
	}
	return raw
}

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
	emoji := emojiParam(c)
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
	emoji := emojiParam(c)
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

// listReactionUsers GET /channels/{id}/messages/{mid}/reactions/{emoji}
//（Owl-Desktop docs 05 FR-26：hover 反应胶囊查看反应者列表）。
// 需 READ_MESSAGE_HISTORY；?limit=1..100（默认 100），按反应时间升序。
func (s *service) listReactionUsers(c *gin.Context) {
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
	if !rbac.Has(bits, rbac.ReadMessageHistory) {
		notFound(c)
		return
	}
	emoji := emojiParam(c)
	if err := validateEmoji(emoji); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_EMOJI", err.Error())
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			fail(c, http.StatusBadRequest, "INVALID_LIMIT", "limit 需为 1-100 的整数")
			return
		}
		if parsed < limit {
			limit = parsed
		}
	}
	type reactorRow struct {
		UserID      uuid.UUID `json:"user_id"`
		Username    string    `json:"username"`
		DisplayName string    `json:"display_name"`
		AvatarURL   string    `json:"avatar_url"`
		CreatedAt   time.Time `json:"reacted_at"`
	}
	var rows []reactorRow
	err = s.db.Raw(`SELECT r.user_id, u.username, u.display_name, u.avatar_url, r.created_at
		FROM message_reactions r JOIN users u ON u.id = r.user_id
		WHERE r.message_id = ? AND r.emoji = ?
		ORDER BY r.created_at ASC LIMIT ?`, message.ID, emoji, limit).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取反应者失败")
		return
	}
	if rows == nil {
		rows = []reactorRow{}
	}
	c.JSON(http.StatusOK, gin.H{"emoji": emoji, "users": rows})
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
