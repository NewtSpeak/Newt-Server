package publicinvite

import (
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
)

type api struct {
	deps        appdeps.Deps
	clientPlane bool
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

func (h *api) guildCtx(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	asUser := user
	if h.clientPlane {
		asUser.SystemAdmin = false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, asUser, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	return ctx, user, true
}

func (h *api) audit(ctx *perms.GuildContext, actor model.User, action, targetType, targetID string, detail map[string]any) {
	actorID := actor.ID
	actorType := "user"
	if ctx.SystemAdmin {
		actorType = "system_admin"
	} else if ctx.Owner {
		actorType = "guild_admin"
	}
	guildID := ctx.Guild.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: actorType, GuildID: &guildID,
		Action: action, TargetType: targetType, TargetID: targetID, Detail: detail,
	})
}

// baseURL 对外根地址：优先配置 PUBLIC_BASE_URL，否则按请求推导（支持反代头）。
func (h *api) baseURL(c *gin.Context) string {
	if h.deps.Cfg.PublicBaseURL != "" {
		return h.deps.Cfg.PublicBaseURL
	}
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// loadPortal 读取全局门户配置（单行，缺省值兜底）。
func (h *api) loadPortal() model.InvitePortalConfig {
	portal := model.InvitePortalConfig{ID: 1, AppName: "NewtSpeak", DeepLinkScheme: "newtspeak"}
	_ = h.deps.DB.First(&portal, "id = 1").Error
	if portal.AppName == "" {
		portal.AppName = "NewtSpeak"
	}
	if portal.DeepLinkScheme == "" {
		portal.DeepLinkScheme = "newtspeak"
	}
	return portal
}

// deepLink 客户端唤起深链：携带后端地址与邀请码，客户端免手工填写连接信息，
// 同后端已有账号时可直接免注册加入（需求：邀请分享自动加入）。
func deepLink(scheme, base, code string, guildID uuid.UUID) string {
	return scheme + "://invite?code=" + url.QueryEscape(code) +
		"&server=" + url.QueryEscape(base) + "&guild=" + guildID.String()
}

// signupEnabled 与 clientapi 同语义：平台是否开放用户端注册。
func signupEnabled() bool {
	value := os.Getenv("CLIENT_SIGNUP_ENABLED")
	if value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return enabled
}

// resolved 一次邀请解析出的落地页所需全部数据。
type resolved struct {
	Invite      model.Invite
	Guild       model.Guild
	Landing     model.InviteLandingConfig
	Notices     []model.InviteNotice
	Portal      model.InvitePortalConfig
	MemberCount int64
}

// resolveInvite 解析邀请码 → 落地页数据；过期/不存在/落地页停用均返回 false。
func (h *api) resolveInvite(code string) (resolved, bool) {
	var out resolved
	code = strings.TrimSpace(code)
	if code == "" {
		return out, false
	}
	if err := h.deps.DB.First(&out.Invite, "code = ?", code).Error; err != nil {
		return out, false
	}
	now := time.Now().UTC()
	if out.Invite.ExpiresAt != nil && !out.Invite.ExpiresAt.After(now) {
		return out, false
	}
	if err := h.deps.DB.First(&out.Guild, "id = ?", out.Invite.GuildID).Error; err != nil {
		return out, false
	}
	// 落地页配置：无记录视为启用。
	out.Landing = model.InviteLandingConfig{GuildID: out.Guild.ID, Enabled: true, AutoDeepLink: true}
	_ = h.deps.DB.Where("guild_id = ?", out.Guild.ID).First(&out.Landing).Error
	if !out.Landing.Enabled {
		return out, false
	}
	_ = h.deps.DB.Where("guild_id = ? AND enabled = true", out.Guild.ID).
		Order("position ASC, created_at ASC").Find(&out.Notices).Error
	out.Portal = h.loadPortal()
	_ = h.deps.DB.Model(&model.Member{}).Where("guild_id = ?", out.Guild.ID).Count(&out.MemberCount).Error
	return out, true
}
