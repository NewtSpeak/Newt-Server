package customization

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// RoleSurfaceStyle 表面样式（用户名文字 / 色点 / 徽章背景共用字段）：
//   - type=solid：纯色，colors 恰好 1 个；
//   - type=linear：线性渐变，colors 2–8 个，angle 0–360（默认 90）；
//   - type=radial：径向渐变，colors 2–8 个，shape=circle|ellipse（默认 circle）；
//   - animated=true 时前端按 speed（秒/周期，0.5–20，默认 4）做流动动画；
//   - colors_dark：暗色主题独立配色（数量规则同 colors）；空则亮暗共用 colors。
type RoleSurfaceStyle struct {
	Type       string   `json:"type"`
	Colors     []string `json:"colors,omitempty"`
	ColorsDark []string `json:"colors_dark,omitempty"`
	Angle      *int     `json:"angle,omitempty"`
	Shape      string   `json:"shape,omitempty"`
	Animated   bool     `json:"animated,omitempty"`
	// Speed 流动动画周期（秒）；仅 animated 时有效。
	Speed *float64 `json:"speed,omitempty"`
}

// RoleBadgeStyle 角色徽章（消息流/成员列表旁的标签）：
//   - background：背景纯色/线性/径向/流动渐变；
//   - background_image_url：自定义背景图（PNG/JPEG/WebP/GIF/SVG）；
//   - icon_url：自定义前景 SVG/图片；
//   - show_name：是否显示角色名（默认 true）；
//   - text_color：可选文字色 #RRGGBB；
//   - bold/italic/underline/strikethrough：徽章文字样式。
// 背景图可与渐变叠加：前端将渐变叠在图片之上。
type RoleBadgeStyle struct {
	// Enabled 显式开启徽章展示（可与背景/icon 组合）。
	Enabled bool `json:"enabled,omitempty"`
	// Background 徽章背景样式；nil 时前端用角色色/默认灰底。
	Background *RoleSurfaceStyle `json:"background,omitempty"`
	// BackgroundImageURL 上传的背景图（/public-assets/role-badges/...）。
	BackgroundImageURL string `json:"background_image_url,omitempty"`
	// IconURL 上传的徽章图标（/public-assets/role-badges/...）。
	IconURL string `json:"icon_url,omitempty"`
	// ShowName 是否显示角色名文字；nil 视为 true。
	ShowName *bool `json:"show_name,omitempty"`
	// TextColor 徽章文字色；空则前端自动选用对比色。
	TextColor string `json:"text_color,omitempty"`
	// 徽章文字样式
	Bold          bool `json:"bold,omitempty"`
	Italic        bool `json:"italic,omitempty"`
	Underline     bool `json:"underline,omitempty"`
	Strikethrough bool `json:"strikethrough,omitempty"`
}

// RoleStyle 角色名样式 schema（存入 Role.Style jsonb）：
//   - 文字侧：Type/Colors/…/Speed + bold/italic/underline/strikethrough；
//   - IconSync / Icon：列表色点；
//   - Badge：角色徽章（背景 + 自定义 icon + 文字样式）。
// type 与 badge 可独立：仅配徽章或仅配文字样式时 type 可为空。
type RoleStyle struct {
	Type       string   `json:"type"`
	Colors     []string `json:"colors,omitempty"`
	ColorsDark []string `json:"colors_dark,omitempty"`
	Angle      *int     `json:"angle,omitempty"`
	Shape      string   `json:"shape,omitempty"`
	Animated   bool     `json:"animated,omitempty"`
	Speed      *float64 `json:"speed,omitempty"`
	// 用户名文字样式（可与颜色/渐变独立配置）
	Bold          bool `json:"bold,omitempty"`
	Italic        bool `json:"italic,omitempty"`
	Underline     bool `json:"underline,omitempty"`
	Strikethrough bool `json:"strikethrough,omitempty"`
	// IconSync 打开后色点跟随文字样式；为 true 时忽略 Icon 独立配置。
	IconSync bool `json:"icon_sync,omitempty"`
	// Icon 独立色点样式（仅 IconSync=false 时生效）。
	Icon *RoleSurfaceStyle `json:"icon,omitempty"`
	// Badge 角色徽章样式（消息流/成员列表）。
	Badge *RoleBadgeStyle `json:"badge,omitempty"`
}

var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

const (
	defaultAnimSpeed = 4.0
	minAnimSpeed     = 0.5
	maxAnimSpeed     = 20.0
)

// validateSurface 校验并归一化一层表面样式。
// allowEmpty：允许 type=""（表示该层无样式）。
func validateSurface(s *RoleSurfaceStyle, allowEmpty bool) error {
	if s == nil {
		return fmt.Errorf("样式不能为空")
	}
	if s.Type == "" {
		if !allowEmpty {
			return fmt.Errorf("样式类型不能为空")
		}
		s.Colors = nil
		s.ColorsDark = nil
		s.Angle = nil
		s.Shape = ""
		s.Animated = false
		s.Speed = nil
		return nil
	}
	switch s.Type {
	case "solid":
		if len(s.Colors) != 1 {
			return fmt.Errorf("纯色样式需要且仅需要 1 个颜色")
		}
		if len(s.ColorsDark) > 1 {
			return fmt.Errorf("纯色样式暗色配色最多 1 个颜色")
		}
		s.Animated = false
		s.Speed = nil
		s.Angle = nil
		s.Shape = ""
	case "linear", "radial":
		if len(s.Colors) < 2 || len(s.Colors) > 8 {
			return fmt.Errorf("渐变样式需要 2–8 个颜色")
		}
		if len(s.ColorsDark) > 0 && (len(s.ColorsDark) < 2 || len(s.ColorsDark) > 8) {
			return fmt.Errorf("渐变样式暗色配色需要 2–8 个颜色")
		}
	default:
		return fmt.Errorf("不支持的样式类型 %q（可选 solid/linear/radial）", s.Type)
	}
	if len(s.ColorsDark) == 0 {
		s.ColorsDark = nil
	}
	for _, color := range s.Colors {
		if !hexColorPattern.MatchString(color) {
			return fmt.Errorf("颜色 %q 不是合法的 #RRGGBB", color)
		}
	}
	for _, color := range s.ColorsDark {
		if !hexColorPattern.MatchString(color) {
			return fmt.Errorf("暗色配色 %q 不是合法的 #RRGGBB", color)
		}
	}
	if s.Type == "linear" {
		if s.Angle == nil {
			angle := 90
			s.Angle = &angle
		} else if *s.Angle < 0 || *s.Angle > 360 {
			return fmt.Errorf("angle 需在 0–360 之间")
		}
		s.Shape = ""
	} else if s.Type == "radial" {
		if s.Shape == "" {
			s.Shape = "circle"
		}
		if s.Shape != "circle" && s.Shape != "ellipse" {
			return fmt.Errorf("shape 仅支持 circle/ellipse")
		}
		s.Angle = nil
	}
	if s.Animated {
		if s.Speed == nil {
			speed := defaultAnimSpeed
			s.Speed = &speed
		} else if *s.Speed < minAnimSpeed || *s.Speed > maxAnimSpeed {
			return fmt.Errorf("speed 需在 %.1f–%.0f 秒之间", minAnimSpeed, maxAnimSpeed)
		}
	} else {
		s.Speed = nil
	}
	return nil
}

func validatePublicAssetURL(url, field string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	if len(url) > 512 {
		return fmt.Errorf("徽章 %s 过长", field)
	}
	if !strings.HasPrefix(url, "/public-assets/role-badges/") &&
		!strings.HasPrefix(url, "/public-assets/profile/") {
		return fmt.Errorf("徽章 %s 必须为受管公开路径", field)
	}
	return nil
}

func validateBadge(b *RoleBadgeStyle) error {
	if b == nil {
		return nil
	}
	b.IconURL = strings.TrimSpace(b.IconURL)
	if err := validatePublicAssetURL(b.IconURL, "icon_url"); err != nil {
		return err
	}
	b.BackgroundImageURL = strings.TrimSpace(b.BackgroundImageURL)
	if err := validatePublicAssetURL(b.BackgroundImageURL, "background_image_url"); err != nil {
		return err
	}
	b.TextColor = strings.TrimSpace(b.TextColor)
	if b.TextColor != "" && !hexColorPattern.MatchString(b.TextColor) {
		return fmt.Errorf("徽章 text_color 需为 #RRGGBB")
	}
	if b.Background != nil {
		if b.Background.Type == "" {
			b.Background = nil
		} else if err := validateSurface(b.Background, false); err != nil {
			return fmt.Errorf("徽章背景：%w", err)
		}
	}
	// 无任何实质配置则视为未设置徽章
	showName := b.ShowName == nil || *b.ShowName
	hasTextStyle := b.Bold || b.Italic || b.Underline || b.Strikethrough
	if !b.Enabled && b.Background == nil && b.IconURL == "" &&
		b.BackgroundImageURL == "" && showName && b.TextColor == "" && !hasTextStyle {
		return errBadgeEmpty
	}
	return nil
}

var errBadgeEmpty = fmt.Errorf("empty badge")

// Validate 校验并归一化样式；返回可入库的紧凑 JSON。
func (s *RoleStyle) Validate() (string, error) {
	hasColor := s.Type != ""
	hasTextDecor := s.Bold || s.Italic || s.Underline || s.Strikethrough
	hasBadge := s.Badge != nil

	if !hasColor {
		// 无颜色/渐变：清空颜色相关字段，但保留文字装饰（加粗/斜体等）
		s.Type = ""
		s.Colors = nil
		s.ColorsDark = nil
		s.Angle = nil
		s.Shape = ""
		s.Animated = false
		s.Speed = nil
		s.IconSync = false
		s.Icon = nil
	} else {
		surface := RoleSurfaceStyle{
			Type: s.Type, Colors: s.Colors, ColorsDark: s.ColorsDark, Angle: s.Angle,
			Shape: s.Shape, Animated: s.Animated, Speed: s.Speed,
		}
		if err := validateSurface(&surface, false); err != nil {
			return "", err
		}
		s.Type = surface.Type
		s.Colors = surface.Colors
		s.ColorsDark = surface.ColorsDark
		s.Angle = surface.Angle
		s.Shape = surface.Shape
		s.Animated = surface.Animated
		s.Speed = surface.Speed

		if s.IconSync {
			s.Icon = nil
		} else if s.Icon != nil {
			if s.Icon.Type == "" {
				s.Icon = nil
			} else if err := validateSurface(s.Icon, false); err != nil {
				return "", fmt.Errorf("图标样式：%w", err)
			}
		}
	}

	if hasBadge {
		if err := validateBadge(s.Badge); err != nil {
			if err == errBadgeEmpty {
				s.Badge = nil
			} else {
				return "", err
			}
		}
	}

	if s.Type == "" && s.Badge == nil && !hasTextDecor {
		return "{}", nil
	}

	encoded, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// requireRoleStyleEditor MANAGE_CUSTOMIZATION 或 MANAGE_ROLES（或系统管/服主）。
func (h *api) requireRoleStyleEditor(c *gin.Context) (*model.Role, bool) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return nil, false
	}
	_ = user
	if !ctx.SystemAdmin && !ctx.Has(rbac.ManageCustomization) && !ctx.Has(rbac.ManageRoles) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有编辑角色样式的权限")
		return nil, false
	}
	var role model.Role
	if err := h.deps.DB.First(&role, "id = ? AND guild_id = ?", c.Param("roleID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "角色不存在")
		return nil, false
	}
	return &role, true
}

// updateRoleStyle PUT /guilds/{gid}/roles/{rid}/style
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
			Type:    eventbus.EventGuildRoleUpdate,
			GuildID: &guildID,
			Payload: eventbus.NewGuildRolePayload(role),
		})
	}
	c.JSON(http.StatusOK, role)
}
