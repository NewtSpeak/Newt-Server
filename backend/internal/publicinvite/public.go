package publicinvite

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// noticeView 公开视图仅暴露展示字段。
type noticeView struct {
	Kind  model.InviteNoticeKind `json:"kind"`
	Title string                 `json:"title"`
	Body  string                 `json:"body"`
}

func toNoticeViews(notices []model.InviteNotice) []noticeView {
	result := make([]noticeView, 0, len(notices))
	for _, notice := range notices {
		result = append(result, noticeView{Kind: notice.Kind, Title: notice.Title, Body: notice.Body})
	}
	return result
}

// publicInviteInfo GET /invite-api/invites/{code}：邀请公开信息（无需登录）。
// 未安装客户端的用户据此看到服务器信息、公告/注意事项/协议与下载引导；
// 已安装客户端可用 deep_link 直接唤起并自动填入后端地址与邀请码。
func (h *api) publicInviteInfo(c *gin.Context) {
	data, ok := h.resolveInvite(c.Param("code"))
	if !ok {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
		return
	}
	base := h.baseURL(c)
	var expiresAt *time.Time
	if data.Invite.ExpiresAt != nil {
		expiresAt = data.Invite.ExpiresAt
	}
	c.JSON(http.StatusOK, gin.H{
		"code":       data.Invite.Code,
		"expires_at": expiresAt,
		"guild": gin.H{
			"id":           data.Guild.ID,
			"name":         data.Guild.Name,
			"member_count": data.MemberCount,
		},
		"description":    data.Landing.Description,
		"auto_deep_link": data.Landing.AutoDeepLink,
		"notices":        toNoticeViews(data.Notices),
		"portal": gin.H{
			"app_name":    data.Portal.AppName,
			"website_url": data.Portal.WebsiteURL,
			"downloads": gin.H{
				"windows": data.Portal.WindowsURL,
				"macos":   data.Portal.MacosURL,
				"linux":   data.Portal.LinuxURL,
				"android": data.Portal.AndroidURL,
				"ios":     data.Portal.IosURL,
			},
		},
		"deep_link":       deepLink(data.Portal.DeepLinkScheme, base, data.Invite.Code, data.Guild.ID),
		"server_base_url": base,
		"signup_enabled":  signupEnabled(),
	})
}

// previewInvite GET /gapi/v1/invites/{code}/preview：登录用户预览邀请。
// 同后端已有有效账号的用户凭此确认后直接 POST /invites/{code}/join 免注册加入。
func (h *api) previewInvite(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	data, ok := h.resolveInvite(c.Param("code"))
	if !ok {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
		return
	}
	var member model.Member
	alreadyMember := h.deps.DB.First(&member, "guild_id = ? AND user_id = ?", data.Guild.ID, user.ID).Error == nil
	var ban model.GuildBan
	banned := h.deps.DB.First(&ban, "guild_id = ? AND user_id = ?", data.Guild.ID, user.ID).Error == nil
	c.JSON(http.StatusOK, gin.H{
		"code":       data.Invite.Code,
		"expires_at": data.Invite.ExpiresAt,
		"guild": gin.H{
			"id":           data.Guild.ID,
			"name":         data.Guild.Name,
			"member_count": data.MemberCount,
		},
		"description":    data.Landing.Description,
		"notices":        toNoticeViews(data.Notices),
		"already_member": alreadyMember,
		"banned":         banned,
	})
}
