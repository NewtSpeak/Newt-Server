package message

import (
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
)

// slowmodeExempt 判断当前成员是否持有频道配置的任一豁免角色。
// 普通成员直接使用 GuildContext 已加载的完整角色集合（包含 @everyone）。
// 系统管理员的上下文会跳过角色加载，因此仅该分支回查数据库。
func (s *service) slowmodeExempt(channel model.Channel, ctx *perms.GuildContext) bool {
	if len(channel.RateLimitExemptRoleIDs) == 0 || ctx == nil {
		return false
	}
	roleIDs := make([]uuid.UUID, 0, len(channel.RateLimitExemptRoleIDs))
	for _, roleID := range channel.RateLimitExemptRoleIDs {
		roleIDs = append(roleIDs, roleID)
	}
	heldRoleIDs := make([]uuid.UUID, 0, len(ctx.Roles))
	for _, role := range ctx.Roles {
		if roleID, err := uuid.Parse(role.ID); err == nil {
			heldRoleIDs = append(heldRoleIDs, roleID)
		}
	}
	if roleSetsIntersect(roleIDs, heldRoleIDs) {
		return true
	}
	if !ctx.SystemAdmin {
		return false
	}
	var everyone int64
	if s.db.Model(&model.Role{}).
		Where("guild_id = ? AND id IN ? AND is_everyone = ?", channel.GuildID, roleIDs, true).
		Count(&everyone).Error == nil && everyone > 0 {
		return true
	}
	if ctx.Member == nil {
		return false
	}
	var held int64
	if s.db.Model(&model.MemberRole{}).
		Where("member_id = ? AND role_id IN ?", ctx.Member.ID, roleIDs).
		Count(&held).Error != nil {
		return false
	}
	return held > 0
}

func roleSetsIntersect(left, right []uuid.UUID) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	known := make(map[uuid.UUID]struct{}, len(left))
	for _, id := range left {
		known[id] = struct{}{}
	}
	for _, id := range right {
		if _, ok := known[id]; ok {
			return true
		}
	}
	return false
}
