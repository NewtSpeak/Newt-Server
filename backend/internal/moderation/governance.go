package moderation

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"gorm.io/gorm"
)

// kickMember DELETE /guilds/{gid}/members/{memberID}：踢出成员（需 KICK_MEMBERS + 层级）。
// 路径段为 @me 时转入主动退出流程（与踢出共用路由避免 gin 路由树冲突）。
func (h *api) kickMember(c *gin.Context) {
	if c.Param("memberID") == "@me" {
		h.leaveGuild(c)
		return
	}
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.KickMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有踢出成员的权限")
		return
	}
	memberID, err := uuid.Parse(c.Param("memberID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "成员不存在")
		return
	}
	var member model.Member
	if err := h.deps.DB.First(&member, "id = ? AND guild_id = ?", memberID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "成员不存在")
		return
	}
	if member.UserID == user.ID {
		fail(c, http.StatusBadRequest, "CANNOT_KICK_SELF", "不能踢出自己")
		return
	}
	if member.UserID == ctx.Guild.OwnerUserID {
		fail(c, http.StatusForbidden, "CANNOT_KICK_TARGET", "不能踢出服务器所有者")
		return
	}
	if !canGovern(ctx, h.highestRoleOf(member.ID)) {
		fail(c, http.StatusForbidden, "CANNOT_KICK_TARGET", "不能踢出角色层级不低于自己的成员")
		return
	}
	if err := h.removeMember(member, "kick"); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "踢出成员失败")
		return
	}
	h.audit(ctx, user, "moderation.kick", "member", member.ID.String(), map[string]any{"target_user_id": member.UserID})
	c.Status(http.StatusNoContent)
}

type banRequest struct {
	Reason string `json:"reason" binding:"omitempty,max=512"`
}

// banUser PUT /guilds/{gid}/bans/{userID}：封禁用户（需 BAN_MEMBERS + 层级）。
// 效果：移除成员 + 禁止凭邀请再加入 + 失活该用户本服全部 Restriction（docs 12 AO.3）。
func (h *api) banUser(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.BanMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有封禁成员的权限")
		return
	}
	targetUserID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	var input banRequest
	if err := c.ShouldBindJSON(&input); err != nil && err.Error() != "EOF" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if targetUserID == user.ID {
		fail(c, http.StatusBadRequest, "CANNOT_BAN_SELF", "不能封禁自己")
		return
	}
	if targetUserID == ctx.Guild.OwnerUserID {
		fail(c, http.StatusForbidden, "CANNOT_BAN_TARGET", "不能封禁服务器所有者")
		return
	}
	var target model.User
	if err := h.deps.DB.First(&target, "id = ?", targetUserID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	// 目标仍是成员时校验层级；非成员（预防性封禁）无层级可比，直接放行。
	var member model.Member
	isMember := h.deps.DB.First(&member, "guild_id = ? AND user_id = ?", ctx.Guild.ID, targetUserID).Error == nil
	if isMember && !canGovern(ctx, h.highestRoleOf(member.ID)) {
		fail(c, http.StatusForbidden, "CANNOT_BAN_TARGET", "不能封禁角色层级不低于自己的成员")
		return
	}
	ban := model.GuildBan{
		ID:        uuid.New(),
		GuildID:   ctx.Guild.ID,
		UserID:    targetUserID,
		Reason:    strings.TrimSpace(input.Reason),
		CreatedBy: user.ID,
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		// 幂等：重复 Ban 更新 reason / 操作者。
		return tx.Where(model.GuildBan{GuildID: ctx.Guild.ID, UserID: targetUserID}).
			Assign(map[string]any{"reason": ban.Reason, "created_by": user.ID}).
			FirstOrCreate(&ban).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "封禁失败")
		return
	}
	if isMember {
		if err := h.removeMember(member, "ban"); err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "移除成员失败")
			return
		}
	}
	// Ban 联动：失活该用户本服全部生效 Restriction（docs 12 AO.3），历史保留供审计。
	lifted, liftErr := restriction.LiftAllForUser(h.deps.DB, h.deps.Bus, ctx.Guild.ID, targetUserID, user.ID)
	h.audit(ctx, user, "moderation.ban", "user", targetUserID.String(), map[string]any{
		"reason":              ban.Reason,
		"was_member":          isMember,
		"restrictions_lifted": lifted,
		"lift_error":          errString(liftErr),
	})
	// GUILD_BAN_ADD：guild 广播（含预防性封禁非成员的场景——removeMember 只覆盖成员路径），
	// 管理端封禁列表与在线成员据此实时刷新（docs 08 §8-8）。
	h.publishBanEvent(eventbus.EventGuildBanAdd, ctx.Guild.ID, targetUserID, ban.Reason)
	c.JSON(http.StatusOK, ban)
}

// unbanUser DELETE /guilds/{gid}/bans/{userID}：解除封禁。
func (h *api) unbanUser(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.BanMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有解除封禁的权限")
		return
	}
	targetUserID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "封禁记录不存在")
		return
	}
	result := h.deps.DB.Delete(&model.GuildBan{}, "guild_id = ? AND user_id = ?", ctx.Guild.ID, targetUserID)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解除封禁失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "封禁记录不存在")
		return
	}
	h.audit(ctx, user, "moderation.unban", "user", targetUserID.String(), nil)
	// GUILD_BAN_REMOVE：guild 广播 + 定向被解封者（其在线时立即感知可重新加入）。
	h.publishBanEvent(eventbus.EventGuildBanRemove, ctx.Guild.ID, targetUserID, "")
	if h.deps.Bus != nil {
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type: eventbus.EventGuildBanRemove, GuildID: &guildID, UserIDs: []uuid.UUID{targetUserID},
			Payload: banEventPayload(guildID, targetUserID, ""),
		})
	}
	c.Status(http.StatusNoContent)
}

// banEventPayload GUILD_BAN_ADD / GUILD_BAN_REMOVE 载荷。
func banEventPayload(guildID, userID uuid.UUID, reason string) gin.H {
	payload := gin.H{"guild_id": guildID, "user_id": userID, "event_at": time.Now().UTC()}
	if reason != "" {
		payload["reason"] = reason
	}
	return payload
}

// publishBanEvent 封禁事件 guild 广播（bus 未注入时 no-op）。
func (h *api) publishBanEvent(eventType string, guildID, userID uuid.UUID, reason string) {
	if h.deps.Bus == nil {
		return
	}
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventType, GuildID: &guildID,
		Payload: banEventPayload(guildID, userID, reason),
	})
}

// listBans GET /guilds/{gid}/bans：封禁列表（需 BAN_MEMBERS）。
func (h *api) listBans(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.BanMembers) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有查看封禁列表的权限")
		return
	}
	var bans []model.GuildBan
	if err := h.deps.DB.Where("guild_id = ?", ctx.Guild.ID).Order("created_at DESC").Find(&bans).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取封禁列表失败")
		return
	}
	c.JSON(http.StatusOK, bans)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
