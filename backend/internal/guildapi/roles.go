package guildapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// listRoles GET /guilds/{gid}/roles：全量角色（成员即可见）。
func (h *api) listRoles(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	var roles []model.Role
	if err := h.deps.DB.Where("guild_id = ?", ctx.Guild.ID).Order("position ASC, id ASC").Find(&roles).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取角色失败")
		return
	}
	c.JSON(http.StatusOK, roles)
}

type roleRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=100"`
	Permissions int64  `json:"permissions"`
	Position    int    `json:"position" binding:"gte=1"`
}

// createRole POST /guilds/{gid}/roles（需 MANAGE_ROLES + 防提权：不能授予超过
// 自身权限或层级的角色）。
func (h *api) createRole(c *gin.Context) {
	var input roleRequest
	if !bind(c, &input) {
		return
	}
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	requested := permissionMask(input.Permissions)
	if !canGrant(ctx, requested, input.Position) {
		fail(c, http.StatusForbidden, "CANNOT_GRANT_ROLE", "不能授予超过自身权限或层级的角色")
		return
	}
	role := model.Role{ID: uuid.New(), GuildID: ctx.Guild.ID, Name: strings.TrimSpace(input.Name), Permissions: databaseMask(requested), Position: input.Position}
	if err := h.deps.DB.Create(&role).Error; err != nil {
		fail(c, http.StatusConflict, "ROLE_EXISTS", "角色名称已存在或数据无效")
		return
	}
	h.audit(ctx, user, "rbac.role_create", "role", role.ID.String(), map[string]any{
		"name": role.Name, "permissions": role.Permissions, "position": role.Position,
	})
	guildID := ctx.Guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildRoleCreate, GuildID: &guildID,
		Payload: eventbus.NewGuildRolePayload(role),
	})
	c.JSON(http.StatusCreated, role)
}

// updateRole PATCH /guilds/{gid}/roles/{roleID}（需 MANAGE_ROLES + 层级校验）。
func (h *api) updateRole(c *gin.Context) {
	var input roleRequest
	if !bind(c, &input) {
		return
	}
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	var role model.Role
	if err := h.deps.DB.First(&role, "id = ? AND guild_id = ?", c.Param("roleID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return
	}
	if role.IsEveryone && input.Position != 0 {
		fail(c, http.StatusBadRequest, "EVERYONE_POSITION_FIXED", "@everyone 的层级固定为 0")
		return
	}
	requested := permissionMask(input.Permissions)
	if !canManageRole(ctx, role) || !canGrant(ctx, requested, input.Position) {
		fail(c, http.StatusForbidden, "CANNOT_MANAGE_ROLE", "不能管理该角色")
		return
	}
	// 记录变更前快照，便于审计对比。
	before := map[string]any{"name": role.Name, "permissions": role.Permissions, "position": role.Position}
	// 扩展功能位（52–54）超出 JS Number 2^53 精度，控制台数值掩码不感知，
	// 常规保存保留原值，改由 customization 的 feature-bits 端点布尔量读写。
	const featureBits = rbac.ManageBots | rbac.ManageBadges | rbac.ManageCustomization
	merged := (requested &^ featureBits) | (permissionMask(role.Permissions) & featureBits)
	role.Name, role.Permissions = strings.TrimSpace(input.Name), databaseMask(merged)
	if !role.IsEveryone {
		role.Position = input.Position
	}
	if err := h.deps.DB.Save(&role).Error; err != nil {
		fail(c, http.StatusConflict, "ROLE_UPDATE_FAILED", "角色更新失败")
		return
	}
	h.audit(ctx, user, "rbac.role_update", "role", role.ID.String(), map[string]any{
		"before": before,
		"after":  map[string]any{"name": role.Name, "permissions": role.Permissions, "position": role.Position},
	})
	guildID := ctx.Guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildRoleUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildRolePayload(role),
	})
	c.JSON(http.StatusOK, role)
}

// deleteRole DELETE /guilds/{gid}/roles/{roleID}（需 MANAGE_ROLES + 层级；
// @everyone 不可删）。连带清理成员绑定与该角色的频道权限覆盖，发 GUILD_ROLE_DELETE。
func (h *api) deleteRole(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	roleID, err := uuid.Parse(c.Param("roleID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return
	}
	var role model.Role
	if err := h.deps.DB.First(&role, "id = ? AND guild_id = ?", roleID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return
	}
	if role.IsEveryone {
		fail(c, http.StatusBadRequest, "EVERYONE_UNDELETABLE", "@everyone 角色不可删除")
		return
	}
	if !canManageRole(ctx, role) {
		fail(c, http.StatusForbidden, "CANNOT_MANAGE_ROLE", "不能管理该角色")
		return
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", role.ID).Delete(&model.MemberRole{}).Error; err != nil {
			return err
		}
		// 该角色作为覆盖目标的记录一并清理（本服频道范围）。
		if err := tx.Where("type = ? AND target_id = ? AND channel_id IN (SELECT id FROM channels WHERE guild_id = ?)",
			model.OverwriteRole, role.ID, ctx.Guild.ID).Delete(&model.ChannelOverwrite{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Role{}, "id = ?", role.ID).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除角色失败")
		return
	}
	h.audit(ctx, user, "rbac.role_delete", "role", role.ID.String(), map[string]any{
		"name": role.Name, "position": role.Position,
	})
	guildID := ctx.Guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildRoleDelete, GuildID: &guildID,
		Payload: eventbus.NewGuildRoleDeletePayload(guildID, role.ID),
	})
	c.Status(http.StatusNoContent)
}

func (h *api) assignRole(c *gin.Context) { h.changeMemberRole(c, true) }
func (h *api) removeRole(c *gin.Context) { h.changeMemberRole(c, false) }

// changeMemberRole 成员角色绑定/解绑（需 MANAGE_ROLES + 双重层级校验：
// 可管理该角色 + 可治理目标成员，防提权）。
func (h *api) changeMemberRole(c *gin.Context, assign bool) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	memberID, err1 := uuid.Parse(c.Param("memberID"))
	roleID, err2 := uuid.Parse(c.Param("roleID"))
	if err1 != nil || err2 != nil {
		fail(c, http.StatusBadRequest, "INVALID_ID", "成员或角色 ID 无效")
		return
	}
	var member model.Member
	var role model.Role
	if h.deps.DB.First(&member, "id = ? AND guild_id = ?", memberID, ctx.Guild.ID).Error != nil ||
		h.deps.DB.First(&role, "id = ? AND guild_id = ?", roleID, ctx.Guild.ID).Error != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "成员或角色不存在")
		return
	}
	if role.IsEveryone {
		fail(c, http.StatusBadRequest, "EVERYONE_IMPLICIT", "@everyone 自动应用，不能手工绑定")
		return
	}
	if !canManageRole(ctx, role) || !h.canManageMember(ctx, member) {
		fail(c, http.StatusForbidden, "CANNOT_MANAGE_MEMBER", "角色层级不足")
		return
	}
	binding := model.MemberRole{MemberID: member.ID, RoleID: role.ID}
	auditDetail := map[string]any{"target_user_id": member.UserID, "role_id": role.ID, "role_name": role.Name}
	if assign {
		if err := h.deps.DB.FirstOrCreate(&binding, binding).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "绑定角色失败")
			return
		}
		h.audit(ctx, user, "rbac.member_role_assign", "member", member.ID.String(), auditDetail)
		h.publishMemberUpdate(ctx.Guild.ID, member)
		c.JSON(http.StatusOK, binding)
		return
	}
	h.deps.DB.Delete(&binding)
	h.audit(ctx, user, "rbac.member_role_remove", "member", member.ID.String(), auditDetail)
	h.publishMemberUpdate(ctx.Guild.ID, member)
	c.Status(http.StatusNoContent)
}

// publishMemberUpdate 角色绑定/解绑后广播 GUILD_MEMBER_UPDATE（含全量 role_ids，docs 14 §3.2）。
func (h *api) publishMemberUpdate(guildID uuid.UUID, member model.Member) {
	if h.deps.Bus == nil {
		return
	}
	var roleIDs []uuid.UUID
	if err := h.deps.DB.Model(&model.MemberRole{}).Where("member_id = ?", member.ID).Pluck("role_id", &roleIDs).Error; err != nil {
		return
	}
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildMemberUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildMemberUpdatePayload(member, roleIDs),
	})
}
