package social

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

type api struct {
	deps appdeps.Deps
}

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

func (h *api) publishToUser(userID uuid.UUID, eventType string, payload any) {
	if h.deps.Bus == nil {
		return
	}
	h.deps.Bus.Publish(eventbus.Event{
		Type:    eventType,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

// userSummary 关系/通知里的对方公开摘要。
type userSummary struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
}

func (h *api) loadUserSummary(userID uuid.UUID) (userSummary, error) {
	var user model.User
	if err := h.deps.DB.Select("id", "username", "display_name", "avatar_url").
		First(&user, "id = ?", userID).Error; err != nil {
		return userSummary{}, err
	}
	return userSummary{
		ID: user.ID, Username: user.Username,
		DisplayName: user.DisplayName, AvatarURL: user.AvatarURL,
	}, nil
}

func userJSON(summary userSummary) json.RawMessage {
	raw, _ := json.Marshal(summary)
	return raw
}

func (h *api) findUserByUsername(username string) (model.User, error) {
	var user model.User
	err := h.deps.DB.Where("username = ? AND disabled_at IS NULL", username).First(&user).Error
	return user, err
}

// isBlocked 任一方屏蔽了对方。
func (h *api) isBlocked(a, b uuid.UUID) bool {
	var n int64
	h.deps.DB.Model(&model.Relationship{}).
		Where("type = ? AND ((user_id = ? AND target_user_id = ?) OR (user_id = ? AND target_user_id = ?))",
			model.RelationshipBlocked, a, b, b, a).
		Count(&n)
	return n > 0
}

func (h *api) hasFriend(a, b uuid.UUID) bool {
	var n int64
	h.deps.DB.Model(&model.Relationship{}).
		Where("type = ? AND user_id = ? AND target_user_id = ?",
			model.RelationshipFriend, a, b).
		Count(&n)
	return n > 0
}

func (h *api) mutualGuildCount(a, b uuid.UUID) int64 {
	var n int64
	// 共同服务器：双方都在 members 中的 guild 数
	h.deps.DB.Raw(`
		SELECT COUNT(*) FROM members ma
		INNER JOIN members mb ON ma.guild_id = mb.guild_id
		WHERE ma.user_id = ? AND mb.user_id = ?
	`, a, b).Scan(&n)
	return n
}

func (h *api) mutualFriendCount(a, b uuid.UUID) int64 {
	var n int64
	h.deps.DB.Raw(`
		SELECT COUNT(*) FROM relationships ra
		INNER JOIN relationships rb ON ra.target_user_id = rb.target_user_id
		WHERE ra.user_id = ? AND ra.type = ?
		  AND rb.user_id = ? AND rb.type = ?
	`, a, model.RelationshipFriend, b, model.RelationshipFriend).Scan(&n)
	return n
}

func (h *api) countType(userID uuid.UUID, typ string) int64 {
	var n int64
	h.deps.DB.Model(&model.Relationship{}).
		Where("user_id = ? AND type = ?", userID, typ).Count(&n)
	return n
}

func (h *api) countPendingTotal(userID uuid.UUID) int64 {
	// 发出 + 收到
	var outgoing, incoming int64
	h.deps.DB.Model(&model.Relationship{}).
		Where("user_id = ? AND type = ?", userID, model.RelationshipPendingOutgoing).Count(&outgoing)
	h.deps.DB.Model(&model.Relationship{}).
		Where("target_user_id = ? AND type = ?", userID, model.RelationshipPendingOutgoing).Count(&incoming)
	return outgoing + incoming
}

// loadOrDefaultPrivacy 读取隐私；无行返回安全默认。
func (h *api) loadOrDefaultPrivacy(userID uuid.UUID) model.PrivacySettings {
	var p model.PrivacySettings
	if err := h.deps.DB.First(&p, "user_id = ?", userID).Error; err != nil {
		return model.PrivacySettings{
			UserID:                    userID,
			FriendRequestFrom:         model.FriendRequestMutualGuilds,
			DmFrom:                    model.DmFromFriends,
			MessageRequestFilter:      true,
			ShowMutualGuilds:          true,
			PublicProfileToNonFriends: true,
		}
	}
	return p
}

func (h *api) guildAllowDM(userID, guildID uuid.UUID) bool {
	var row model.GuildMemberPrivacy
	if err := h.deps.DB.First(&row, "user_id = ? AND guild_id = ?", userID, guildID).Error; err != nil {
		return true // 默认允许
	}
	return row.AllowDM
}

// shareAllowedGuild 共同服中是否存在 allow_dm 未关闭的服。
func (h *api) shareAllowedGuild(sender, receiver uuid.UUID) bool {
	var guildIDs []uuid.UUID
	_ = h.deps.DB.Raw(`
		SELECT ma.guild_id FROM members ma
		INNER JOIN members mb ON ma.guild_id = mb.guild_id
		WHERE ma.user_id = ? AND mb.user_id = ?
	`, sender, receiver).Scan(&guildIDs).Error
	if len(guildIDs) == 0 {
		return false
	}
	for _, gid := range guildIDs {
		if h.guildAllowDM(receiver, gid) {
			return true
		}
	}
	return false
}

// ensurePrivacyRow 确保隐私行存在（PATCH 时 upsert）。
func (h *api) ensurePrivacyRow(userID uuid.UUID) model.PrivacySettings {
	p := h.loadOrDefaultPrivacy(userID)
	if p.UpdatedAt.IsZero() {
		// 新建
		_ = h.deps.DB.Clauses().Create(&p).Error
		// 可能冲突，再读
		_ = h.deps.DB.First(&p, "user_id = ?", userID).Error
		if p.UserID == uuid.Nil {
			p.UserID = userID
			p.FriendRequestFrom = model.FriendRequestMutualGuilds
			p.DmFrom = model.DmFromFriends
			p.MessageRequestFilter = true
			p.ShowMutualGuilds = true
			p.PublicProfileToNonFriends = true
			_ = h.deps.DB.Save(&p).Error
		}
	}
	return p
}

// createNotification 写入收件箱并推送 NOTIFICATION_CREATE。
func (h *api) createNotification(userID uuid.UUID, typ string, payload map[string]any) {
	raw, _ := json.Marshal(payload)
	n := model.Notification{
		ID: uuid.New(), UserID: userID, Type: typ, Payload: string(raw),
	}
	if err := h.deps.DB.Create(&n).Error; err != nil {
		return
	}
	h.publishToUser(userID, eventbus.EventNotificationCreate, eventbus.NewNotificationPayload(
		n.ID, userID, typ, raw,
	))
}

// db helper for tests
func (h *api) db() *gorm.DB { return h.deps.DB }
