package model

import (
	"time"

	"github.com/google/uuid"
)

// PrivacySettings 账号级隐私裁决字段（Server-16 BM.1）。
// 缺省行不存在时服务端按安全默认值裁决：mutual_guilds / friends / 请求箱开。
type PrivacySettings struct {
	UserID                   uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	FriendRequestFrom        string    `gorm:"size:32;not null;default:'mutual_guilds'" json:"friend_request_from"`
	DmFrom                   string    `gorm:"size:32;not null;default:'friends'" json:"dm_from"`
	MessageRequestFilter     bool      `gorm:"not null;default:true" json:"message_request_filter"`
	ShowMutualGuilds         bool      `gorm:"not null;default:true" json:"show_mutual_guilds"`
	PublicProfileToNonFriends bool     `gorm:"not null;default:true" json:"public_profile_to_non_friends"`
	// ShowActivityTo 活动可见范围（Server-18）：everyone | friends | nobody；空串=默认 friends。
	ShowActivityTo string    `gorm:"size:32;not null;default:'friends'" json:"show_activity_to"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// GuildMemberPrivacy 每服私信覆盖（Server-16 BM.2）：allow_dm 默认 true，仅 false 时强制落库。
type GuildMemberPrivacy struct {
	UserID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	GuildID   uuid.UUID `gorm:"type:uuid;primaryKey;index" json:"guild_id"`
	AllowDM   bool      `gorm:"not null;default:true" json:"allow_dm"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Relationship 单行投影关系（Server-16 BK.1）：
// friend 双行；pending_outgoing 仅发起方一行；blocked 仅屏蔽方一行。
type Relationship struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_rel_pair;index:idx_rel_user_type" json:"user_id"`
	TargetUserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_rel_pair;index:idx_rel_target" json:"target_user_id"`
	// Type: friend | pending_outgoing | blocked
	Type      string    `gorm:"size:32;not null;index:idx_rel_user_type" json:"type"`
	Nickname  string    `gorm:"size:32;not null;default:''" json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Notification 站内通知收件箱（Server-16 BQ.1）。
type Notification struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index:idx_notif_user_created" json:"user_id"`
	// Type: FRIEND_REQUEST | FRIEND_ACCEPT | GUILD_MODERATION | SYSTEM_ANNOUNCE | ACCOUNT_SECURITY
	Type      string     `gorm:"size:32;not null" json:"type"`
	Payload   string     `gorm:"type:jsonb;not null;default:'{}'" json:"payload"`
	CreatedAt time.Time  `gorm:"index:idx_notif_user_created" json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

// NotificationAck 水位线已读（Server-16 BR.4）：记录用户最后 ack 的通知 id 时间。
type NotificationAck struct {
	UserID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	LastReadID     uuid.UUID `gorm:"type:uuid;not null" json:"last_read_id"`
	LastReadAt     time.Time `gorm:"not null" json:"last_read_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ChannelRecipient 私信/群组私信参与者（Server-16 BN.1）。
// 可见性 = 在本表中且 hidden=false（列表）；消息读写仍校验 membership（hidden 亦可重开）。
type ChannelRecipient struct {
	ChannelID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"channel_id"`
	UserID           uuid.UUID  `gorm:"type:uuid;primaryKey;index" json:"user_id"`
	JoinedAt         time.Time  `json:"joined_at"`
	Hidden           bool       `gorm:"not null;default:false" json:"hidden"`
	LastReadMessageID *int64    `json:"last_read_message_id,string,omitempty"`
	MessageRequest   bool       `gorm:"not null;default:false" json:"message_request"`
	Muted            bool       `gorm:"not null;default:false" json:"muted"`
	MutedUntil       *time.Time `json:"muted_until"`
}

const (
	RelationshipFriend           = "friend"
	RelationshipPendingOutgoing  = "pending_outgoing"
	RelationshipBlocked          = "blocked"

	// 投影类型（仅响应/事件，不落库）
	RelationshipPendingIncoming = "pending_incoming"

	FriendRequestEveryone      = "everyone"
	FriendRequestMutualFriends = "mutual_friends"
	FriendRequestMutualGuilds  = "mutual_guilds"
	FriendRequestNobody        = "nobody"

	DmFromEveryone     = "everyone"
	DmFromFriends      = "friends"
	DmFromMutualGuilds = "mutual_guilds"
	DmFromNobody       = "nobody"

	ShowActivityEveryone = "everyone"
	ShowActivityFriends  = "friends"
	ShowActivityNobody   = "nobody"

	NotificationFriendRequest   = "FRIEND_REQUEST"
	NotificationFriendAccept    = "FRIEND_ACCEPT"
	NotificationGuildModeration = "GUILD_MODERATION"
	NotificationSystemAnnounce  = "SYSTEM_ANNOUNCE"
	NotificationAccountSecurity = "ACCOUNT_SECURITY"
)

func init() {
	Register(
		&PrivacySettings{},
		&GuildMemberPrivacy{},
		&Relationship{},
		&Notification{},
		&NotificationAck{},
		&ChannelRecipient{},
	)
}
