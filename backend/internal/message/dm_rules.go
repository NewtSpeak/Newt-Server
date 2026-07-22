package message

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// enforcePrivateSend 私信发送前规则（Server-16 BP / BO）：
//  1. 任一方拉黑 → 403 不投递（区分本人拉黑 / 被对方拉黑，供客户端文案）
//  2. 接收方仍在消息请求箱时，发起方 ≤5 条/自然日
//  3. 发送前：对方 hidden 解除；本人若 message_request 则视同接受（回复即接受）
//
// 返回 false 时已写错误响应。
func (s *service) enforcePrivateSend(c *gin.Context, channel model.Channel, authorID uuid.UUID) bool {
	if !channel.Type.IsPrivate() {
		return true
	}
	var recipients []model.ChannelRecipient
	if err := s.db.Where("channel_id = ?", channel.ID).Find(&recipients).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取私信成员失败")
		return false
	}
	if len(recipients) == 0 {
		notFound(c)
		return false
	}

	// 拉黑：任一方 blocked 即禁止发送；错误码区分方向以便客户端展示不同文案
	for _, r := range recipients {
		if r.UserID == authorID {
			continue
		}
		if iBlockedThem(s.db, authorID, r.UserID) {
			// 我拉黑了对方
			fail(c, http.StatusForbidden, "BLOCKED_BY_SELF", "请解除拉黑之后再发送消息！")
			return false
		}
		if iBlockedThem(s.db, r.UserID, authorID) {
			// 对方拉黑了我（对外文案伪装为好友验证）
			fail(c, http.StatusForbidden, "BLOCKED_BY_PEER", "对方开启了好友验证，您暂时无法给对方发送消息！")
			return false
		}
	}

	// 请求箱频控：其他参与者 message_request=true 时作者为发起方
	peerPending := false
	for _, r := range recipients {
		if r.UserID != authorID && r.MessageRequest {
			peerPending = true
			break
		}
	}
	if peerPending {
		// 5 条/自然日（UTC）
		start := time.Now().UTC().Truncate(24 * time.Hour)
		var n int64
		s.db.Model(&model.Message{}).
			Where("channel_id = ? AND author_id = ? AND deleted_at IS NULL AND created_at >= ?",
				channel.ID, authorID, start).
			Count(&n)
		if n >= 5 {
			fail(c, http.StatusTooManyRequests, "MESSAGE_REQUEST_LIMIT", "消息请求未接受前每日最多发送 5 条")
			return false
		}
	}

	// 副作用：解除其他参与者 hidden；本人 message_request 清零（回复即接受 BO.2）。
	// 客户端靠 MESSAGE_CREATE 触发 private-channels.refresh 同步列表分组。
	for _, r := range recipients {
		updates := map[string]any{}
		if r.UserID == authorID {
			if r.MessageRequest {
				updates["message_request"] = false
			}
			if r.Hidden {
				updates["hidden"] = false
			}
		} else if r.Hidden {
			// 对方收到新消息时列表重新可见（BN.1 hidden 重开）
			updates["hidden"] = false
		}
		if len(updates) > 0 {
			_ = s.db.Model(&model.ChannelRecipient{}).
				Where("channel_id = ? AND user_id = ?", channel.ID, r.UserID).
				Updates(updates).Error
		}
	}
	return true
}

// relationshipBlocked 任一方拉黑了对方。
func relationshipBlocked(db *gorm.DB, a, b uuid.UUID) bool {
	return iBlockedThem(db, a, b) || iBlockedThem(db, b, a)
}

// iBlockedThem：a 是否拉黑了 b（仅 a→b 的 blocked 行）。
func iBlockedThem(db *gorm.DB, a, b uuid.UUID) bool {
	var n int64
	db.Model(&model.Relationship{}).
		Where("type = ? AND user_id = ? AND target_user_id = ?",
			model.RelationshipBlocked, a, b).
		Count(&n)
	return n > 0
}
