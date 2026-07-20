// Package perms 提供基于数据库的成员权限计算，供语音、消息、Gateway 等新模块统一使用。
// 计算顺序对齐 docs 02：所有者/管理员短路 → @everyone → 角色覆盖 → 成员覆盖 → Restriction 收紧。
package perms

import (
	"errors"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// ErrNotFound 统一的「不可见即不存在」错误（docs 06 议题 8：无权限返回 404，防扫频）。
var ErrNotFound = errors.New("资源不存在或不可见")

// RestrictionMask 由 server 装配阶段注入（指向 restriction 包），对最终权限做「只收紧」处理。
// channel 为 nil 表示计算服务器级权限。默认恒等（Restriction 模块未启用时不收紧）。
var RestrictionMask = func(db *gorm.DB, bits rbac.Permission, userID, guildID uuid.UUID, channel *model.Channel) rbac.Permission {
	return bits
}

// GuildContext 某用户在某服务器内的权限上下文。
type GuildContext struct {
	Guild       model.Guild
	Member      *model.Member // 系统管理员可能不是成员，此时为 nil
	Roles       []rbac.RolePermissions
	Permissions rbac.Permission // 服务器级最终权限（已应用 Restriction 收紧）
	HighestRole int
	Owner       bool
	SystemAdmin bool
}

// LoadGuild 加载用户在指定服务器的权限上下文；非成员且非系统管理员返回 ErrNotFound。
func LoadGuild(db *gorm.DB, user model.User, guildID uuid.UUID) (*GuildContext, error) {
	var guild model.Guild
	if err := db.First(&guild, "id = ?", guildID).Error; err != nil {
		return nil, ErrNotFound
	}
	ctx := &GuildContext{Guild: guild, Owner: guild.OwnerUserID == user.ID, SystemAdmin: user.SystemAdmin}
	if ctx.SystemAdmin {
		ctx.Permissions = rbac.AllDefined
		var member model.Member
		if err := db.First(&member, "guild_id = ? AND user_id = ?", guild.ID, user.ID).Error; err == nil {
			ctx.Member = &member
		}
		return ctx, nil
	}
	var member model.Member
	if err := db.First(&member, "guild_id = ? AND user_id = ?", guild.ID, user.ID).Error; err != nil {
		return nil, ErrNotFound
	}
	ctx.Member = &member
	var roles []model.Role
	err := db.Raw(`SELECT roles.* FROM roles WHERE roles.guild_id = ? AND (roles.is_everyone = true OR roles.id IN (SELECT role_id FROM member_roles WHERE member_id = ?)) ORDER BY roles.position`, guild.ID, member.ID).Scan(&roles).Error
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		ctx.Roles = append(ctx.Roles, rbac.RolePermissions{ID: role.ID.String(), Permissions: rbac.Permission(uint64(role.Permissions)), Everyone: role.IsEveryone})
		if role.Position > ctx.HighestRole {
			ctx.HighestRole = role.Position
		}
	}
	ctx.Permissions = RestrictionMask(db, rbac.GuildPermissions(ctx.Owner, ctx.Roles), user.ID, guild.ID, nil)
	return ctx, nil
}

// ChannelPerms 计算用户在某频道的最终权限（含覆盖与 Restriction 收紧）。
// 频道不存在、不属于该服、或计算结果无 VIEW_CHANNEL 时一律返回 ErrNotFound。
func (ctx *GuildContext) ChannelPerms(db *gorm.DB, channelID uuid.UUID) (model.Channel, rbac.Permission, error) {
	var channel model.Channel
	if err := db.First(&channel, "id = ? AND guild_id = ?", channelID, ctx.Guild.ID).Error; err != nil {
		return channel, 0, ErrNotFound
	}
	bits, err := ctx.channelBits(db, channel)
	if err != nil {
		return channel, 0, err
	}
	if !rbac.Has(bits, rbac.ViewChannel) {
		return channel, 0, ErrNotFound
	}
	return channel, bits, nil
}

// VisibleChannels 列出该用户可见（VIEW_CHANNEL）的全部频道。
// 排序：持久化 position 优先，created_at 兜底（历史数据 position 均为 0 时保持旧序）。
func (ctx *GuildContext) VisibleChannels(db *gorm.DB) ([]model.Channel, error) {
	var channels []model.Channel
	if err := db.Where("guild_id = ?", ctx.Guild.ID).Order("position ASC, created_at ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	visible := make([]model.Channel, 0, len(channels))
	for _, channel := range channels {
		bits, err := ctx.channelBits(db, channel)
		if err != nil {
			return nil, err
		}
		if rbac.Has(bits, rbac.ViewChannel) {
			visible = append(visible, channel)
		}
	}
	return visible, nil
}

func (ctx *GuildContext) channelBits(db *gorm.DB, channel model.Channel) (rbac.Permission, error) {
	userID := uuid.Nil
	if ctx.Member != nil {
		userID = ctx.Member.UserID
	}
	var bits rbac.Permission
	if ctx.SystemAdmin || ctx.Owner {
		bits = rbac.AllDefined
	} else {
		var overwrites []model.ChannelOverwrite
		if err := db.Where("channel_id = ?", channel.ID).Find(&overwrites).Error; err != nil {
			return 0, err
		}
		converted := make([]rbac.Overwrite, 0, len(overwrites))
		for _, overwrite := range overwrites {
			converted = append(converted, rbac.Overwrite{
				TargetID: overwrite.TargetID.String(),
				Member:   overwrite.Type == model.OverwriteMember,
				Allow:    rbac.Permission(uint64(overwrite.Allow)),
				Deny:     rbac.Permission(uint64(overwrite.Deny)),
			})
		}
		bits = rbac.ChannelPermissions(false, userID.String(), ctx.Roles, converted)
	}
	// Restriction 只收紧不放宽；管理员/所有者也不豁免（docs 12 AL.1）。
	return RestrictionMask(db, bits, userID, ctx.Guild.ID, &channel), nil
}

// CanSeeChannel 供 Gateway 等场景快速判断某用户对频道的可见性。
func CanSeeChannel(db *gorm.DB, user model.User, guildID, channelID uuid.UUID) bool {
	ctx, err := LoadGuild(db, user, guildID)
	if err != nil {
		return false
	}
	_, _, err = ctx.ChannelPerms(db, channelID)
	return err == nil
}

// Has 语义糖：ctx 权限是否包含 required 全部位。
func (ctx *GuildContext) Has(required rbac.Permission) bool {
	return rbac.Has(ctx.Permissions, required)
}
