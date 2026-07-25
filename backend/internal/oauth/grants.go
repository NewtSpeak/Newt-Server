package oauth

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

type grantView struct {
	SessionID        string    `json:"session_id"`
	ClientID         string    `json:"client_id"`
	ClientName       string    `json:"client_name"`
	Scope            string    `json:"scope"`
	DeviceName       string    `json:"device_name,omitempty"`
	Platform         string    `json:"platform,omitempty"`
	IPAddress        string    `json:"ip_address,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	SessionCreatedAt time.Time `json:"session_created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// listGrants GET /oauth/v1/grants — 当前用户已授权的 agent 会话（按 session 聚合）。
func (h *handler) listGrants(c *gin.Context) {
	user, ok := h.requireClientUser(c)
	if !ok {
		return
	}
	var rows []model.RefreshToken
	err := h.deps.DB.
		Where("user_id = ? AND audience = ? AND revoked_at IS NULL AND expires_at > ?",
			user.ID, security.AudienceAgent, time.Now().UTC()).
		Order("created_at DESC").
		Find(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取授权失败")
		return
	}
	// 按 SessionID 去重（保留最新一条元数据）
	seen := map[uuid.UUID]struct{}{}
	out := make([]grantView, 0, len(rows))
	for _, r := range rows {
		if _, ok := seen[r.SessionID]; ok {
			continue
		}
		seen[r.SessionID] = struct{}{}
		client, _ := lookupClient(r.ClientID)
		name := client.Name
		if name == "" {
			name = r.ClientID
		}
		out = append(out, grantView{
			SessionID: r.SessionID.String(), ClientID: r.ClientID, ClientName: name,
			Scope: r.Scope, DeviceName: r.DeviceName, Platform: r.Platform,
			IPAddress: r.IPAddress, CreatedAt: r.CreatedAt,
			SessionCreatedAt: r.SessionCreatedAt, ExpiresAt: r.ExpiresAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"grants": out})
}

// revokeGrant DELETE /oauth/v1/grants/:sessionID — 吊销该 OAuth 会话全部 refresh。
func (h *handler) revokeGrant(c *gin.Context) {
	user, ok := h.requireClientUser(c)
	if !ok {
		return
	}
	sid, err := uuid.Parse(c.Param("sessionID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "session_id 无效")
		return
	}
	now := time.Now().UTC()
	res := h.deps.DB.Model(&model.RefreshToken{}).
		Where("user_id = ? AND audience = ? AND session_id = ? AND revoked_at IS NULL",
			user.ID, security.AudienceAgent, sid).
		Update("revoked_at", now)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": res.RowsAffected})
}

// revokeAllGrants POST /oauth/v1/grants/revoke-all
func (h *handler) revokeAllGrants(c *gin.Context) {
	user, ok := h.requireClientUser(c)
	if !ok {
		return
	}
	now := time.Now().UTC()
	res := h.deps.DB.Model(&model.RefreshToken{}).
		Where("user_id = ? AND audience = ? AND revoked_at IS NULL", user.ID, security.AudienceAgent).
		Update("revoked_at", now)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": res.RowsAffected})
}
