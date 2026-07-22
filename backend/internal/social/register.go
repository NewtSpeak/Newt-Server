// Package social 私信/好友/隐私/通知（Server-16）。
// 落地：隐私设置、好友关系、通知收件箱、1:1 DM get-or-create。
package social

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// Register 挂载到后台平面（/api/v1）——社交为用户自助，后台同样可测。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	mount(v1, deps)
	return nil
}

// RegisterClient 挂载到用户端 /gapi/v1。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	mount(root, deps)
	return nil
}

func mount(group *gin.RouterGroup, deps appdeps.Deps) {
	h := &api{deps: deps}
	me := group.Group("/users/@me", deps.Auth)

	me.GET("/privacy", h.getPrivacy)
	me.PATCH("/privacy", h.patchPrivacy)
	me.PUT("/guilds/:guildID/privacy", h.putGuildPrivacy)

	me.GET("/relationships", h.listRelationships)
	me.POST("/relationships", h.postRelationship)
	me.PUT("/relationships/:userID", h.putRelationship)
	me.PATCH("/relationships/:userID", h.patchRelationship)
	me.DELETE("/relationships/:userID", h.deleteRelationship)

	me.GET("/notifications", h.listNotifications)
	me.POST("/notifications/ack", h.ackNotifications)
	me.DELETE("/notifications/:id", h.deleteNotification)

	// 私信频道（Server-16 BR.2）
	me.GET("/channels", h.listPrivateChannels)
	me.POST("/channels", h.createPrivateChannel)

	// 私信频道操作（Server-16 BR.2）
	// 改名走 /users/@me/channels/:id，避免与 guildapi PATCH /channels/:id 冲突。
	auth := group.Group("", deps.Auth)
	auth.PATCH("/channels/:channelID/recipients/@me", h.patchRecipientMe)
	auth.DELETE("/channels/:channelID/recipients/@me", h.leaveGroupDM)
	auth.PUT("/channels/:channelID/recipients/:userID", h.inviteGroupDM)
	me.PATCH("/channels/:channelID", h.patchPrivateChannel)
}

// SnapshotForReady 组装 READY 扩展字段（Server-16 BS.1）。
func SnapshotForReady(db *gorm.DB, userID uuid.UUID) (
	relationships []relationshipView,
	privacy privacyView,
	privateChannels []privateChannelView,
	unread int64,
) {
	h := &api{deps: appdeps.Deps{DB: db}}
	// relationships
	var rows []model.Relationship
	_ = db.Where("user_id = ?", userID).Order("created_at DESC").Find(&rows).Error
	var incoming []model.Relationship
	_ = db.Where("target_user_id = ? AND type = ?", userID, model.RelationshipPendingOutgoing).
		Order("created_at DESC").Find(&incoming).Error
	relationships = make([]relationshipView, 0, len(rows)+len(incoming))
	for _, r := range rows {
		summary, err := h.loadUserSummary(r.TargetUserID)
		if err != nil {
			continue
		}
		relationships = append(relationships, relationshipView{
			ID: r.ID, Type: r.Type, Nickname: r.Nickname, User: summary, CreatedAt: r.CreatedAt,
		})
	}
	for _, r := range incoming {
		summary, err := h.loadUserSummary(r.UserID)
		if err != nil {
			continue
		}
		relationships = append(relationships, relationshipView{
			ID: r.ID, Type: model.RelationshipPendingIncoming, User: summary, CreatedAt: r.CreatedAt,
		})
	}
	p := h.loadOrDefaultPrivacy(userID)
	privacy = privacyToView(p, h.loadGuildOverrides(userID))
	privateChannels = h.listPrivateChannelViews(userID, false)
	ack := h.loadAck(userID)
	unread = h.unreadCount(userID, ack)
	return
}

// PrivacyJSON 供调试。
func PrivacyJSON(db *gorm.DB, userID uuid.UUID) json.RawMessage {
	_, privacy, _, _ := SnapshotForReady(db, userID)
	raw, _ := json.Marshal(privacy)
	return raw
}
