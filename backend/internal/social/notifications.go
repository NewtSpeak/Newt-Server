package social

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm/clause"
)

type notificationView struct {
	ID        uuid.UUID       `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
	Read      bool            `json:"read"`
}

// listNotifications GET /users/@me/notifications
func (h *api) listNotifications(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	query := h.deps.DB.Where("user_id = ?", user.ID).Order("created_at DESC, id DESC")
	if before := c.Query("before"); before != "" {
		if id, err := uuid.Parse(before); err == nil {
			var cursor model.Notification
			if h.deps.DB.First(&cursor, "id = ? AND user_id = ?", id, user.ID).Error == nil {
				query = query.Where("(created_at, id) < (?, ?)", cursor.CreatedAt, cursor.ID)
			}
		}
	}
	var rows []model.Notification
	_ = query.Limit(limit + 1).Find(&rows).Error
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	ack := h.loadAck(user.ID)
	items := make([]notificationView, 0, len(rows))
	for _, r := range rows {
		read := r.ReadAt != nil
		if !read && ack != nil && !r.CreatedAt.After(ack.LastReadAt) {
			// 水位线：created_at <= last_read_at 视为已读
			read = true
		}
		items = append(items, notificationView{
			ID: r.ID, Type: r.Type, Payload: json.RawMessage(r.Payload),
			CreatedAt: r.CreatedAt, Read: read,
		})
	}
	resp := gin.H{"items": items, "has_more": hasMore}
	if hasMore && len(rows) > 0 {
		resp["next_cursor"] = rows[len(rows)-1].ID.String()
	}
	resp["unread_count"] = h.unreadCount(user.ID, ack)
	c.JSON(http.StatusOK, resp)
}

func (h *api) loadAck(userID uuid.UUID) *model.NotificationAck {
	var ack model.NotificationAck
	if h.deps.DB.First(&ack, "user_id = ?", userID).Error != nil {
		return nil
	}
	return &ack
}

func (h *api) unreadCount(userID uuid.UUID, ack *model.NotificationAck) int64 {
	query := h.deps.DB.Model(&model.Notification{}).Where("user_id = ? AND read_at IS NULL", userID)
	if ack != nil {
		query = query.Where("created_at > ?", ack.LastReadAt)
	}
	var n int64
	query.Count(&n)
	return n
}

type ackRequest struct {
	LastReadID uuid.UUID `json:"last_read_id" binding:"required"`
}

// ackNotifications POST /users/@me/notifications/ack
func (h *api) ackNotifications(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input ackRequest
	if !bind(c, &input) {
		return
	}
	var n model.Notification
	if h.deps.DB.First(&n, "id = ? AND user_id = ?", input.LastReadID, user.ID).Error != nil {
		// 允许 ack 不存在的 id：用当前最新
		var latest model.Notification
		if h.deps.DB.Where("user_id = ?", user.ID).Order("created_at DESC").First(&latest).Error != nil {
			c.JSON(http.StatusOK, gin.H{"unread_count": 0})
			return
		}
		n = latest
	}
	ack := model.NotificationAck{
		UserID: user.ID, LastReadID: n.ID, LastReadAt: n.CreatedAt, UpdatedAt: time.Now().UTC(),
	}
	_ = h.deps.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_read_id", "last_read_at", "updated_at"}),
	}).Create(&ack).Error
	// 批量标记 read_at
	now := time.Now().UTC()
	_ = h.deps.DB.Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL AND created_at <= ?", user.ID, n.CreatedAt).
		Update("read_at", now).Error
	c.JSON(http.StatusOK, gin.H{"unread_count": h.unreadCount(user.ID, &ack)})
}

// deleteNotification DELETE /users/@me/notifications/:id
func (h *api) deleteNotification(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "通知不存在")
		return
	}
	result := h.deps.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&model.Notification{})
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "通知不存在")
		return
	}
	h.publishToUser(user.ID, eventbus.EventNotificationDelete, eventbus.NewNotificationPayload(
		id, user.ID, "", nil,
	))
	c.Status(http.StatusNoContent)
}

// UnreadCount 供 READY 使用
func UnreadCount(db interface{ /* gorm */ }, userID uuid.UUID) int64 {
	return 0
}
