package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

type createGuildRequest struct {
	Name string `json:"name" binding:"required,min=2,max=100"`
}

// createGuild godoc
// @Summary 创建服务器
// @Tags RBAC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param body body createGuildRequest true "服务器资料"
// @Success 201 {object} model.Guild
// @Router /guilds [post]
func (a *API) createGuild(c *gin.Context) {
	var input createGuildRequest
	if !bind(c, &input) {
		return
	}
	user := currentUser(c)
	guild := model.Guild{ID: uuid.New(), Name: strings.TrimSpace(input.Name), OwnerUserID: user.ID}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&guild).Error; err != nil {
			return err
		}
		member := model.Member{ID: uuid.New(), GuildID: guild.ID, UserID: user.ID}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		everyone := model.Role{ID: uuid.New(), GuildID: guild.ID, Name: "@everyone", Permissions: databaseMask(rbac.DefaultEveryone), Position: 0, IsEveryone: true}
		return tx.Create(&everyone).Error
	})
	if err != nil {
		fail(c, 500, "DATABASE_ERROR", "创建服务器失败")
		return
	}
	c.JSON(http.StatusCreated, guild)
}

func (a *API) listRoles(c *gin.Context) {
	guild, ok := a.guildForUser(c)
	if !ok {
		return
	}
	var roles []model.Role
	if err := a.db.Where("guild_id = ?", guild.ID).Order("position ASC, id ASC").Find(&roles).Error; err != nil {
		fail(c, 500, "DATABASE_ERROR", "读取角色失败")
		return
	}
	c.JSON(200, roles)
}

type roleRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=100"`
	Permissions int64  `json:"permissions"`
	Position    int    `json:"position" binding:"gte=1"`
}

func (a *API) createRole(c *gin.Context) {
	var input roleRequest
	if !bind(c, &input) {
		return
	}
	guild, context, ok := a.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	requested := permissionMask(input.Permissions)
	if !a.canGrant(context, requested, input.Position) {
		fail(c, 403, "CANNOT_GRANT_ROLE", "不能授予超过自身权限或层级的角色")
		return
	}
	role := model.Role{ID: uuid.New(), GuildID: guild.ID, Name: strings.TrimSpace(input.Name), Permissions: databaseMask(requested), Position: input.Position}
	if err := a.db.Create(&role).Error; err != nil {
		fail(c, 409, "ROLE_EXISTS", "角色名称已存在或数据无效")
		return
	}
	c.JSON(201, role)
}

func (a *API) updateRole(c *gin.Context) {
	var input roleRequest
	if !bind(c, &input) {
		return
	}
	guild, context, ok := a.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	var role model.Role
	if err := a.db.First(&role, "id = ? AND guild_id = ?", c.Param("roleID"), guild.ID).Error; err != nil {
		fail(c, 404, "NOT_FOUND", "角色不存在")
		return
	}
	if role.IsEveryone && input.Position != 0 {
		fail(c, 400, "EVERYONE_POSITION_FIXED", "@everyone 的层级固定为 0")
		return
	}
	requested := permissionMask(input.Permissions)
	if !a.canManageRole(context, role) || !a.canGrant(context, requested, input.Position) {
		fail(c, 403, "CANNOT_MANAGE_ROLE", "不能管理该角色")
		return
	}
	role.Name, role.Permissions = strings.TrimSpace(input.Name), databaseMask(requested)
	if !role.IsEveryone {
		role.Position = input.Position
	}
	if err := a.db.Save(&role).Error; err != nil {
		fail(c, 409, "ROLE_UPDATE_FAILED", "角色更新失败")
		return
	}
	c.JSON(200, role)
}

func (a *API) assignRole(c *gin.Context) { a.changeMemberRole(c, true) }
func (a *API) removeRole(c *gin.Context) { a.changeMemberRole(c, false) }

func (a *API) changeMemberRole(c *gin.Context, assign bool) {
	guild, context, ok := a.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	memberID, err1 := uuid.Parse(c.Param("memberID"))
	roleID, err2 := uuid.Parse(c.Param("roleID"))
	if err1 != nil || err2 != nil {
		fail(c, 400, "INVALID_ID", "成员或角色 ID 无效")
		return
	}
	var member model.Member
	var role model.Role
	if a.db.First(&member, "id = ? AND guild_id = ?", memberID, guild.ID).Error != nil || a.db.First(&role, "id = ? AND guild_id = ?", roleID, guild.ID).Error != nil {
		fail(c, 404, "NOT_FOUND", "成员或角色不存在")
		return
	}
	if role.IsEveryone {
		fail(c, 400, "EVERYONE_IMPLICIT", "@everyone 自动应用，不能手工绑定")
		return
	}
	if !a.canManageRole(context, role) || !a.canManageMember(context, guild, member) {
		fail(c, 403, "CANNOT_MANAGE_MEMBER", "角色层级不足")
		return
	}
	binding := model.MemberRole{MemberID: member.ID, RoleID: role.ID}
	if assign {
		if err := a.db.FirstOrCreate(&binding, binding).Error; err != nil {
			fail(c, 500, "DATABASE_ERROR", "绑定角色失败")
			return
		}
		c.JSON(200, binding)
		return
	}
	a.db.Delete(&binding)
	c.Status(204)
}

type createChannelRequest struct {
	Name string            `json:"name" binding:"required,min=1,max=100"`
	Type model.ChannelType `json:"type" binding:"required,oneof=TEXT VOICE"`
}

func (a *API) createChannel(c *gin.Context) {
	var input createChannelRequest
	if !bind(c, &input) {
		return
	}
	guild, _, ok := a.requireGuildPermission(c, rbac.ManageChannels)
	if !ok {
		return
	}
	channel := model.Channel{ID: uuid.New(), GuildID: guild.ID, Name: strings.TrimSpace(input.Name), Type: input.Type}
	if err := a.db.Create(&channel).Error; err != nil {
		fail(c, 500, "DATABASE_ERROR", "创建频道失败")
		return
	}
	c.JSON(201, channel)
}

type overwriteRequest struct {
	Type  model.OverwriteType `json:"type" binding:"required,oneof=ROLE MEMBER"`
	Allow int64               `json:"allow"`
	Deny  int64               `json:"deny"`
}

func (a *API) upsertOverwrite(c *gin.Context) {
	var input overwriteRequest
	if !bind(c, &input) {
		return
	}
	guild, context, ok := a.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	requested := permissionMask(input.Allow)
	denied := permissionMask(input.Deny)
	if requested&denied != 0 {
		fail(c, 400, "OVERWRITE_CONFLICT", "同一权限位不能同时 allow 和 deny")
		return
	}
	if !context.systemAdmin && !rbac.Has(context.permissions, requested) {
		fail(c, 403, "CANNOT_GRANT_PERMISSION", "不能授予超过自身的权限")
		return
	}
	channelID, err1 := uuid.Parse(c.Param("channelID"))
	targetID, err2 := uuid.Parse(c.Param("targetID"))
	if err1 != nil || err2 != nil {
		fail(c, 400, "INVALID_ID", "频道或目标 ID 无效")
		return
	}
	var channel model.Channel
	if a.db.First(&channel, "id = ? AND guild_id = ?", channelID, guild.ID).Error != nil {
		fail(c, 404, "NOT_FOUND", "频道不存在")
		return
	}
	if !a.overwriteTargetExists(guild.ID, input.Type, targetID) {
		fail(c, 404, "NOT_FOUND", "覆盖目标不存在")
		return
	}
	overwrite := model.ChannelOverwrite{ID: uuid.New(), ChannelID: channel.ID, Type: input.Type, TargetID: targetID, Allow: input.Allow, Deny: input.Deny}
	err := a.db.Where(model.ChannelOverwrite{ChannelID: channel.ID, Type: input.Type, TargetID: targetID}).Assign(map[string]any{"allow": input.Allow, "deny": input.Deny}).FirstOrCreate(&overwrite).Error
	if err != nil {
		fail(c, 500, "DATABASE_ERROR", "保存频道覆盖失败")
		return
	}
	c.JSON(200, overwrite)
}

func (a *API) myGuildPermissions(c *gin.Context) {
	guild, context, ok := a.guildContext(c)
	if !ok {
		return
	}
	c.JSON(200, gin.H{"guild_id": guild.ID, "permissions": databaseMask(context.permissions)})
}

func (a *API) myChannelPermissions(c *gin.Context) {
	guild, context, ok := a.guildContext(c)
	if !ok {
		return
	}
	var channel model.Channel
	if a.db.First(&channel, "id = ? AND guild_id = ?", c.Param("channelID"), guild.ID).Error != nil {
		fail(c, 404, "NOT_FOUND", "频道不存在")
		return
	}
	var overwrites []model.ChannelOverwrite
	a.db.Where("channel_id = ?", channel.ID).Find(&overwrites)
	converted := make([]rbac.Overwrite, 0, len(overwrites))
	for _, overwrite := range overwrites {
		converted = append(converted, rbac.Overwrite{TargetID: overwrite.TargetID.String(), Member: overwrite.Type == model.OverwriteMember, Allow: permissionMask(overwrite.Allow), Deny: permissionMask(overwrite.Deny)})
	}
	permissions := rbac.ChannelPermissions(context.owner || context.systemAdmin, context.member.UserID.String(), context.roles, converted)
	if !rbac.Has(permissions, rbac.ViewChannel) {
		fail(c, 404, "NOT_FOUND", "频道不存在")
		return
	}
	c.JSON(200, gin.H{"guild_id": guild.ID, "channel_id": channel.ID, "permissions": databaseMask(permissions)})
}

type guildPermissionContext struct {
	member      model.Member
	roles       []rbac.RolePermissions
	permissions rbac.Permission
	highestRole int
	owner       bool
	systemAdmin bool
}

func (a *API) guildForUser(c *gin.Context) (model.Guild, bool) {
	guild, _, ok := a.guildContext(c)
	return guild, ok
}

func (a *API) guildContext(c *gin.Context) (model.Guild, guildPermissionContext, bool) {
	user := currentUser(c)
	var guild model.Guild
	if err := a.db.First(&guild, "id = ?", c.Param("guildID")).Error; err != nil {
		fail(c, 404, "NOT_FOUND", "服务器不存在")
		return guild, guildPermissionContext{}, false
	}
	context := guildPermissionContext{owner: guild.OwnerUserID == user.ID, systemAdmin: user.SystemAdmin}
	if !context.systemAdmin {
		if err := a.db.First(&context.member, "guild_id = ? AND user_id = ?", guild.ID, user.ID).Error; err != nil {
			fail(c, 404, "NOT_FOUND", "服务器不存在")
			return guild, context, false
		}
	}
	var roles []model.Role
	if context.systemAdmin {
		context.permissions = rbac.AllDefined
		return guild, context, true
	}
	err := a.db.Raw(`SELECT roles.* FROM roles WHERE roles.guild_id = ? AND (roles.is_everyone = true OR roles.id IN (SELECT role_id FROM member_roles WHERE member_id = ?)) ORDER BY roles.position`, guild.ID, context.member.ID).Scan(&roles).Error
	if err != nil {
		fail(c, 500, "DATABASE_ERROR", "读取成员权限失败")
		return guild, context, false
	}
	for _, role := range roles {
		context.roles = append(context.roles, rbac.RolePermissions{ID: role.ID.String(), Permissions: permissionMask(role.Permissions), Everyone: role.IsEveryone})
		if role.Position > context.highestRole {
			context.highestRole = role.Position
		}
	}
	context.permissions = rbac.GuildPermissions(context.owner, context.roles)
	return guild, context, true
}

func (a *API) requireGuildPermission(c *gin.Context, required rbac.Permission) (model.Guild, guildPermissionContext, bool) {
	guild, context, ok := a.guildContext(c)
	if !ok {
		return guild, context, false
	}
	if !rbac.Has(context.permissions, required) {
		fail(c, 403, "MISSING_PERMISSION", "权限不足")
		return guild, context, false
	}
	return guild, context, true
}

func (a *API) canGrant(context guildPermissionContext, requested rbac.Permission, position int) bool {
	return context.systemAdmin || context.owner || (position < context.highestRole && rbac.Has(context.permissions, requested))
}

func (a *API) canManageRole(context guildPermissionContext, role model.Role) bool {
	return context.systemAdmin || context.owner || (!role.IsEveryone && context.highestRole > role.Position)
}

func (a *API) canManageMember(context guildPermissionContext, guild model.Guild, member model.Member) bool {
	if context.systemAdmin {
		return true
	}
	if member.UserID == guild.OwnerUserID {
		return false
	}
	if context.owner {
		return true
	}
	var highest int
	a.db.Raw(`SELECT COALESCE(MAX(roles.position), 0) FROM roles JOIN member_roles ON member_roles.role_id = roles.id WHERE member_roles.member_id = ?`, member.ID).Scan(&highest)
	return context.highestRole > highest
}

func (a *API) overwriteTargetExists(guildID uuid.UUID, targetType model.OverwriteType, targetID uuid.UUID) bool {
	var count int64
	if targetType == model.OverwriteRole {
		a.db.Model(&model.Role{}).Where("id = ? AND guild_id = ?", targetID, guildID).Count(&count)
	} else {
		a.db.Model(&model.Member{}).Where("id = ? AND guild_id = ?", targetID, guildID).Count(&count)
	}
	return count == 1
}
