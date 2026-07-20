package moderation

// 成员自助与昵称管理：
//   - DELETE /guilds/{gid}/members/@me   主动退出服务器（所有者不可退，需先转让）
//   - PATCH  /guilds/{gid}/members/{memberID} 修改昵称（本人 CHANGE_NICKNAME /
//     他人 MANAGE_NICKNAMES + 层级；memberID 支持 @me 别名）

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// leaveGuild 主动退出服务器 → GUILD_MEMBER_REMOVE（reason=leave）。
// 所有者不可退出（Owl-Desktop docs 02 FR-10：需先转让所有权或删除服务器），
// 返回 409（与所有权状态冲突，而非权限不足，客户端据此弹「转让/删服」引导）。
func (h *api) leaveGuild(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if ctx.Guild.OwnerUserID == user.ID {
		fail(c, http.StatusConflict, "OWNER_CANNOT_LEAVE", "所有者不能退出服务器，请先转让所有权或删除服务器")
		return
	}
	if ctx.Member == nil {
		// 系统管理员可见但非成员（仅后台平面可能出现）。
		fail(c, http.StatusNotFound, "NOT_FOUND", "你不是该服务器成员")
		return
	}
	if err := h.removeMember(*ctx.Member, "leave"); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "退出服务器失败")
		return
	}
	h.audit(ctx, user, "moderation.member_leave", "member", ctx.Member.ID.String(), nil)
	c.Status(http.StatusNoContent)
}

type nicknameRequest struct {
	Nickname string `json:"nickname" binding:"omitempty,max=64"`
}

// updateNickname PATCH /guilds/{gid}/members/{memberID} → GUILD_MEMBER_UPDATE。
// 空字符串表示清除昵称（回退显示用户名）。
func (h *api) updateNickname(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	var input nicknameRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	nickname := strings.TrimSpace(input.Nickname)
	if utf8.RuneCountInString(nickname) > 32 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "昵称不能超过 32 个字符")
		return
	}

	var member model.Member
	raw := c.Param("memberID")
	if raw == "@me" {
		if ctx.Member == nil {
			fail(c, http.StatusNotFound, "NOT_FOUND", "你不是该服务器成员")
			return
		}
		member = *ctx.Member
	} else {
		memberID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusNotFound, "NOT_FOUND", "成员不存在")
			return
		}
		if err := h.deps.DB.First(&member, "id = ? AND guild_id = ?", memberID, ctx.Guild.ID).Error; err != nil {
			fail(c, http.StatusNotFound, "NOT_FOUND", "成员不存在")
			return
		}
	}

	self := member.UserID == user.ID
	if self {
		if !ctx.SystemAdmin && !ctx.Has(rbac.ChangeNickname) {
			fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有修改自己昵称的权限")
			return
		}
	} else {
		if !ctx.SystemAdmin && !ctx.Has(rbac.ManageNicknames) {
			fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有管理他人昵称的权限")
			return
		}
		if member.UserID == ctx.Guild.OwnerUserID {
			fail(c, http.StatusForbidden, "CANNOT_MANAGE_TARGET", "不能修改服务器所有者的昵称")
			return
		}
		if !canGovern(ctx, h.highestRoleOf(member.ID)) {
			fail(c, http.StatusForbidden, "CANNOT_MANAGE_TARGET", "不能管理角色层级不低于自己的成员")
			return
		}
	}

	before := member.Nickname
	member.Nickname = nickname
	if err := h.deps.DB.Model(&model.Member{}).Where("id = ?", member.ID).Update("nickname", nickname).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新昵称失败")
		return
	}
	h.audit(ctx, user, "moderation.nickname_update", "member", member.ID.String(), map[string]any{
		"target_user_id": member.UserID, "before": before, "after": nickname, "self": self,
	})
	h.publishMemberUpdate(member)
	c.JSON(http.StatusOK, member)
}

// publishMemberUpdate 昵称变更后广播 GUILD_MEMBER_UPDATE（含全量 role_ids，docs 14 §3.2）。
func (h *api) publishMemberUpdate(member model.Member) {
	if h.deps.Bus == nil {
		return
	}
	var roleIDs []uuid.UUID
	if err := h.deps.DB.Model(&model.MemberRole{}).Where("member_id = ?", member.ID).Pluck("role_id", &roleIDs).Error; err != nil {
		return
	}
	guildID := member.GuildID
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventbus.EventGuildMemberUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildMemberUpdatePayload(member, roleIDs),
	})
}
