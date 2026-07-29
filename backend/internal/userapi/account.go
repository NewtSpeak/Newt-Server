package userapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/gorm"
)

type deleteAccountRequest struct {
	// Password 密码二次确认（docs 16 FR-04 危险区：删除账号需输入密码）。
	Password string `json:"password" binding:"required"`
}

// deleteAccount DELETE /users/@me：注销账号（Newt-Desktop docs 16 FR-04）。
// 语义对标 Discord「删除账户」：
//   - 密码确认；仍拥有服务器时返回 409 OWNS_GUILDS（须先转让或删除）；
//   - 历史消息保留但作者匿名化（用户行改写为「已注销用户」，不物理删行，
//     避免消息/审计外键悬空）；
//   - 退出全部服务器（连带角色绑定）、清理语音状态与设置、吊销全部会话。
//
// 系统管理员账号不允许自助注销（防止最后一名管理员消失，走平台侧流程）。
func (h *api) deleteAccount(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input deleteAccountRequest
	if !bind(c, &input) {
		return
	}
	if !security.VerifyPassword(user.PasswordHash, input.Password) {
		fail(c, http.StatusForbidden, "INVALID_PASSWORD", "密码错误")
		return
	}
	if user.SystemAdmin {
		fail(c, http.StatusForbidden, "SYSTEM_ADMIN_UNDELETABLE", "系统管理员账号不能自助注销，请先移除管理员身份")
		return
	}
	var ownedGuilds int64
	if err := h.deps.DB.Model(&model.Guild{}).Where("owner_user_id = ?", user.ID).Count(&ownedGuilds).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询失败")
		return
	}
	if ownedGuilds > 0 {
		fail(c, http.StatusConflict, "OWNS_GUILDS", "你仍拥有服务器，请先转让所有权或删除服务器")
		return
	}
	now := time.Now().UTC()
	suffix := uuid.NewString()[:8]
	randomHash, err := security.HashPassword(uuid.NewString())
	if err != nil {
		fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "注销失败")
		return
	}
	// 删除前收集成员关系，事后逐服广播 GUILD_MEMBER_REMOVE（实时同步专项：
	// 各服在线成员列表即时移除此人，不等重连）。
	var memberships []model.Member
	if err := h.deps.DB.Where("user_id = ?", user.ID).Find(&memberships).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询失败")
		return
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		// 退出全部服务器：先清角色绑定再删成员行。
		if err := tx.Where("member_id IN (SELECT id FROM members WHERE user_id = ?)", user.ID).
			Delete(&model.MemberRole{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.Member{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.VoiceState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.UserSettings{}).Error; err != nil {
			return err
		}
		// 吊销两个受众的全部会话。
		if err := tx.Model(&model.RefreshToken{}).
			Where("user_id = ? AND revoked_at IS NULL", user.ID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		// 匿名化用户行（保留 ID 供历史消息/审计引用）并平台禁用。
		return tx.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"username":      "deleted-user-" + suffix,
			"email":         fmt.Sprintf("%s@deleted.invalid", user.ID),
			"display_name":  "已注销用户",
			"bio":           "",
			"avatar_url":    "",
			"banner_url":    "",
			"accent_color":  "",
			"password_hash": randomHash,
			"disabled_at":   now,
		}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "注销账号失败")
		return
	}
	// 强制下线：注销后立即断开本人全部 Gateway 连接（refresh 已吊销，WS 也不保留）。
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.InternalSessionRevoke,
			UserIDs: []uuid.UUID{user.ID},
		})
	}
	// 事件下发：逐服广播成员移除 + 匿名化后的资料投影（成员行已删，须按删除前
	// 收集的 guild 列表广播——共享服在线成员立即看到移除与「已注销用户」）。
	if h.deps.Bus != nil {
		var fresh model.User
		hasFresh := h.deps.DB.First(&fresh, "id = ?", user.ID).Error == nil
		for _, membership := range memberships {
			guildID := membership.GuildID
			h.deps.Bus.Publish(eventbus.Event{
				Type: eventbus.EventGuildMemberRemove, GuildID: &guildID,
				Payload: eventbus.NewGuildMemberRemovePayload(membership, "leave"),
			})
			if hasFresh {
				h.deps.Bus.Publish(eventbus.Event{
					Type: eventbus.EventUserUpdate, GuildID: &guildID,
					Payload: eventbus.NewUserUpdatePayload(fresh),
				})
			}
		}
	}
	c.Status(http.StatusNoContent)
}
