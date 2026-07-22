package customization

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/platformbadge"
)

// badgeView 成员展示聚合里的徽章条目。
type badgeView struct {
	BadgeID     uuid.UUID  `json:"badge_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Emoji       string     `json:"emoji"`
	IconURL     string     `json:"icon_url"`
	Color       string     `json:"color"`
	GrantedAt   time.Time  `json:"granted_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// memberDisplay 成员完整展示信息：头像/横幅/强调色 + 解析后的用户名样式 + 有效徽章。
// 后台频道用户信息与客户端成员列表均按此渲染（需求：自定义样式完整呈现）。
type memberDisplay struct {
	ID              uuid.UUID       `json:"id"`
	UserID          uuid.UUID       `json:"user_id"`
	Username        string          `json:"username"`
	DisplayName     string          `json:"display_name"`
	Nickname        string          `json:"nickname"`
	Bio             string          `json:"bio"`
	IsOwner         bool            `json:"is_owner"`
	IsBot           bool            `json:"is_bot"`
	AvatarURL       string          `json:"avatar_url"`
	AvatarAnimated  bool            `json:"avatar_animated"`
	BannerURL       string          `json:"banner_url"`
	AccentColor     string          `json:"accent_color"`
	RoleIDs         []uuid.UUID     `json:"role_ids"`
	NameStyle       json.RawMessage `json:"name_style"`
	NameStyleRoleID *uuid.UUID      `json:"name_style_role_id"`
	Badges          []badgeView     `json:"badges"`
}

// listMembersDisplay GET /guilds/{gid}/members/display：成员展示聚合列表。
// 用户名样式取「该成员所绑角色中层级最高且配置了样式」的角色 Style。
func (h *api) listMembersDisplay(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	guild := ctx.Guild

	type memberRow struct {
		model.Member
		Username       string
		DisplayName    string
		Bio            string
		IsBot          bool
		SystemAdmin    bool
		AvatarURL      string
		AvatarAnimated bool
		BannerURL      string
		AccentColor    string
	}
	var rows []memberRow
	err := h.deps.DB.Raw(`SELECT members.*, users.username, users.display_name, users.bio, users.is_bot,
			users.system_admin, users.avatar_url, users.avatar_animated, users.banner_url, users.accent_color
		FROM members JOIN users ON users.id = members.user_id
		WHERE members.guild_id = ? ORDER BY members.created_at ASC`, guild.ID).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取成员失败")
		return
	}

	// 角色绑定与角色样式。
	var roles []model.Role
	if err := h.deps.DB.Where("guild_id = ?", guild.ID).Find(&roles).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取角色失败")
		return
	}
	roleByID := make(map[uuid.UUID]model.Role, len(roles))
	for _, role := range roles {
		roleByID[role.ID] = role
	}
	memberIDs := make([]uuid.UUID, 0, len(rows))
	userIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		memberIDs = append(memberIDs, r.Member.ID)
		userIDs = append(userIDs, r.Member.UserID)
	}
	bindings := make(map[uuid.UUID][]uuid.UUID)
	if len(memberIDs) > 0 {
		var links []model.MemberRole
		if err := h.deps.DB.Where("member_id IN ?", memberIDs).Find(&links).Error; err == nil {
			for _, link := range links {
				bindings[link.MemberID] = append(bindings[link.MemberID], link.RoleID)
			}
		}
	}

	// 有效徽章（未过期）。
	type grantRow struct {
		model.UserBadge
		Name        string
		Description string
		Emoji       string
		IconURL     string
		Color       string
	}
	badgesByUser := make(map[uuid.UUID][]badgeView)
	if len(userIDs) > 0 {
		var grants []grantRow
		err := h.deps.DB.Raw(`SELECT user_badges.*, badges.name, badges.description,
				badges.emoji, badges.icon_url, badges.color
			FROM user_badges JOIN badges ON badges.id = user_badges.badge_id
			WHERE user_badges.guild_id = ? AND user_badges.user_id IN ?
				AND (user_badges.expires_at IS NULL OR user_badges.expires_at > ?)
			ORDER BY user_badges.created_at ASC`, guild.ID, userIDs, time.Now().UTC()).Scan(&grants).Error
		if err == nil {
			for _, grant := range grants {
				badgesByUser[grant.UserID] = append(badgesByUser[grant.UserID], badgeView{
					BadgeID: grant.BadgeID, Name: grant.Name, Description: grant.Description,
					Emoji: grant.Emoji, IconURL: grant.IconURL, Color: grant.Color,
					GrantedAt: grant.CreatedAt, ExpiresAt: grant.ExpiresAt,
				})
			}
		}
	}

	result := make([]memberDisplay, 0, len(rows))
	for _, r := range rows {
		roleIDs := bindings[r.Member.ID]
		if roleIDs == nil {
			roleIDs = []uuid.UUID{}
		}
		// 名字样式：优先成员自选角色（须仍持有且 style 非空）；否则持有角色按 position 从高到低。
		candidates := make([]model.Role, 0, len(roleIDs)+1)
		held := make(map[uuid.UUID]struct{}, len(roleIDs)+1)
		for _, roleID := range roleIDs {
			if role, ok := roleByID[roleID]; ok {
				candidates = append(candidates, role)
				held[roleID] = struct{}{}
			}
		}
		for _, role := range roles {
			if role.IsEveryone {
				candidates = append(candidates, role)
				held[role.ID] = struct{}{}
			}
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Position > candidates[j].Position })
		nameStyle := json.RawMessage("{}")
		var styleRoleID *uuid.UUID
		if r.Member.NameStyleRoleID != nil {
			if _, ok := held[*r.Member.NameStyleRoleID]; ok {
				if role, ok := roleByID[*r.Member.NameStyleRoleID]; ok && role.Style != "" && role.Style != "{}" {
					nameStyle = json.RawMessage(role.Style)
					id := role.ID
					styleRoleID = &id
				}
			}
		}
		if styleRoleID == nil {
			for _, role := range candidates {
				if role.Style != "" && role.Style != "{}" {
					nameStyle = json.RawMessage(role.Style)
					id := role.ID
					styleRoleID = &id
					break
				}
			}
		}
		badges := badgesByUser[r.Member.UserID]
		if badges == nil {
			badges = []badgeView{}
		}
		// 系统所有者自动前置平台徽章（不落库，docs 04 FR-32）。
		if r.SystemAdmin {
			so := platformbadge.SystemOwner()
			badges = append([]badgeView{{
				BadgeID: so.BadgeID, Name: so.Name, Description: so.Description,
				Emoji: so.Emoji, Color: so.Color, GrantedAt: so.GrantedAt,
			}}, badges...)
		}
		result = append(result, memberDisplay{
			ID:              r.Member.ID,
			UserID:          r.Member.UserID,
			Username:        r.Username,
			DisplayName:     r.DisplayName,
			Nickname:        r.Member.Nickname,
			Bio:             r.Bio,
			IsOwner:         r.Member.UserID == guild.OwnerUserID,
			IsBot:           r.IsBot,
			AvatarURL:       r.AvatarURL,
			AvatarAnimated:  r.AvatarAnimated,
			BannerURL:       r.BannerURL,
			AccentColor:     r.AccentColor,
			RoleIDs:         roleIDs,
			NameStyle:       nameStyle,
			NameStyleRoleID: styleRoleID,
			Badges:          badges,
		})
	}
	c.JSON(http.StatusOK, result)
}
