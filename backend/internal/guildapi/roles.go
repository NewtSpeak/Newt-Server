package guildapi

import (
	"errors"
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

// getRole GET /guilds/{gid}/roles/{roleID}：单角色详情（成员即可见；反应角色 bot 按名解析后精查）。
func (h *api) getRole(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
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
	c.JSON(http.StatusOK, role)
}

type roleRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=100"`
	Permissions int64  `json:"permissions"`
	// position：自定义角色 ≥1；@everyone 固定 0（update 时允许 0，create 仍由 canGrant 拒绝 0 级）。
	Position int `json:"position" binding:"gte=0"`
	// 展示属性（Owl-Desktop docs 04 §8）：可选，缺省时保留原值（更新）或用零值（创建）。
	Color       *string `json:"color" binding:"omitempty,max=16"`
	Hoist       *bool   `json:"hoist"`
	Mentionable *bool   `json:"mentionable"`
}

// validRoleColor 校验角色颜色为空串或 #RGB/#RRGGBB 十六进制。
func validRoleColor(color string) bool {
	if color == "" {
		return true
	}
	if len(color) != 4 && len(color) != 7 {
		return false
	}
	if color[0] != '#' {
		return false
	}
	for _, r := range color[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// applyRoleAppearance 将请求中的展示属性写入 role；颜色非法返回 false。
func applyRoleAppearance(c *gin.Context, role *model.Role, input roleRequest) bool {
	if input.Color != nil {
		color := strings.TrimSpace(*input.Color)
		if !validRoleColor(color) {
			fail(c, http.StatusBadRequest, "INVALID_COLOR", "颜色需为 #RGB 或 #RRGGBB 十六进制")
			return false
		}
		role.Color = color
	}
	if input.Hoist != nil {
		role.Hoist = *input.Hoist
	}
	if input.Mentionable != nil {
		role.Mentionable = *input.Mentionable
	}
	return true
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
	if !applyRoleAppearance(c, &role, input) {
		return
	}
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
	// 内置管理员角色（guildseed）：permissions 锁定为 ADMINISTRATOR、position 固定；
	// 名称与展示属性可改（仍走下方层级校验，实际仅所有者/系统管可达）。
	if role.Managed {
		if databaseMask(permissionMask(input.Permissions)) != role.Permissions {
			fail(c, http.StatusConflict, "MANAGED_ROLE", "内置管理员角色的权限已锁定，不可修改")
			return
		}
		if input.Position != role.Position {
			fail(c, http.StatusConflict, "MANAGED_ROLE", "内置管理员角色的层级固定，不可修改")
			return
		}
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
	if !applyRoleAppearance(c, &role, input) {
		return
	}
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

type rolePositionEntry struct {
	ID       uuid.UUID `json:"id" binding:"required"`
	Position int       `json:"position" binding:"gte=1"`
}

// reorderRoles PATCH /guilds/{gid}/roles（需 MANAGE_ROLES）：角色批量排序
// （Owl-Desktop docs 04 §8：拖拽调整层级）。body 为 [{id, position}] 数组；
// @everyone（position=0）不可参与排序；每个被移动的角色必须处于调用者可管理
// 层级内，且目标 position 不得超过自身最高角色（防自我提权）；事务整体生效，
// 逐角色发 GUILD_ROLE_UPDATE。
func (h *api) reorderRoles(c *gin.Context) {
	var input []rolePositionEntry
	if err := c.ShouldBindJSON(&input); err != nil || len(input) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "需要非空的 [{id, position}] 数组")
		return
	}
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	guildID := ctx.Guild.ID
	moved := make([]model.Role, 0, len(input))
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		for _, entry := range input {
			var role model.Role
			if err := tx.First(&role, "id = ? AND guild_id = ?", entry.ID, guildID).Error; err != nil {
				return err
			}
			if role.IsEveryone {
				return errEveryoneReorder
			}
			if role.Managed {
				return errManagedReorder
			}
			if !canManageRole(ctx, role) || !(ctx.SystemAdmin || ctx.Owner || entry.Position < ctx.HighestRole) {
				return errRoleHierarchy
			}
			if role.Position == entry.Position {
				continue
			}
			if err := tx.Model(&model.Role{}).Where("id = ?", role.ID).Update("position", entry.Position).Error; err != nil {
				return err
			}
			role.Position = entry.Position
			moved = append(moved, role)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errEveryoneReorder):
			fail(c, http.StatusBadRequest, "EVERYONE_POSITION_FIXED", "@everyone 的层级固定为 0")
		case errors.Is(err, errManagedReorder):
			fail(c, http.StatusConflict, "MANAGED_ROLE", "内置管理员角色的层级固定，不参与排序")
		case errors.Is(err, errRoleHierarchy):
			fail(c, http.StatusForbidden, "CANNOT_MANAGE_ROLE", "存在超出自身层级的角色调整")
		case errors.Is(err, gorm.ErrRecordNotFound):
			fail(c, http.StatusNotFound, "NOT_FOUND", "存在不属于本服务器的角色")
		default:
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存角色排序失败")
		}
		return
	}
	positions := map[string]any{}
	for _, role := range moved {
		positions[role.ID.String()] = role.Position
	}
	h.audit(ctx, user, "rbac.role_reorder", "guild", guildID.String(), map[string]any{"positions": positions})
	for _, role := range moved {
		h.publish(eventbus.Event{
			Type: eventbus.EventGuildRoleUpdate, GuildID: &guildID,
			Payload: eventbus.NewGuildRolePayload(role),
		})
	}
	c.Status(http.StatusNoContent)
}

var (
	errEveryoneReorder = errors.New("everyone role cannot be reordered")
	errManagedReorder  = errors.New("managed role cannot be reordered")
	errRoleHierarchy   = errors.New("role hierarchy violation")
)

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
	if role.Managed {
		fail(c, http.StatusConflict, "MANAGED_ROLE", "内置管理员角色不可删除")
		return
	}
	if !canManageRole(ctx, role) {
		fail(c, http.StatusForbidden, "CANNOT_MANAGE_ROLE", "不能管理该角色")
		return
	}
	// 删除前快照：供 auditundo 重建
	var memberIDs []uuid.UUID
	_ = h.deps.DB.Model(&model.MemberRole{}).Where("role_id = ?", role.ID).Pluck("member_id", &memberIDs)
	var overwrites []model.ChannelOverwrite
	_ = h.deps.DB.Where("type = ? AND target_id = ? AND channel_id IN (SELECT id FROM channels WHERE guild_id = ?)",
		model.OverwriteRole, role.ID, ctx.Guild.ID).Find(&overwrites).Error
	roleSnap := map[string]any{
		"name": role.Name, "permissions": role.Permissions, "position": role.Position,
		"color": role.Color, "style": role.Style, "hoist": role.Hoist, "mentionable": role.Mentionable,
		"member_ids": memberIDs,
	}
	if len(overwrites) > 0 {
		owList := make([]map[string]any, 0, len(overwrites))
		for _, ow := range overwrites {
			owList = append(owList, map[string]any{
				"id": ow.ID, "channel_id": ow.ChannelID, "allow": ow.Allow, "deny": ow.Deny, "type": ow.Type,
			})
		}
		roleSnap["overwrites"] = owList
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
		// 角色删除后同步清理频道慢速模式豁免，避免遗留无效角色 ID。
		var channels []model.Channel
		if err := tx.Where("guild_id = ?", ctx.Guild.ID).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			next := make(model.UUIDList, 0, len(channel.RateLimitExemptRoleIDs))
			removed := false
			for _, exemptID := range channel.RateLimitExemptRoleIDs {
				if exemptID == role.ID {
					removed = true
					continue
				}
				next = append(next, exemptID)
			}
			if removed {
				if err := tx.Model(&model.Channel{}).Where("id = ?", channel.ID).
					Update("rate_limit_exempt_role_ids", next).Error; err != nil {
					return err
				}
			}
		}
		return tx.Delete(&model.Role{}, "id = ?", role.ID).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除角色失败")
		return
	}
	h.audit(ctx, user, "rbac.role_delete", "role", role.ID.String(), map[string]any{
		"name": role.Name, "position": role.Position,
		"before": roleSnap,
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
	member, memberOK := h.findMemberByPathID(ctx.Guild.ID, memberID)
	var role model.Role
	if !memberOK ||
		h.deps.DB.First(&role, "id = ? AND guild_id = ?", roleID, ctx.Guild.ID).Error != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "成员或角色不存在")
		return
	}
	if role.IsEveryone {
		fail(c, http.StatusBadRequest, "EVERYONE_IMPLICIT", "@everyone 自动应用，不能手工绑定")
		return
	}
	// 内置管理员角色的成员操作：仅所有者或已持有 ADMINISTRATOR 者可为之
	//（不走 canManageRole 层级——ADMINISTRATOR 持有者最高层级恰等于该角色
	// position，严格大于永不成立；也防止所有者手建更高层级 MANAGE_ROLES
	// 角色后被绕过）。目标成员治理校验（canManageMember）仍保留：
	// 管理员之间不能互相摘除该角色，所有者不受限。
	if role.Managed && !(ctx.SystemAdmin || ctx.Owner || ctx.Has(rbac.Administrator)) {
		fail(c, http.StatusForbidden, "CANNOT_MANAGE_MEMBER", "仅所有者或管理员可以调整内置管理员角色的成员")
		return
	}
	if (!role.Managed && !canManageRole(ctx, role)) || !h.canManageMember(ctx, member) {
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
