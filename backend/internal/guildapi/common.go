package guildapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// api 共享 handler 集合；平面差异全部收敛在 deps.Auth / deps.CurrentUser。
type api struct {
	deps appdeps.Deps
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

func permissionMask(value int64) rbac.Permission { return rbac.Permission(uint64(value)) }
func databaseMask(value rbac.Permission) int64   { return int64(uint64(value)) }

// maskString 权限掩码的对外形态：十进制字符串（uint64 全 64 位无损；
// 扩展位 52–54 超出 JS Number 2^53 精度，数值形式会被前端静默截断）。
func maskString(value rbac.Permission) string { return strconv.FormatUint(uint64(value), 10) }

// publish 发布事件（bus 未注入时 no-op，纯单测兼容）。
func (h *api) publish(event eventbus.Event) {
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(event)
	}
}

// guildCtx 解析 guildID 并加载当前用户权限上下文；非成员/不存在统一 404。
func (h *api) guildCtx(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	return ctx, user, true
}

// requireGuildPermission guildCtx + 服务器级权限校验（可见但权限不足 → 403）。
func (h *api) requireGuildPermission(c *gin.Context, required rbac.Permission) (*perms.GuildContext, model.User, bool) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return nil, user, false
	}
	if !ctx.Has(required) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "权限不足")
		return ctx, user, false
	}
	return ctx, user, true
}

// channelCtx 顶级 /channels/{cid} 路由：由频道反查 guild 并加载权限上下文。
// 频道不存在或当前用户无 VIEW_CHANNEL 一律 404（防扫频）。
func (h *api) channelCtx(c *gin.Context, required rbac.Permission) (*perms.GuildContext, model.User, model.Channel, bool) {
	user := h.deps.CurrentUser(c)
	var channel model.Channel
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return nil, user, channel, false
	}
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return nil, user, channel, false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, channel.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return nil, user, channel, false
	}
	// 无 VIEW_CHANNEL 一律 404。
	if _, _, err := ctx.ChannelPerms(h.deps.DB, channel.ID); err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return nil, user, channel, false
	}
	if !ctx.Has(required) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "权限不足")
		return ctx, user, channel, false
	}
	return ctx, user, channel, true
}

// canGrant 授予校验（与原后台实现一致）：系统管/所有者任意；
// 其余需目标层级严格低于自身最高角色，且不能授予超过自身的权限位。
func canGrant(ctx *perms.GuildContext, requested rbac.Permission, position int) bool {
	return ctx.SystemAdmin || ctx.Owner || (position < ctx.HighestRole && ctx.Has(requested))
}

// canManageRole 角色管理层级校验：@everyone 仅系统管/所有者可动，
// 其余需自身最高角色严格高于目标角色。
func canManageRole(ctx *perms.GuildContext, role model.Role) bool {
	return ctx.SystemAdmin || ctx.Owner || (!role.IsEveryone && ctx.HighestRole > role.Position)
}

// canManageMember 成员治理层级校验：所有者不可被治理；系统管/所有者任意；
// 其余需自身最高角色严格高于目标成员最高角色。
func (h *api) canManageMember(ctx *perms.GuildContext, member model.Member) bool {
	if ctx.SystemAdmin {
		return true
	}
	if member.UserID == ctx.Guild.OwnerUserID {
		return false
	}
	if ctx.Owner {
		return true
	}
	return ctx.HighestRole > h.highestRoleOf(member.ID)
}

// highestRoleOf 目标成员最高角色 position（无角色为 0，即 @everyone 基线）。
func (h *api) highestRoleOf(memberID uuid.UUID) int {
	var highest int
	h.deps.DB.Raw(`SELECT COALESCE(MAX(roles.position), 0) FROM roles JOIN member_roles ON member_roles.role_id = roles.id WHERE member_roles.member_id = ?`, memberID).Scan(&highest)
	return highest
}

// findMemberByPathID 解析路径段中的成员标识：优先 members.id，其次 user_id。
// 文档写 :uid；角色绑定等历史路径也传成员记录 ID，两侧均接受。
func (h *api) findMemberByPathID(guildID, pathID uuid.UUID) (model.Member, bool) {
	var member model.Member
	if err := h.deps.DB.First(&member, "id = ? AND guild_id = ?", pathID, guildID).Error; err == nil {
		return member, true
	}
	if err := h.deps.DB.First(&member, "user_id = ? AND guild_id = ?", pathID, guildID).Error; err == nil {
		return member, true
	}
	return member, false
}

// audit RBAC/结构变更审计写入（沿用 internal/audit 风格与 actor_type 判定）。
func (h *api) audit(ctx *perms.GuildContext, user model.User, action, targetType, targetID string, detail map[string]any) {
	actorID := user.ID
	actorType := "user"
	if ctx.SystemAdmin {
		actorType = "system_admin"
	} else if ctx.Owner || ctx.Has(rbac.Administrator) {
		actorType = "guild_admin"
	}
	guildID := ctx.Guild.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID:    &actorID,
		ActorType:  actorType,
		GuildID:    &guildID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
	})
}
