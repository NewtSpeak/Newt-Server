package sfucontrol

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// EnrollmentTokenTTL 一次性 enrollment token 有效期（docs 03 §4.2：15–60 分钟）。
const EnrollmentTokenTTL = 30 * time.Minute

// CreateNodeWithEnrollment 创建 PENDING_ENROLLMENT 占位节点并签发一次性 enrollment token。
// 明文 token 仅在此返回一次，库中只存 SHA-256 哈希。
// 供管理 API（POST /admin/sfu/nodes）与自动部署编排（internal/sfudeploy）复用。
func CreateNodeWithEnrollment(db *gorm.DB, displayName string, labels map[string]string) (model.SfuNode, string, time.Time, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return model.SfuNode{}, "", time.Time{}, fmt.Errorf("生成 enrollment token 失败: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	expiresAt := time.Now().UTC().Add(EnrollmentTokenTTL)
	labelMap := model.SfuLabelMap{}
	for k, v := range labels {
		if k == "" {
			continue
		}
		labelMap[k] = v
	}
	node := model.SfuNode{
		ID:                       uuid.New(),
		DisplayName:              displayName,
		Status:                   model.SfuNodePendingEnrollment,
		Labels:                   labelMap,
		EnrollmentTokenHash:      HashEnrollmentToken(token),
		EnrollmentTokenExpiresAt: &expiresAt,
	}
	if err := db.Create(&node).Error; err != nil {
		return model.SfuNode{}, "", time.Time{}, fmt.Errorf("创建 SFU 节点失败: %w", err)
	}
	return node, token, expiresAt, nil
}
