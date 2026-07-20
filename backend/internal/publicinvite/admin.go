package publicinvite

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// requireLandingManager 落地页内容管理权限：MANAGE_GUILD。
func (h *api) requireLandingManager(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return nil, user, false
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有管理邀请落地页的权限")
		return nil, user, false
	}
	return ctx, user, true
}

// getLanding GET /guilds/{gid}/invite-landing：落地页配置 + 全部内容块（含停用项，编辑视图）。
func (h *api) getLanding(c *gin.Context) {
	ctx, _, ok := h.requireLandingManager(c)
	if !ok {
		return
	}
	landing := model.InviteLandingConfig{GuildID: ctx.Guild.ID, Enabled: true, AutoDeepLink: true}
	_ = h.deps.DB.Where("guild_id = ?", ctx.Guild.ID).First(&landing).Error
	var notices []model.InviteNotice
	_ = h.deps.DB.Where("guild_id = ?", ctx.Guild.ID).Order("position ASC, created_at ASC").Find(&notices).Error
	if notices == nil {
		notices = []model.InviteNotice{}
	}
	c.JSON(http.StatusOK, gin.H{"config": landing, "notices": notices})
}

type landingRequest struct {
	Description  string `json:"description" binding:"max=4000"`
	Enabled      *bool  `json:"enabled"`
	AutoDeepLink *bool  `json:"auto_deep_link"`
}

// putLanding PUT /guilds/{gid}/invite-landing：更新落地页配置（幂等 upsert）。
func (h *api) putLanding(c *gin.Context) {
	ctx, user, ok := h.requireLandingManager(c)
	if !ok {
		return
	}
	var input landingRequest
	if !bind(c, &input) {
		return
	}
	landing := model.InviteLandingConfig{
		ID: uuid.New(), GuildID: ctx.Guild.ID,
		Description: input.Description, Enabled: true, AutoDeepLink: true, UpdatedBy: user.ID,
	}
	if input.Enabled != nil {
		landing.Enabled = *input.Enabled
	}
	if input.AutoDeepLink != nil {
		landing.AutoDeepLink = *input.AutoDeepLink
	}
	err := h.deps.DB.Where(model.InviteLandingConfig{GuildID: ctx.Guild.ID}).
		Assign(map[string]any{
			"description": landing.Description, "enabled": landing.Enabled,
			"auto_deep_link": landing.AutoDeepLink, "updated_by": user.ID,
		}).FirstOrCreate(&landing).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存落地页配置失败")
		return
	}
	h.audit(ctx, user, "publicinvite.landing_update", "guild", ctx.Guild.ID.String(), map[string]any{
		"enabled": landing.Enabled, "auto_deep_link": landing.AutoDeepLink,
	})
	c.JSON(http.StatusOK, landing)
}

type noticeRequest struct {
	Kind     model.InviteNoticeKind `json:"kind" binding:"required,oneof=ANNOUNCEMENT NOTICE AGREEMENT"`
	Title    string                 `json:"title" binding:"required,min=1,max=200"`
	Body     string                 `json:"body" binding:"max=20000"`
	Position int                    `json:"position"`
	Enabled  *bool                  `json:"enabled"`
}

// createNotice POST /guilds/{gid}/invite-notices：新增公告/注意事项/协议内容块。
func (h *api) createNotice(c *gin.Context) {
	ctx, user, ok := h.requireLandingManager(c)
	if !ok {
		return
	}
	var input noticeRequest
	if !bind(c, &input) {
		return
	}
	notice := model.InviteNotice{
		ID: uuid.New(), GuildID: ctx.Guild.ID,
		Kind: input.Kind, Title: strings.TrimSpace(input.Title), Body: input.Body,
		Position: input.Position, Enabled: true,
	}
	if input.Enabled != nil {
		notice.Enabled = *input.Enabled
	}
	if err := h.deps.DB.Create(&notice).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建内容块失败")
		return
	}
	h.audit(ctx, user, "publicinvite.notice_create", "invite_notice", notice.ID.String(), map[string]any{
		"kind": notice.Kind, "title": notice.Title,
	})
	c.JSON(http.StatusCreated, notice)
}

// updateNotice PATCH /guilds/{gid}/invite-notices/{nid}：编辑内容块。
func (h *api) updateNotice(c *gin.Context) {
	ctx, user, ok := h.requireLandingManager(c)
	if !ok {
		return
	}
	var input noticeRequest
	if !bind(c, &input) {
		return
	}
	var notice model.InviteNotice
	if err := h.deps.DB.First(&notice, "id = ? AND guild_id = ?", c.Param("noticeID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "内容块不存在")
		return
	}
	notice.Kind, notice.Title, notice.Body, notice.Position = input.Kind, strings.TrimSpace(input.Title), input.Body, input.Position
	if input.Enabled != nil {
		notice.Enabled = *input.Enabled
	}
	if err := h.deps.DB.Save(&notice).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新内容块失败")
		return
	}
	h.audit(ctx, user, "publicinvite.notice_update", "invite_notice", notice.ID.String(), map[string]any{
		"kind": notice.Kind, "title": notice.Title,
	})
	c.JSON(http.StatusOK, notice)
}

// deleteNotice DELETE /guilds/{gid}/invite-notices/{nid}：删除内容块。
func (h *api) deleteNotice(c *gin.Context) {
	ctx, user, ok := h.requireLandingManager(c)
	if !ok {
		return
	}
	var notice model.InviteNotice
	if err := h.deps.DB.First(&notice, "id = ? AND guild_id = ?", c.Param("noticeID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "内容块不存在")
		return
	}
	if err := h.deps.DB.Delete(&notice).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除内容块失败")
		return
	}
	h.audit(ctx, user, "publicinvite.notice_delete", "invite_notice", notice.ID.String(), map[string]any{"title": notice.Title})
	c.Status(http.StatusNoContent)
}

// inviteView 邀请列表条目：附分享链接与深链，控制台直接复制分发。
type inviteView struct {
	model.Invite
	ShareURL string `json:"share_url"`
	DeepLink string `json:"deep_link"`
}

// listInvites GET /guilds/{gid}/invites：本服有效邀请（需 CREATE_INSTANT_INVITE 或 MANAGE_GUILD）。
func (h *api) listInvites(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.CreateInstantInvite) && !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有查看邀请的权限")
		return
	}
	var invites []model.Invite
	err := h.deps.DB.Where("guild_id = ? AND (expires_at IS NULL OR expires_at > NOW())", ctx.Guild.ID).
		Order("created_at DESC").Find(&invites).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取邀请失败")
		return
	}
	base := h.baseURL(c)
	portal := h.loadPortal()
	result := make([]inviteView, 0, len(invites))
	for _, invite := range invites {
		result = append(result, inviteView{
			Invite:   invite,
			ShareURL: base + "/invite/" + invite.Code,
			DeepLink: deepLink(portal.DeepLinkScheme, base, invite.Code, invite.GuildID),
		})
	}
	c.JSON(http.StatusOK, result)
}

// deleteInvite DELETE /guilds/{gid}/invites/{code}：撤销邀请。
func (h *api) deleteInvite(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.ManageGuild) && !ctx.Has(rbac.CreateInstantInvite) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有撤销邀请的权限")
		return
	}
	var invite model.Invite
	if err := h.deps.DB.First(&invite, "code = ? AND guild_id = ?", c.Param("code"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在")
		return
	}
	if err := h.deps.DB.Delete(&invite).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "撤销邀请失败")
		return
	}
	h.audit(ctx, user, "publicinvite.invite_revoke", "invite", invite.Code, nil)
	c.Status(http.StatusNoContent)
}

// revokeInviteByCode DELETE /invites/{code}：按码撤销邀请（用户端顶级入口，
// Owl-Desktop docs 02 §8-2：客户端只持有邀请码，不强制携带 guildID）。
// 由码反查 guild 后走标准 RBAC：非成员/码不存在统一 404（不泄露归属），
// 成员但无 MANAGE_GUILD 且无 CREATE_INSTANT_INVITE 返回 403。
func (h *api) revokeInviteByCode(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var invite model.Invite
	if err := h.deps.DB.First(&invite, "code = ?", c.Param("code")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在")
		return
	}
	asUser := user
	if h.clientPlane {
		asUser.SystemAdmin = false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, asUser, invite.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在")
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.ManageGuild) && !ctx.Has(rbac.CreateInstantInvite) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有撤销邀请的权限")
		return
	}
	if err := h.deps.DB.Delete(&invite).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "撤销邀请失败")
		return
	}
	h.audit(ctx, user, "publicinvite.invite_revoke", "invite", invite.Code, nil)
	c.Status(http.StatusNoContent)
}

type portalRequest struct {
	AppName        string `json:"app_name" binding:"max=64"`
	DeepLinkScheme string `json:"deep_link_scheme" binding:"max=32"`
	WindowsURL     string `json:"windows_url" binding:"max=512"`
	MacosURL       string `json:"macos_url" binding:"max=512"`
	LinuxURL       string `json:"linux_url" binding:"max=512"`
	AndroidURL     string `json:"android_url" binding:"max=512"`
	IosURL         string `json:"ios_url" binding:"max=512"`
	WebsiteURL     string `json:"website_url" binding:"max=512"`
}

// getPortal GET /admin/invite-portal：全局下载渠道/深链配置（系统管理员）。
func (h *api) getPortal(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	if !user.SystemAdmin {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可查看门户配置")
		return
	}
	c.JSON(http.StatusOK, h.loadPortal())
}

// putPortal PUT /admin/invite-portal：更新全局配置（系统管理员）。
func (h *api) putPortal(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	if !user.SystemAdmin {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可修改门户配置")
		return
	}
	var input portalRequest
	if !bind(c, &input) {
		return
	}
	portal := h.loadPortal()
	if strings.TrimSpace(input.AppName) != "" {
		portal.AppName = strings.TrimSpace(input.AppName)
	}
	if strings.TrimSpace(input.DeepLinkScheme) != "" {
		portal.DeepLinkScheme = strings.TrimSpace(input.DeepLinkScheme)
	}
	portal.WindowsURL, portal.MacosURL, portal.LinuxURL = input.WindowsURL, input.MacosURL, input.LinuxURL
	portal.AndroidURL, portal.IosURL, portal.WebsiteURL = input.AndroidURL, input.IosURL, input.WebsiteURL
	portal.ID = 1
	if err := h.deps.DB.Save(&portal).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存门户配置失败")
		return
	}
	actorID := user.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: "system_admin",
		Action: "publicinvite.portal_update", TargetType: "portal", TargetID: "1",
		Detail: map[string]any{"app_name": portal.AppName, "deep_link_scheme": portal.DeepLinkScheme},
	})
	c.JSON(http.StatusOK, portal)
}
