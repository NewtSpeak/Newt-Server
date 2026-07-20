package userapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=8,max=128"`
}

// changePassword PATCH /users/@me/password（docs 01 FR-28）：
// 验证旧密码后更新；成功后吊销该用户除当前会话链外的全部未吊销 refresh token
//（两个受众一并吊销——改密码的安全语义是「踢掉其他所有登录」，不区分平面）。
// 当前会话由 access token 的 sid claim 识别；旧 token 无 sid 时保守起见全部吊销
//（客户端持有的 refresh token 仍可整体重新登录）。
func (h *api) changePassword(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input changePasswordRequest
	if !bind(c, &input) {
		return
	}
	if !security.VerifyPassword(user.PasswordHash, input.CurrentPassword) {
		fail(c, http.StatusForbidden, "INVALID_PASSWORD", "当前密码错误")
		return
	}
	hash, err := security.HashPassword(input.NewPassword)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_PASSWORD", err.Error())
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Update("password_hash", hash).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新密码失败")
		return
	}
	now := time.Now().UTC()
	revoke := h.deps.DB.Model(&model.RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", user.ID)
	if sid := h.currentSessionID(c); sid != uuid.Nil {
		revoke = revoke.Where("session_id <> ?", sid)
	}
	if err := revoke.Update("revoked_at", now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销其他会话失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// sessionView 会话列表条目（脱敏：不含 token 任何形态）。
type sessionView struct {
	ID uuid.UUID `json:"id"` // 会话链 session_id（DELETE /users/@me/sessions/:id 的入参）
	// Audience 会话所属认证平面（admin=后台 / client=用户端）。
	Audience  string    `json:"audience"`
	CreatedAt time.Time `json:"created_at"` // 会话首次登录时间（refresh 轮换不重置）
	// LastUsedAt 最近一次签发/轮换时间（登录或 refresh 轮换即视为使用）。
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"` // 是否为发起本次请求的会话
}

// listSessions GET /users/@me/sessions（docs 01 FR-27）：列出全部活跃登录会话
//（未吊销且未过期的 refresh token，按会话链聚合后每链恰有一个活跃 token）。
func (h *api) listSessions(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var tokens []model.RefreshToken
	err := h.deps.DB.
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", user.ID, time.Now().UTC()).
		Order("created_at DESC").Find(&tokens).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询会话失败")
		return
	}
	current := h.currentSessionID(c)
	sessions := make([]sessionView, 0, len(tokens))
	seen := make(map[uuid.UUID]bool, len(tokens))
	for _, token := range tokens {
		if seen[token.SessionID] {
			continue // 理论上每链只有一个活跃 token；并发轮换的瞬时重复取最新一条
		}
		seen[token.SessionID] = true
		sessions = append(sessions, sessionView{
			ID: token.SessionID, Audience: token.Audience,
			CreatedAt: token.SessionCreatedAt, LastUsedAt: token.CreatedAt,
			ExpiresAt: token.ExpiresAt, Current: token.SessionID == current,
		})
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// revokeSession DELETE /users/@me/sessions/:id：吊销指定会话链的全部未吊销 token。
// 允许吊销当前会话（等效登出）。无活跃 token 可吊销返回 404。
func (h *api) revokeSession(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "会话不存在")
		return
	}
	now := time.Now().UTC()
	result := h.deps.DB.Model(&model.RefreshToken{}).
		Where("user_id = ? AND session_id = ? AND revoked_at IS NULL", user.ID, sessionID).
		Update("revoked_at", now)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销会话失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "会话不存在")
		return
	}
	c.Status(http.StatusNoContent)
}
