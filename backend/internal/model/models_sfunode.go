package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SFU 节点状态（docs 03 §2.2 / §8 状态机）。
const (
	SfuNodePendingEnrollment = "PENDING_ENROLLMENT" // 已发 enrollment token，尚未完成
	SfuNodeEnrolled          = "ENROLLED"           // 证书已签发，未在线（含心跳失败降级）
	SfuNodeOnline            = "ONLINE"             // 控制通道已连接且心跳正常
	SfuNodeDraining          = "DRAINING"           // 排空中，不再调度新房间
	SfuNodeDisabled          = "DISABLED"           // 管理员禁用，拒绝连接
	SfuNodeRevoked           = "REVOKED"            // 证书吊销（终态）
)

// SfuLabelMap 节点标签（region、network、pool、合规等），jsonb 存储。
type SfuLabelMap map[string]string

func (m SfuLabelMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(m)
	return string(raw), err
}

func (m *SfuLabelMap) Scan(value any) error { return scanJSON(value, m) }

// SfuStringList 字符串数组，jsonb 存储。
type SfuStringList []string

func (l SfuStringList) Value() (driver.Value, error) {
	if l == nil {
		return "[]", nil
	}
	raw, err := json.Marshal(l)
	return string(raw), err
}

func (l *SfuStringList) Scan(value any) error { return scanJSON(value, l) }

// SfuFloatMap 浮点映射（如节点间 RTT，key 为对端 node_id），jsonb 存储。
type SfuFloatMap map[string]float64

func (m SfuFloatMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(m)
	return string(raw), err
}

func (m *SfuFloatMap) Scan(value any) error { return scanJSON(value, m) }

func scanJSON(value, target any) error {
	switch data := value.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(data, target)
	case string:
		return json.Unmarshal([]byte(data), target)
	default:
		return fmt.Errorf("无法把 %T 反序列化为 JSON 字段", value)
	}
}

// SfuNode SFU 节点记录，字段对齐 docs 03 §2.2。
type SfuNode struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	DisplayName string    `gorm:"size:100;not null" json:"display_name"`
	Status      string    `gorm:"size:32;not null;default:'PENDING_ENROLLMENT';index:idx_sfunode_status" json:"status"`

	// 证书信息；轮换时保留上一张指纹形成短暂双指纹窗口（docs 03 §4.4）。
	CertFingerprint     string     `gorm:"size:64;index:idx_sfunode_cert_fp" json:"cert_fingerprint"`
	PrevCertFingerprint string     `gorm:"size:64" json:"prev_cert_fingerprint,omitempty"`
	CertNotAfter        *time.Time `json:"cert_not_after,omitempty"`

	Labels SfuLabelMap `gorm:"type:jsonb;not null;default:'{}'" json:"labels"`

	// NodeVersion 节点 Register/Heartbeat 上报的程序版本（如 0.1.0-m1）。
	NodeVersion string `gorm:"size:64;not null;default:''" json:"node_version"`

	// endpoints
	ControlAdvertise string        `gorm:"size:255" json:"control_advertise"`
	WebRTCHosts      SfuStringList `gorm:"column:webrtc_hosts;type:jsonb;not null;default:'[]'" json:"webrtc_hosts"`
	// gRPC 控制通道 Register 自报的接入端点（proto NodeAdvertise，docs 协议 §3）。
	AdvertiseWssURL string        `gorm:"column:advertise_wss_url;size:255" json:"advertise_wss_url"`
	MediaUDPPort    int           `gorm:"column:media_udp_port;not null;default:0" json:"media_udp_port"`
	MediaIPs        SfuStringList `gorm:"column:media_ips;type:jsonb;not null;default:'[]'" json:"media_ips"`
	CascadeEndpoint string        `gorm:"size:255" json:"cascade_endpoint"`

	// capacity（心跳上报持久化快照；实时值以内存为准）
	MaxUsers         int         `gorm:"not null;default:0" json:"max_users"`
	CurrentUsers     int         `gorm:"not null;default:0" json:"current_users"`
	BandwidthOutMbps float64     `gorm:"column:bandwidth_out_mbps" json:"bandwidth_out_mbps"`
	CPUPct           float64     `gorm:"column:cpu_pct" json:"cpu_pct"`
	MemPct           float64     `gorm:"column:mem_pct" json:"mem_pct"`
	ScreenTracks     int         `gorm:"not null;default:0" json:"screen_tracks"`
	NodeRTTMs        SfuFloatMap `gorm:"column:node_rtt_ms;type:jsonb;not null;default:'{}'" json:"node_rtt_ms"`

	// 显式调度开关：未打开绝不调度（docs 03 §2.2 关键规则）。
	EnabledForScheduling bool `gorm:"not null;default:false" json:"enabled_for_scheduling"`
	// PlatformDefault 是否属于平台默认池（服级节点池为空时的回落目标，docs 07 专项 2.2）。
	PlatformDefault bool `gorm:"not null;default:false" json:"platform_default"`

	// enrollment token 只存 SHA-256 哈希（docs 03 §4.2）。
	EnrollmentTokenHash      string     `gorm:"size:64" json:"-"`
	EnrollmentTokenExpiresAt *time.Time `json:"-"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	EnrolledAt *time.Time `json:"enrolled_at,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// SfuGuildNodePool 服级节点池配置（每服一行；docs 07 专项 2）。
type SfuGuildNodePool struct {
	GuildID uuid.UUID `gorm:"type:uuid;primaryKey" json:"guild_id"`
	// FallbackToDefault 池为空时是否回落平台默认池（docs 07 专项 2.2，可关闭）。
	FallbackToDefault bool      `gorm:"not null;default:true" json:"fallback_to_default"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// SfuGuildNodeCandidate 系统管理员授权给某服的候选节点（docs 07 专项 2.1）。
type SfuGuildNodeCandidate struct {
	GuildID   uuid.UUID `gorm:"type:uuid;primaryKey;index:idx_sfupool_candidate_guild" json:"guild_id"`
	NodeID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"node_id"`
	CreatedAt time.Time `json:"created_at"`
}

// SfuGuildNodeSelection 服务器管理员从候选集中勾选的池成员。
type SfuGuildNodeSelection struct {
	GuildID   uuid.UUID `gorm:"type:uuid;primaryKey;index:idx_sfupool_selection_guild" json:"guild_id"`
	NodeID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"node_id"`
	CreatedAt time.Time `json:"created_at"`
}

func init() {
	Register(&SfuNode{}, &SfuGuildNodePool{}, &SfuGuildNodeCandidate{}, &SfuGuildNodeSelection{})
}
