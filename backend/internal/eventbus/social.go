package eventbus

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// 社交层 Gateway 事件（Server-16 BS）。
const (
	EventRelationshipAdd    = "RELATIONSHIP_ADD"
	EventRelationshipUpdate = "RELATIONSHIP_UPDATE"
	EventRelationshipRemove = "RELATIONSHIP_REMOVE"
	EventNotificationCreate = "NOTIFICATION_CREATE"
	EventNotificationDelete = "NOTIFICATION_DELETE"
)

// RelationshipPayload RELATIONSHIP_* 载荷。
type RelationshipPayload struct {
	ID           uuid.UUID       `json:"id"`
	UserID       uuid.UUID       `json:"user_id"`
	TargetUserID uuid.UUID       `json:"target_user_id"`
	Type         string          `json:"type"`
	Nickname     string          `json:"nickname,omitempty"`
	User         json.RawMessage `json:"user,omitempty"` // 对方摘要，可选
	EventAt      time.Time       `json:"event_at"`
}

// NotificationPayload NOTIFICATION_* 载荷。
type NotificationPayload struct {
	ID      uuid.UUID       `json:"id"`
	UserID  uuid.UUID       `json:"user_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	EventAt time.Time       `json:"event_at"`
}

// PrivacyUpdatePayload USER_SETTINGS_UPDATE 的 privacy 扩展：客户端可单独处理。
// 亦可用 settings 全量文档；此处供 social 模块定向推送精简隐私。
type PrivacyUpdatePayload struct {
	Privacy json.RawMessage `json:"privacy"`
	EventAt time.Time       `json:"event_at"`
}

func NewRelationshipPayload(id, userID, targetUserID uuid.UUID, relType, nickname string, userJSON json.RawMessage) RelationshipPayload {
	return RelationshipPayload{
		ID: id, UserID: userID, TargetUserID: targetUserID,
		Type: relType, Nickname: nickname, User: userJSON, EventAt: eventNow(),
	}
}

func NewNotificationPayload(id, userID uuid.UUID, notifType string, payload json.RawMessage) NotificationPayload {
	return NotificationPayload{
		ID: id, UserID: userID, Type: notifType, Payload: payload, EventAt: eventNow(),
	}
}
