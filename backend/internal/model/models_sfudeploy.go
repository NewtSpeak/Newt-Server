package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SFU 一键部署：部署任务状态。
const (
	SfuDeployRunning   = "RUNNING"
	SfuDeploySucceeded = "SUCCEEDED"
	SfuDeployFailed    = "FAILED"
	SfuDeployCanceled  = "CANCELED"
)

// SFU 一键部署：步骤标识（状态机顺序推进，见 internal/sfudeploy）。
const (
	SfuDeployStepConnecting       = "CONNECTING"
	SfuDeployStepPrecheck         = "PRECHECK"
	SfuDeployStepInstallDeps      = "INSTALL_DEPS"
	SfuDeployStepCreateNode       = "CREATE_NODE"
	SfuDeployStepConfigure        = "CONFIGURE"
	SfuDeployStepWaitOnline       = "WAIT_ONLINE"
	SfuDeployStepEnableScheduling = "ENABLE_SCHEDULING"
	SfuDeployStepDone             = "DONE"
)

// SfuDeployParamMap 部署参数快照（jsonb）；绝不包含 SSH 凭据与 enrollment token。
type SfuDeployParamMap map[string]any

func (m SfuDeployParamMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(m)
	return string(raw), err
}

func (m *SfuDeployParamMap) Scan(value any) error { return scanJSON(value, m) }

// SfuDeployServer 已保存的部署目标服务器。
// SSH 凭据（密码或私钥+passphrase+sudo 密码）以 AES-256-GCM 加密存于
// EncryptedCredential（主密钥在 ClusterSecret，见 internal/sfudeploy/credentials.go），
// 任何 API 响应与日志不得出现明文（json:"-"）。
type SfuDeployServer struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Name     string    `gorm:"size:100;not null" json:"name"`
	Host     string    `gorm:"size:255;not null;uniqueIndex:idx_sfudeploy_server_target,priority:1" json:"host"`
	Port     int       `gorm:"not null;default:22;uniqueIndex:idx_sfudeploy_server_target,priority:2" json:"port"`
	Username string    `gorm:"size:64;not null;uniqueIndex:idx_sfudeploy_server_target,priority:3" json:"username"`
	// AuthMethod password | private_key。
	AuthMethod          string `gorm:"size:16;not null" json:"auth_method"`
	EncryptedCredential []byte `gorm:"type:bytea" json:"-"`
	// HostKeyFingerprint TOFU：首次连接记录 SHA256:xxx 指纹，后续比对防中间人。
	HostKeyFingerprint string    `gorm:"size:128" json:"host_key_fingerprint"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// SfuNodeDeployment 单次自动部署记录（不含任何凭据）。
type SfuNodeDeployment struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	// ServerID 关联的已保存服务器（凭据来源）；一次性凭据部署时为空。
	ServerID *uuid.UUID `gorm:"type:uuid;index" json:"server_id,omitempty"`
	// NodeID CREATE_NODE 步骤完成后回填。
	NodeID   *uuid.UUID `gorm:"type:uuid;index" json:"node_id,omitempty"`
	Host     string     `gorm:"size:255;not null;index" json:"host"`
	Port     int        `gorm:"not null;default:22" json:"port"`
	Username string     `gorm:"size:64;not null" json:"username"`
	Status   string     `gorm:"size:24;not null;index" json:"status"`
	Step     string     `gorm:"size:32;not null" json:"step"`
	ErrorMsg string     `gorm:"type:text" json:"error,omitempty"`
	// Params 部署参数快照（display_name/tls_mode/domain/public_ip/端口等），供重试预填。
	Params SfuDeployParamMap `gorm:"type:jsonb;not null;default:'{}'" json:"params"`
	// Log 全量部署日志（上限约 256KB，由 logbuf 截断）；列表接口不返回，详情单独拉取。
	Log        string     `gorm:"type:text" json:"-"`
	CreatedBy  uuid.UUID  `gorm:"type:uuid" json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func init() {
	Register(&SfuDeployServer{}, &SfuNodeDeployment{})
}
