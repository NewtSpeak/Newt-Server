package registrationinvite

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// publicInfo GET /invite-api/registration/{code}：桌面客户端注册前的免登录预检。
// 与 guild 邀请一致不泄露细节：不存在 404，过期/用尽/撤销统一 410。
// remaining_uses 为 null 表示不限次数。
func (h *api) publicInfo(c *gin.Context) {
	invite, status := Resolve(h.deps.DB, c.Param("code"))
	switch status {
	case StatusNotFound:
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请码不存在")
		return
	case StatusExpired, StatusExhausted, StatusRevoked:
		fail(c, http.StatusGone, "INVITE_EXPIRED", "邀请码已失效")
		return
	}
	var remaining *int
	if invite.MaxUses > 0 {
		left := invite.MaxUses - invite.Uses
		remaining = &left
	}
	c.JSON(http.StatusOK, gin.H{
		"code":           invite.Code,
		"server_name":    h.loadPortal().AppName,
		"expires_at":     invite.ExpiresAt,
		"remaining_uses": remaining,
	})
}
