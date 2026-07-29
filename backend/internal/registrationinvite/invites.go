package registrationinvite

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// inviteView 管理 API 返回的邀请条目：附分享链接与客户端深链，控制台一键复制分发。
type inviteView struct {
	ID        uuid.UUID  `json:"id"`
	Code      string     `json:"code"`
	ShareURL  string     `json:"share_url"`
	DeepLink  string     `json:"deep_link"`
	CreatedBy uuid.UUID  `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
	Revoked   bool       `json:"revoked"`
	// Status active / expired / exhausted / revoked。
	Status string `json:"status"`
}

func (h *api) toView(c *gin.Context, invite model.RegistrationInvite, scheme string, now time.Time) inviteView {
	base := h.baseURL(c)
	return inviteView{
		ID:        invite.ID,
		Code:      invite.Code,
		ShareURL:  base + "/register/" + invite.Code,
		DeepLink:  deepLink(scheme, base, invite.Code),
		CreatedBy: invite.CreatedBy,
		CreatedAt: invite.CreatedAt,
		ExpiresAt: invite.ExpiresAt,
		MaxUses:   invite.MaxUses,
		Uses:      invite.Uses,
		Revoked:   invite.RevokedAt != nil,
		Status:    statusLabel(invite, now),
	}
}

func (h *api) audit(c *gin.Context, action, targetID string, detail map[string]any) {
	actor := h.deps.CurrentUser(c)
	actorID := actor.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: "system_admin",
		Action: action, TargetType: "registration_invite", TargetID: targetID, Detail: detail,
	})
}

type createInviteRequest struct {
	// TTLSeconds 邀请有效期（秒），可选；0 或缺省表示不过期，上限 90 天。
	TTLSeconds int `json:"ttl_seconds" binding:"omitempty,min=60,max=7776000"`
	// MaxUses 最大使用次数，可选；0 或缺省表示不限次，1 即一次性邀请。
	MaxUses int `json:"max_uses" binding:"omitempty,min=1,max=10000"`
}

// createInvite POST /admin/registration-invites：生成注册邀请短码。
func (h *api) createInvite(c *gin.Context) {
	var input createInviteRequest
	if err := c.ShouldBindJSON(&input); err != nil && err.Error() != "EOF" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	var expiresAt *time.Time
	if input.TTLSeconds > 0 {
		expiry := time.Now().UTC().Add(time.Duration(input.TTLSeconds) * time.Second)
		expiresAt = &expiry
	}
	user := h.deps.CurrentUser(c)
	var invite model.RegistrationInvite
	// 短码冲突概率极低，仍重试数次兜底唯一索引冲突。
	for attempt := 0; attempt < 3; attempt++ {
		code, err := newInviteCode()
		if err != nil {
			fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "生成邀请码失败")
			return
		}
		invite = model.RegistrationInvite{ID: uuid.New(), Code: code, CreatedBy: user.ID, ExpiresAt: expiresAt, MaxUses: input.MaxUses}
		if err := h.deps.DB.Create(&invite).Error; err == nil {
			h.audit(c, "platform.registration_invite_create", invite.Code, map[string]any{
				"expires_at": expiresAt, "max_uses": invite.MaxUses,
			})
			c.JSON(http.StatusCreated, h.toView(c, invite, h.loadPortal().DeepLinkScheme, time.Now().UTC()))
			return
		}
	}
	fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建注册邀请失败")
}

// listInvites GET /admin/registration-invites：全部注册邀请（含已撤销/过期，带状态字段）。
func (h *api) listInvites(c *gin.Context) {
	var invites []model.RegistrationInvite
	if err := h.deps.DB.Order("created_at DESC").Find(&invites).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取注册邀请失败")
		return
	}
	scheme := h.loadPortal().DeepLinkScheme
	now := time.Now().UTC()
	result := make([]inviteView, 0, len(invites))
	for _, invite := range invites {
		result = append(result, h.toView(c, invite, scheme, now))
	}
	c.JSON(http.StatusOK, result)
}

// revokeInvite DELETE /admin/registration-invites/{id}：软撤销（置 RevokedAt，保留记录）。
func (h *api) revokeInvite(c *gin.Context) {
	inviteID, err := uuid.Parse(c.Param("inviteID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "注册邀请不存在")
		return
	}
	var invite model.RegistrationInvite
	if err := h.deps.DB.First(&invite, "id = ?", inviteID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "注册邀请不存在")
		return
	}
	if invite.RevokedAt == nil {
		now := time.Now().UTC()
		if err := h.deps.DB.Model(&invite).Update("revoked_at", now).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "撤销注册邀请失败")
			return
		}
		h.audit(c, "platform.registration_invite_revoke", invite.Code, nil)
	}
	c.Status(http.StatusNoContent)
}
