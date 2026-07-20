package customization

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// RoleStyle 角色名样式 schema（存入 Role.Style jsonb）：
//   - type=solid：纯色，colors 恰好 1 个；
//   - type=linear：线性渐变，colors 2–8 个，angle 0–360（默认 90）；
//   - type=radial：径向渐变，colors 2–8 个，shape=circle|ellipse（默认 circle）；
//   - type 为空：清除样式（存空对象）。
//
// animated=true 时前端以流动动画渲染渐变。
type RoleStyle struct {
	Type     string   `json:"type"`
	Colors   []string `json:"colors,omitempty"`
	Angle    *int     `json:"angle,omitempty"`
	Shape    string   `json:"shape,omitempty"`
	Animated bool     `json:"animated,omitempty"`
}

var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Validate 校验并归一化样式；返回可入库的紧凑 JSON。
func (s *RoleStyle) Validate() (string, error) {
	if s.Type == "" {
		return "{}", nil
	}
	switch s.Type {
	case "solid":
		if len(s.Colors) != 1 {
			return "", fmt.Errorf("纯色样式需要且仅需要 1 个颜色")
		}
	case "linear", "radial":
		if len(s.Colors) < 2 || len(s.Colors) > 8 {
			return "", fmt.Errorf("渐变样式需要 2–8 个颜色")
		}
	default:
		return "", fmt.Errorf("不支持的样式类型 %q（可选 solid/linear/radial）", s.Type)
	}
	for _, color := range s.Colors {
		if !hexColorPattern.MatchString(color) {
			return "", fmt.Errorf("颜色 %q 不是合法的 #RRGGBB", color)
		}
	}
	if s.Type == "linear" {
		if s.Angle == nil {
			angle := 90
			s.Angle = &angle
		} else if *s.Angle < 0 || *s.Angle > 360 {
			return "", fmt.Errorf("angle 需在 0–360 之间")
		}
	} else {
		s.Angle = nil
	}
	if s.Type == "radial" {
		if s.Shape == "" {
			s.Shape = "circle"
		}
		if s.Shape != "circle" && s.Shape != "ellipse" {
			return "", fmt.Errorf("shape 仅支持 circle/ellipse")
		}
	} else {
		s.Shape = ""
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// updateRoleStyle PUT /guilds/{gid}/roles/{rid}/style：编辑角色名样式
//（纯色/线性/多色/径向渐变）。需 MANAGE_CUSTOMIZATION 或 MANAGE_ROLES。
func (h *api) updateRoleStyle(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.ManageCustomization) && !ctx.Has(rbac.ManageRoles) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有编辑角色样式的权限")
		return
	}
	var input RoleStyle
	if !bind(c, &input) {
		return
	}
	normalized, err := input.Validate()
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_STYLE", err.Error())
		return
	}
	var role model.Role
	if err := h.deps.DB.First(&role, "id = ? AND guild_id = ?", c.Param("roleID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return
	}
	before := role.Style
	role.Style = normalized
	if err := h.deps.DB.Model(&model.Role{}).Where("id = ?", role.ID).Update("style", normalized).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存角色样式失败")
		return
	}
	h.audit(ctx, user, "customization.role_style_update", "role", role.ID.String(), map[string]any{
		"before": json.RawMessage(before), "after": json.RawMessage(normalized),
	})
	if h.deps.Bus != nil {
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventGuildMemberUpdate,
			GuildID: &guildID,
			Payload: gin.H{"guild_id": guildID, "role_id": role.ID, "style": json.RawMessage(normalized)},
		})
	}
	c.JSON(http.StatusOK, role)
}
