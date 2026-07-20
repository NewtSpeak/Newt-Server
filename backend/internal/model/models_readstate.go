package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 未读与提及（docs 15 §3.1 / §7）=================

// ReadState 用户级频道已读状态：user_id + channel_id 唯一。
//   - LastReadMessageID 已读推进的唯一依据（雪花 ID 单调可比，只前进不后退，FR-01/FR-02）；
//   - MentionCount 该频道未读提及数：MESSAGE_CREATE 时服务端为被提及者递增，ack 时清零（FR-04）；
//     MESSAGE_DELETE 不回滚（对齐 Discord，客户端按 FR-05 自行扣减渲染，重同步以本表校正）；
//   - GuildID 冗余存储：READY 快照按可见频道过滤与 GET /users/@me/read-states?guild_id= 过滤用。
type ReadState struct {
	UserID            uuid.UUID `gorm:"type:uuid;primaryKey;index:idx_read_state_user_guild,priority:1" json:"user_id"`
	ChannelID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"channel_id"`
	GuildID           uuid.UUID `gorm:"type:uuid;not null;index:idx_read_state_user_guild,priority:2" json:"guild_id"`
	LastReadMessageID int64     `gorm:"not null;default:0" json:"last_read_message_id,string"`
	MentionCount      int       `gorm:"not null;default:0" json:"mention_count"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func init() {
	Register(&ReadState{})
}
