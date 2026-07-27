package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// UUIDList jsonb 存储的 UUID 数组（消息提及字段用）。
// nil 与空切片一律序列化为 []，保证列值与 JSON 输出恒为数组。
type UUIDList []uuid.UUID

// Value 实现 driver.Valuer：落库为 jsonb 数组。
func (l UUIDList) Value() (driver.Value, error) {
	if l == nil {
		l = UUIDList{}
	}
	return json.Marshal(l)
}

// Scan 实现 sql.Scanner：从 jsonb 读回；NULL 读为为空数组。
func (l *UUIDList) Scan(value any) error {
	if value == nil {
		*l = UUIDList{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("UUIDList: 不支持的扫描类型 %T", value)
	}
	if len(raw) == 0 || string(raw) == "null" {
		*l = UUIDList{}
		return nil
	}
	return json.Unmarshal(raw, l)
}

// MarshalJSON 保证 nil 输出为 [] 而非 null（事件 payload 稳定为数组）。
func (l UUIDList) MarshalJSON() ([]byte, error) {
	if l == nil {
		l = UUIDList{}
	}
	return json.Marshal([]uuid.UUID(l))
}

// ================= 消息领域模型（docs 13 AP/AQ、07 专项 5A/5B）=================
// 索引统一使用 idx_message_* / idx_attachment_* 前缀，避免与其他领域模型撞名。

// MessageType 消息类型（AP.5）：首期仅普通消息与少量系统消息。
type MessageType string

const (
	MessageDefault MessageType = "DEFAULT"
	MessageSystem  MessageType = "SYSTEM"
	// MessageSticker 贴图消息（docs 17）：正文必须为空、恰一张 sticker，禁止附件/小表情混排。
	MessageSticker MessageType = "STICKER"
	// MessageSystemAdmin 系统管理员临场发言（adminpresence 文本频道）：
	// 客户端以金色皇冠头像 +「@ 系统超级管理员」徽章渲染，不依赖成员资料。
	MessageSystemAdmin MessageType = "SYSTEM_ADMIN"
	// 群组私信系统灰条（Server-16 BN.5）
	MessageSystemRecipientAdd      MessageType = "SYSTEM_RECIPIENT_ADD"
	MessageSystemRecipientRemove   MessageType = "SYSTEM_RECIPIENT_REMOVE"
	MessageSystemChannelNameChange MessageType = "SYSTEM_CHANNEL_NAME_CHANGE"
)

// Message 消息主表（AQ.1）。
//   - ID 为自实现雪花 ID（41bit 毫秒时间戳 + 10bit 机器位 + 12bit 序列），可按时间排序（AP.1）；
//   - 软删除用自管 deleted_at（AQ.3），不用 gorm.DeletedAt，便于审计侧仍可查询；
//   - content_tsv（tsvector 全文索引列）由 message 包在启动时以裸 DDL 追加，不进 gorm 模型；
//   - 附件经 attachments.message_id 反向关联，响应期组装 attachment 列表（AP 字段 attachment_ids 的存储实现）。
type Message struct {
	ID        int64       `gorm:"primaryKey;autoIncrement:false;index:idx_message_channel_cursor,priority:2" json:"id,string"`
	GuildID   uuid.UUID   `gorm:"type:uuid;not null;index:idx_message_guild" json:"guild_id"`
	ChannelID uuid.UUID   `gorm:"type:uuid;not null;index:idx_message_channel_cursor,priority:1;index:idx_message_nonce,priority:1" json:"channel_id"`
	AuthorID  uuid.UUID   `gorm:"type:uuid;not null;index:idx_message_author;index:idx_message_nonce,priority:2" json:"author_id"`
	Type      MessageType `gorm:"size:32;not null;default:'DEFAULT'" json:"type"`
	Content   string      `gorm:"type:text;not null;default:''" json:"content"`
	ReplyToID *int64      `json:"reply_to_id,string,omitempty"` // 单层引用回复（AP.6）
	EditCount int         `gorm:"not null;default:0" json:"edit_count"`
	EditedAt  *time.Time  `json:"edited_at,omitempty"`
	// Nonce 客户端幂等标识（AR.6）：同 channel+author+nonce 短窗口内重复提交返回原消息。
	Nonce string `gorm:"size:64;not null;default:'';index:idx_message_nonce,priority:3" json:"nonce,omitempty"`
	// 提及字段（docs 05 FR-19/FR-22、15 §7-2）：发消息/编辑时由服务端解析正文 wire format
	//（<@user_id> / <@&role_id> / @everyone / @here）后落库，MESSAGE_CREATE/UPDATE payload 直接携带。
	//   - Mentions 被提及且确为本服成员的用户 ID；
	//   - MentionRoles 被提及且确为本服角色（非 @everyone 角色）的角色 ID；
	//   - MentionEveryone 正文含 @everyone / @here 字面量且作者具备 MENTION_EVERYONE 权限。
	Mentions        UUIDList `gorm:"type:jsonb;not null;default:'[]'" json:"mentions"`
	MentionRoles    UUIDList `gorm:"type:jsonb;not null;default:'[]'" json:"mention_roles"`
	MentionEveryone bool     `gorm:"not null;default:false" json:"mention_everyone"`
	// Card 卡片消息载荷（bot 专项）：任意 JSON 对象（嵌入/按钮/字段等由客户端渲染），
	// NULL 表示无卡片。指针避免 GORM 把空字符串写进 jsonb 列。
	Card *string `gorm:"type:jsonb" json:"-"`
	// StreamStatus 流式消息状态（bot 专项）：'' 普通消息 / STREAMING 流式进行中。
	// 流式增量经 MESSAGE_STREAM_DELTA 事件下发，结束后本列清空并落最终正文。
	StreamStatus string `gorm:"size:16;not null;default:''" json:"stream_status,omitempty"`
	// StickerItems 贴图消息载荷（docs 17）：jsonb 数组，type=STICKER 时长度必须为 1。
	// 形如 [{"item_id":"...","pack_id":"...","mark":"...","animated":false}]。
	// 指针避免 GORM 把空字符串写进 jsonb 列；普通消息为 nil。
	StickerItems *string `gorm:"type:jsonb" json:"-"`
	// VisibleTo ephemeral 定向可见名单（bot 专项）：空数组 = 公开消息；
	// 非空 = 仅名单内用户 + 作者可见（持久化，历史拉取按 viewer 过滤，上限 20）。
	// 不建索引：读路径先经频道游标索引收敛，残余 jsonb 过滤开销可忽略。
	VisibleTo UUIDList `gorm:"type:jsonb;not null;default:'[]'" json:"visible_to,omitempty"`
	// VisibleRoleIDs 消息限定可见身份组（空 = 公开，频道 VIEW 即可）。
	// 非空时仅：作者 ∪ 持有任一指定角色的成员 ∪ 频道最终 MANAGE_MESSAGES ∪ 服主/系统管可见。
	VisibleRoleIDs UUIDList  `gorm:"type:jsonb;not null;default:'[]'" json:"visible_role_ids"`
	CreatedAt      time.Time `gorm:"index:idx_message_created" json:"created_at"`
	DeletedAt      *time.Time `gorm:"index:idx_message_deleted" json:"deleted_at,omitempty"`
}

// IsEphemeral 是否为定向可见（ephemeral）消息。
func (m Message) IsEphemeral() bool { return len(m.VisibleTo) > 0 }

// MessageEdit 编辑历史（AQ.4）：每次编辑保存编辑前正文的全文快照，version 从 1 递增，
// 与 messages.edit_count 保持一致（AQ.5）。
type MessageEdit struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MessageID int64     `gorm:"not null;uniqueIndex:idx_message_edit_version,priority:1" json:"message_id,string"`
	Version   int       `gorm:"not null;uniqueIndex:idx_message_edit_version,priority:2" json:"version"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	EditorID  uuid.UUID `gorm:"type:uuid;not null" json:"editor_id"`
	EditedAt  time.Time `gorm:"not null" json:"edited_at"`
}

// Attachment 附件元数据（AT）。二段式上传：
//  1. presign 创建记录（Uploaded=false，签发一次性 upload token）；
//  2. PUT content 写入存储后 Uploaded=true；
//  3. 发消息时绑定 MessageID；始终未绑定的记录由 GC 定期清理。
type Attachment struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID    uuid.UUID `gorm:"type:uuid;not null;index:idx_attachment_guild" json:"guild_id"`
	ChannelID  uuid.UUID `gorm:"type:uuid;not null;index:idx_attachment_channel" json:"channel_id"`
	UploaderID uuid.UUID `gorm:"type:uuid;not null;index:idx_attachment_uploader" json:"uploader_id"`
	MessageID  *int64    `gorm:"index:idx_attachment_message" json:"message_id,string,omitempty"`
	Filename   string    `gorm:"size:255;not null" json:"filename"`
	MIME       string    `gorm:"size:255;not null" json:"mime"`
	Size       int64     `gorm:"not null" json:"size"`
	// ObjectKey 存储层对象键；本地实现为 DataDir/attachments 下的相对路径，日后可平移到对象存储。
	ObjectKey string `gorm:"size:255;not null" json:"-"`
	// Width/Height 图片像素尺寸（Owl-Desktop docs 07 §8-5 UX-04 占位比例）：
	// 上传完成时服务端解码探测（PNG/JPEG/GIF），非图片或解码失败为 0。
	Width    int  `gorm:"not null;default:0" json:"width,omitempty"`
	Height   int  `gorm:"not null;default:0" json:"height,omitempty"`
	Uploaded bool `gorm:"not null;default:false" json:"uploaded"`
	// UploadTokenHash 一次性上传令牌的 SHA-256 十六进制；上传成功后置空。
	UploadTokenHash string    `gorm:"size:64;not null;default:''" json:"-"`
	UploadExpiresAt time.Time `gorm:"not null" json:"-"`
	CreatedAt       time.Time `gorm:"index:idx_attachment_created" json:"created_at"`
}

// MessageReaction 表情反应（AV）：emoji 为 unicode 字符串，唯一约束保证幂等。
type MessageReaction struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MessageID int64     `gorm:"not null;uniqueIndex:idx_message_reaction_unique,priority:1" json:"message_id,string"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_message_reaction_unique,priority:2" json:"user_id"`
	Emoji     string    `gorm:"size:64;not null;uniqueIndex:idx_message_reaction_unique,priority:3" json:"emoji"`
	CreatedAt time.Time `json:"created_at"`
}

// GuildMessageConfig 服级消息配置：
//   - UploadLimitBytes 单文件上限（AT.4），0 表示使用平台默认 25MB，仅系统管可调；
//   - RetentionDays 消息保留天数（AW），0 表示永久。
type GuildMessageConfig struct {
	GuildID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"guild_id"`
	UploadLimitBytes int64     `gorm:"not null;default:0" json:"upload_limit_bytes"`
	RetentionDays    int       `gorm:"not null;default:0" json:"retention_days"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// VoicePackScope 入场语音包可听范围（5A.2）。
type VoicePackScope string

const (
	VoicePackSameChannel VoicePackScope = "SAME_CHANNEL"
	VoicePackGuildOnline VoicePackScope = "GUILD_ONLINE"
)

// VoicePackTrigger 触发时机（5A.1）：默认进服首次出现；触发判定由语音模块执行。
type VoicePackTrigger string

const (
	VoicePackFirstJoin   VoicePackTrigger = "FIRST_GUILD_JOIN"
	VoicePackChannelJoin VoicePackTrigger = "CHANNEL_JOIN"
)

// GuildVoicePackConfig 服级入场语音包配置（5A，服管配置）。
type GuildVoicePackConfig struct {
	GuildID   uuid.UUID        `gorm:"type:uuid;primaryKey" json:"guild_id"`
	Enabled   bool             `gorm:"not null;default:false" json:"enabled"`
	AudioURL  string           `gorm:"size:1024;not null;default:''" json:"audio_url"`
	Scope     VoicePackScope   `gorm:"size:32;not null;default:'SAME_CHANNEL'" json:"scope"`
	Trigger   VoicePackTrigger `gorm:"size:32;not null;default:'FIRST_GUILD_JOIN'" json:"trigger"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// ChannelVoicePackConfig 频道级入场语音包开关（5A.1b，子频道管理员可配）。
// 无记录视为允许播放（跟随服级配置）。
type ChannelVoicePackConfig struct {
	ChannelID uuid.UUID `gorm:"type:uuid;primaryKey" json:"channel_id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;index:idx_message_vp_channel_guild" json:"guild_id"`
	Allowed   bool      `gorm:"not null;default:true" json:"allowed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func init() {
	Register(
		&Message{}, &MessageEdit{}, &Attachment{}, &MessageReaction{},
		&GuildMessageConfig{}, &GuildVoicePackConfig{}, &ChannelVoicePackConfig{},
	)
}
