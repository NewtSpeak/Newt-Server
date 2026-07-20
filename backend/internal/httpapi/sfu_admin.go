package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfucontrol"
)

const enrollmentTokenTTL = 30 * time.Minute

// SFUOptions SFU 配套子系统依赖（由 main 装配注入）。
type SFUOptions struct {
	Registry    *sfucontrol.Registry
	MediaTokens *mediatoken.Manager
}

// AttachSFU 注入 SFU 节点注册表与 Media Token 签发器；未注入时相关路由返回 503。
func (a *API) AttachSFU(opts SFUOptions) { a.sfu = &opts }

// requireSFU SFU 子系统未装配时直接 503（理论上仅测试环境出现）。
func (a *API) requireSFU(c *gin.Context) bool {
	if a.sfu == nil || a.sfu.Registry == nil || a.sfu.MediaTokens == nil {
		fail(c, http.StatusServiceUnavailable, "SFU_UNAVAILABLE", "SFU 子系统未启用")
		return false
	}
	return true
}

func (a *API) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !currentUser(c).SystemAdmin {
			fail(c, http.StatusForbidden, "SYSTEM_ADMIN_REQUIRED", "需要系统管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

type createSfuNodeRequest struct {
	DisplayName string `json:"display_name" binding:"required,min=1,max=100"`
}

type createSfuNodeResponse struct {
	NodeID          uuid.UUID `json:"node_id"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// createSfuNode 创建节点占位并签发一次性 enrollment token（明文仅此一次返回，库中只存哈希）。
func (a *API) createSfuNode(c *gin.Context) {
	var input createSfuNodeRequest
	if !bind(c, &input) {
		return
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "生成 enrollment token 失败")
		return
	}
	token := hex.EncodeToString(tokenBytes)
	hash := sfucontrol.HashEnrollmentToken(token)
	expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
	node := model.SfuNode{
		ID:                       uuid.New(),
		DisplayName:              input.DisplayName,
		Status:                   model.SfuNodePendingEnrollment,
		EnrollmentTokenHash:      hash,
		EnrollmentTokenExpiresAt: &expiresAt,
	}
	if err := a.db.Create(&node).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建 SFU 节点失败")
		return
	}
	c.JSON(http.StatusCreated, createSfuNodeResponse{NodeID: node.ID, EnrollmentToken: token, ExpiresAt: expiresAt})
}

type sfuNodeSummary struct {
	ID                   uuid.UUID           `json:"id"`
	DisplayName          string              `json:"display_name"`
	Status               string              `json:"status"`
	EnabledForScheduling bool                `json:"enabled_for_scheduling"`
	Online               bool                `json:"online"`
	Capacity             sfucontrol.Capacity `json:"capacity"`
	AdvertiseWssURL      string              `json:"advertise_wss_url"`
	MediaUDPPort         int                 `json:"media_udp_port"`
	MediaIPs             []string            `json:"media_ips"`
	CascadeEndpoint      string              `json:"cascade_endpoint"`
	CertNotAfter         *time.Time          `json:"cert_not_after"`
	LastSeenAt           *time.Time          `json:"last_seen_at"`
	CreatedAt            time.Time           `json:"created_at"`
}

func (a *API) sfuNodeSummary(node model.SfuNode) sfuNodeSummary {
	summary := sfuNodeSummary{
		ID:                   node.ID,
		DisplayName:          node.DisplayName,
		Status:               node.Status,
		EnabledForScheduling: node.EnabledForScheduling,
		AdvertiseWssURL:      node.AdvertiseWssURL,
		MediaUDPPort:         node.MediaUDPPort,
		MediaIPs:             node.MediaIPs,
		CascadeEndpoint:      node.CascadeEndpoint,
		CertNotAfter:         node.CertNotAfter,
		LastSeenAt:           node.LastSeenAt,
		CreatedAt:            node.CreatedAt,
	}
	if snapshot, ok := a.sfu.Registry.Snapshot(node.ID); ok {
		summary.Online = snapshot.Online
		summary.Capacity = snapshot.Capacity
	}
	return summary
}

func (a *API) listSfuNodes(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var nodes []model.SfuNode
	if err := a.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 SFU 节点失败")
		return
	}
	result := make([]sfuNodeSummary, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, a.sfuNodeSummary(node))
	}
	c.JSON(http.StatusOK, result)
}

type updateSfuNodeRequest struct {
	EnabledForScheduling *bool   `json:"enabled_for_scheduling"`
	Status               *string `json:"status"`
}

// updateSfuNode PATCH 节点：调度开关与状态迁移（enable=ENROLLED / disable=DISABLED / revoke=REVOKED，
// docs 03 §8 状态机）。DISABLED/REVOKED 会强制关闭调度并断开控制流；REVOKED 为终态且作废残留 token。
func (a *API) updateSfuNode(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var input updateSfuNodeRequest
	if !bind(c, &input) {
		return
	}
	if input.EnabledForScheduling == nil && input.Status == nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "enabled_for_scheduling 与 status 至少提供一项")
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	updates := map[string]any{}
	if input.EnabledForScheduling != nil {
		updates["enabled_for_scheduling"] = *input.EnabledForScheduling
	}
	disconnect := false
	if input.Status != nil {
		if node.Status == model.SfuNodeRevoked {
			fail(c, http.StatusConflict, "NODE_REVOKED", "REVOKED 为终态，不允许再变更状态")
			return
		}
		switch *input.Status {
		case model.SfuNodeEnrolled:
			// 解除禁用：仅证书已签发的 DISABLED/DRAINING 节点可回到 ENROLLED，等待重连上线。
			if node.CertFingerprint == "" || (node.Status != model.SfuNodeDisabled && node.Status != model.SfuNodeDraining) {
				fail(c, http.StatusConflict, "INVALID_STATUS_TRANSITION", "当前状态不允许迁移到 ENROLLED")
				return
			}
			updates["status"] = model.SfuNodeEnrolled
		case model.SfuNodeDisabled:
			updates["status"] = model.SfuNodeDisabled
			updates["enabled_for_scheduling"] = false
			disconnect = true
		case model.SfuNodeRevoked:
			updates["status"] = model.SfuNodeRevoked
			updates["enabled_for_scheduling"] = false
			updates["enrollment_token_hash"] = ""
			updates["enrollment_token_expires_at"] = nil
			disconnect = true
		default:
			fail(c, http.StatusBadRequest, "INVALID_STATUS", "status 仅支持 ENROLLED（解除禁用）、DISABLED、REVOKED")
			return
		}
	}
	if err := a.db.Model(&node).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新 SFU 节点失败")
		return
	}
	if disconnect {
		a.sfu.Registry.Disconnect(node.ID)
	}
	if err := a.db.First(&node, "id = ?", node.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 SFU 节点失败")
		return
	}
	c.JSON(http.StatusOK, a.sfuNodeSummary(node))
}

// deleteSfuNode 仅允许删除尚未完成 enrollment 的占位节点（docs 03 §8：PENDING 超时/取消）。
func (a *API) deleteSfuNode(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	if node.Status != model.SfuNodePendingEnrollment {
		fail(c, http.StatusConflict, "NODE_NOT_PENDING", "仅 PENDING_ENROLLMENT 状态的节点可删除，其余请使用禁用或吊销")
		return
	}
	if err := a.db.Delete(&node).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除 SFU 节点失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// revokeSfuNode 吊销节点：状态置 REVOKED（终态）并立即断开其控制流。
func (a *API) revokeSfuNode(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	err := a.db.Model(&node).Updates(map[string]any{
		"status":                      model.SfuNodeRevoked,
		"enabled_for_scheduling":      false,
		"enrollment_token_hash":       "",
		"enrollment_token_expires_at": nil,
	}).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销 SFU 节点失败")
		return
	}
	a.sfu.Registry.Disconnect(node.ID)
	node.Status = model.SfuNodeRevoked
	c.JSON(http.StatusOK, a.sfuNodeSummary(node))
}
