package model

import (
	"time"

	"github.com/google/uuid"
)

// VoiceAnchorLease 每逻辑语音房（logical_room_id = channel_id）一条 Anchor 租约
// （docs 08 §3.1）。epoch 单调递增，换根 / 重构图必须先升 epoch。
type VoiceAnchorLease struct {
	RoomID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"room_id"`
	GuildID        uuid.UUID `gorm:"type:uuid;not null;index:idx_voice_anchor_guild" json:"guild_id"`
	AnchorNodeID   uuid.UUID `gorm:"type:uuid;not null" json:"anchor_node_id"`
	Epoch          int64     `gorm:"not null;default:0" json:"epoch"`
	LeaseExpiresAt time.Time `gorm:"not null" json:"lease_expires_at"`
	// Degraded 检测到根故障但尚未完成切根时置位（docs 08 §7.1 完整切根属 M4；
	// 本期仅标记 + 日志，节点判死后由迁移引擎 reRoot 兜底恢复时清除）。
	Degraded  bool      `gorm:"not null;default:false" json:"degraded"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// VoiceCascadeEdge 级联边（docs 08 §4.2）。首期为纯星型：parent 恒为 anchor，深度 1；
// 二级 region hub（深度 2）预留字段结构不变，由 parent 指向 hub 即可。
// 星型下每个 child 在同一房间内只能有一个父节点。
type VoiceCascadeEdge struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	RoomID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_voice_cascade_room_child" json:"room_id"`
	ChildNodeID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_voice_cascade_room_child" json:"child_node_id"`
	ParentNodeID uuid.UUID `gorm:"type:uuid;not null" json:"parent_node_id"`
	Epoch        int64     `gorm:"not null" json:"epoch"`
	CreatedAt    time.Time `json:"created_at"`
}

// 迁移原因（docs 09 §3），优先级 死亡 > 分区 > Drain > 过载。
const (
	MigrationReasonDeath     = "DEATH"
	MigrationReasonPartition = "PARTITION"
	MigrationReasonDrain     = "DRAIN"
	MigrationReasonOverload  = "OVERLOAD"
	MigrationReasonManual    = "MANUAL"
)

// 迁移状态机状态（docs 09 §5.1 五段 + 终态）。
const (
	MigrationStateQueued   = "QUEUED"
	MigrationStatePrepare  = "MIGRATING_PREPARE"
	MigrationStateConnect  = "MIGRATING_CONNECT"
	MigrationStateCutover  = "MIGRATING_CUTOVER"
	MigrationStateCleanup  = "MIGRATING_CLEANUP"
	MigrationStateDone     = "DONE"
	MigrationStateFailed   = "MIGRATING_FAILED"
	MigrationStateCanceled = "CANCELED"
)

// VoiceMigrationJob 一次用户会话热迁移（docs 09 §5.4）。
// 同一 user_id + guild_id 同时只允许一个未终态 job（引擎创建时在互斥锁内校验）。
type VoiceMigrationJob struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	Reason     string     `gorm:"size:16;not null" json:"reason"`
	Priority   int        `gorm:"not null;default:0" json:"priority"`
	UserID     uuid.UUID  `gorm:"type:uuid;not null;index:idx_voice_migration_user_guild" json:"user_id"`
	GuildID    uuid.UUID  `gorm:"type:uuid;not null;index:idx_voice_migration_user_guild" json:"guild_id"`
	ChannelID  uuid.UUID  `gorm:"type:uuid;not null" json:"channel_id"` // = logical_room_id
	FromNodeID uuid.UUID  `gorm:"type:uuid;not null" json:"from_node_id"`
	ToNodeID   *uuid.UUID `gorm:"type:uuid" json:"to_node_id"`
	// FromSessionID 迁出侧旧会话 sid（PREPARE 时快照）：CLEANUP 阶段经
	// MigrateParticipants 按 sid 精确摘除，绝不误伤新会话（15 BJ.2/BJ.3）。
	FromSessionID *uuid.UUID `gorm:"type:uuid" json:"from_session_id"`
	// BatchKey 批量收敛键（from_node + room），MIGRATE_BATCH 同批尽量同目标（docs 10 §5.1）。
	BatchKey string `gorm:"size:80;index:idx_voice_migration_batch" json:"batch_key"`
	State    string `gorm:"size:24;not null;index:idx_voice_migration_state" json:"state"`
	Attempt  int    `gorm:"not null;default:0" json:"attempt"`
	// TriedNodes 逗号分隔的已尝试目标，重试换目标时排除（docs 09 K.3）。
	TriedNodes string `gorm:"size:800" json:"tried_nodes"`
	LastError  string `gorm:"size:400" json:"last_error"`
	// StateDeadline 当前阶段超时时刻；RetryAt 排队重试时刻。
	StateDeadline *time.Time `json:"state_deadline"`
	RetryAt       *time.Time `json:"retry_at"`
	ActorID       *uuid.UUID `gorm:"type:uuid" json:"actor_id"`
	ActorType     string     `gorm:"size:16;not null;default:'auto'" json:"actor_type"` // auto / system_admin / guild_admin
	PreparedAt    *time.Time `json:"prepared_at"`
	ConnectedAt   *time.Time `json:"connected_at"`
	CutoverAt     *time.Time `json:"cutover_at"`
	CompletedAt   *time.Time `json:"completed_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func init() {
	Register(&VoiceAnchorLease{}, &VoiceCascadeEdge{}, &VoiceMigrationJob{})
}
