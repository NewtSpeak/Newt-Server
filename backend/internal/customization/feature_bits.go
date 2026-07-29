package customization

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// 扩展功能权限位（52–54）单独管理：位值超过 JS Number 的 2^53 精度上限，
// 不能经由控制台的数值型权限矩阵读写，改为服务端置位、布尔量进出。

// FeatureBitsMask 扩展功能位集合。
const FeatureBitsMask = rbac.ManageBots | rbac.ManageBadges | rbac.ManageCustomization

type featureBitsView struct {
	ManageBots          bool `json:"manage_bots"`
	ManageBadges        bool `json:"manage_badges"`
	ManageCustomization bool `json:"manage_customization"`
}

type featureBitsRequest struct {
	ManageBots          *bool `json:"manage_bots"`
	ManageBadges        *bool `json:"manage_badges"`
	ManageCustomization *bool `json:"manage_customization"`
}

func toFeatureBitsView(mask rbac.Permission) featureBitsView {
	return featureBitsView{
		ManageBots:          rbac.Has(mask, rbac.ManageBots),
		ManageBadges:        rbac.Has(mask, rbac.ManageBadges),
		ManageCustomization: rbac.Has(mask, rbac.ManageCustomization),
	}
}

func (h *api) requireFeatureBitsManager(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return nil, user, false
	}
	if !ctx.SystemAdmin && !ctx.Owner && !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有管理扩展权限位的权限")
		return nil, user, false
	}
	return ctx, user, true
}

// getRoleFeatureBits GET /guilds/{gid}/roles/{rid}/feature-bits。
func (h *api) getRoleFeatureBits(c *gin.Context) {
	ctx, _, ok := h.requireFeatureBitsManager(c)
	if !ok {
		return
	}
	var role model.Role
	if err := h.deps.DB.First(&role, "id = ? AND guild_id = ?", c.Param("roleID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return
	}
	c.JSON(http.StatusOK, toFeatureBitsView(rbac.Permission(uint64(role.Permissions))))
}

// patchRoleFeatureBits PATCH /guilds/{gid}/roles/{rid}/feature-bits：按布尔量置位/清位。
func (h *api) patchRoleFeatureBits(c *gin.Context) {
	ctx, user, ok := h.requireFeatureBitsManager(c)
	if !ok {
		return
	}
	var input featureBitsRequest
	if !bind(c, &input) {
		return
	}
	var role model.Role
	if err := h.deps.DB.First(&role, "id = ? AND guild_id = ?", c.Param("roleID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return
	}
	mask := rbac.Permission(uint64(role.Permissions))
	apply := func(bit rbac.Permission, value *bool) {
		if value == nil {
			return
		}
		if *value {
			mask |= bit
		} else {
			mask &^= bit
		}
	}
	apply(rbac.ManageBots, input.ManageBots)
	apply(rbac.ManageBadges, input.ManageBadges)
	apply(rbac.ManageCustomization, input.ManageCustomization)
	if err := h.deps.DB.Model(&model.Role{}).Where("id = ?", role.ID).
		Update("permissions", int64(uint64(mask))).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存扩展权限失败")
		return
	}
	view := toFeatureBitsView(mask)
	h.audit(ctx, user, "customization.role_feature_bits_update", "role", role.ID.String(), map[string]any{
		"manage_bots": view.ManageBots, "manage_badges": view.ManageBadges, "manage_customization": view.ManageCustomization,
	})
	// 权限位（52–54）实际变化：广播 GUILD_ROLE_UPDATE 让客户端刷新权限投影与灰置 UI。
	if h.deps.Bus != nil {
		role.Permissions = int64(uint64(mask))
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventGuildRoleUpdate,
			GuildID: &guildID,
			Payload: eventbus.NewGuildRolePayload(role),
		})
	}
	c.JSON(http.StatusOK, view)
}
