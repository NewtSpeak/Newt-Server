package model

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Username     string    `gorm:"size:32;uniqueIndex;not null" json:"username"`
	Email        string    `gorm:"size:255;uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"not null" json:"-"`
	SystemAdmin  bool      `gorm:"not null;default:false" json:"system_admin"`
	// IsBot 标记该账号为机器人（bot 复用 User/Member/Role 权限体系，但不走密码登录、
	// 走独立 bot token；音频流中带独立标记）。由 bot 专项写入。
	IsBot bool `gorm:"not null;default:false;index" json:"is_bot"`
	// 用户资料（Owl-Desktop docs 01 §3.3）：显示名（1–32 字符，展示优先级高于用户名）、
	// 个性签名（≤190 字符，对标 Discord About Me）。空显示名表示未设置（回退用户名）。
	DisplayName string `gorm:"size:32;not null;default:''" json:"display_name"`
	Bio         string `gorm:"size:190;not null;default:''" json:"bio"`
	// 个人资料自定义（customization 专项使用）：头像/横幅 URL 与动态头像标记、个人强调色。
	AvatarURL      string `gorm:"size:512;not null;default:''" json:"avatar_url"`
	AvatarAnimated bool   `gorm:"not null;default:false" json:"avatar_animated"`
	BannerURL      string `gorm:"size:512;not null;default:''" json:"banner_url"`
	AccentColor    string `gorm:"size:32;not null;default:''" json:"accent_color"`
	// DisabledAt 平台级禁用时间（platformadmin 专项）：非空表示账号被系统管理员禁用，
	// 两个认证平面（后台/用户端）的登录、refresh、requireAuth 均拒绝；禁用时吊销全部会话。
	DisabledAt *time.Time `gorm:"index" json:"disabled_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Disabled 账号是否处于平台禁用状态。
func (u User) Disabled() bool { return u.DisabledAt != nil }

type RefreshToken struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	TokenHash string    `gorm:"size:64;uniqueIndex;not null"`
	// Audience 令牌受众（admin=后台管理 / client=用户端），与 JWT aud claim 对齐。
	// 历史数据（该列上线前签发的 refresh token）均来自后台登录，AutoMigrate 加列时
	// 靠 default:'admin' 回填，不破坏现有数据。
	Audience  string     `gorm:"size:16;not null;default:'admin'"`
	// SessionID 登录会话链 ID：登录时生成，refresh 轮换后的新 token 继承同一值，
	// 使「一次登录」在多次轮换后仍可作为单个会话被列出/吊销（docs 01 FR-27/FR-28）。
	// access token 的 sid claim 与此对应。历史数据靠 gen_random_uuid() 回填（每行独立会话）。
	SessionID uuid.UUID `gorm:"type:uuid;not null;default:gen_random_uuid();index"`
	// SessionCreatedAt 会话链首次登录时间（轮换时继承）；行自身的 CreatedAt 即该会话
	// 最近一次签发/轮换时间，会话列表以此作为「最近使用」展示。
	SessionCreatedAt time.Time  `gorm:"not null;default:now()"`
	ExpiresAt        time.Time  `gorm:"not null;index"`
	RevokedAt        *time.Time `gorm:"index"`
	CreatedAt        time.Time
}

// UserSettings 服务端存储的用户设置 JSON 文档（Owl-Desktop docs 16 §7-1）。
// 服务端不理解业务字段（通知偏好三层结构等由客户端约定），只做 JSON 合法性
// 与大小上限校验；PATCH 按 top-level key 合并（见 userapi）。
type UserSettings struct {
	UserID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	Data      string    `gorm:"type:jsonb;not null;default:'{}'" json:"data"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Guild struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Name        string    `gorm:"size:100;not null" json:"name"`
	// Description 服务器简介（设置页概览可编辑，Owl-Desktop docs 02 FR-13）。
	Description string    `gorm:"size:1024;not null;default:''" json:"description"`
	OwnerUserID uuid.UUID `gorm:"type:uuid;not null;index" json:"owner_user_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Member struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_member_guild_user" json:"guild_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_member_guild_user" json:"user_id"`
	Nickname  string    `gorm:"size:64" json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
}

type Role struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_role_guild_name" json:"guild_id"`
	Name        string    `gorm:"size:100;not null;uniqueIndex:idx_role_guild_name" json:"name"`
	Permissions int64     `gorm:"not null;default:0" json:"permissions"`
	Position    int       `gorm:"not null;default:0" json:"position"`
	IsEveryone  bool      `gorm:"not null;default:false" json:"is_everyone"`
	// Style 角色名样式（customization 专项定义 schema）：纯色/线性/多色/径向渐变等，
	// 存为 JSON 字符串；空对象表示无自定义样式。前端按此渲染用户名。
	Style     string    `gorm:"type:jsonb;not null;default:'{}'" json:"style"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MemberRole struct {
	MemberID uuid.UUID `gorm:"type:uuid;primaryKey"`
	RoleID   uuid.UUID `gorm:"type:uuid;primaryKey"`
}

type ChannelType string

const (
	ChannelText  ChannelType = "TEXT"
	ChannelVoice ChannelType = "VOICE"
)

type Channel struct {
	ID      uuid.UUID   `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID uuid.UUID   `gorm:"type:uuid;not null;index" json:"guild_id"`
	Name    string      `gorm:"size:100;not null" json:"name"`
	Type    ChannelType `gorm:"size:16;not null" json:"type"`
	// Topic 文本频道主题（≤1024 字符，Owl-Desktop docs 03 FR-10）。
	Topic string `gorm:"size:1024;not null;default:''" json:"topic"`
	// Position 频道排序序号（拖拽批量排序，Owl-Desktop docs 03 FR-12）；
	// 历史数据默认 0，列表排序按 position ASC, created_at ASC 兜底稳定。
	Position  int       `gorm:"not null;default:0" json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type OverwriteType string

const (
	OverwriteRole   OverwriteType = "ROLE"
	OverwriteMember OverwriteType = "MEMBER"
)

type ChannelOverwrite struct {
	ID        uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	ChannelID uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_overwrite_target" json:"channel_id"`
	Type      OverwriteType `gorm:"size:16;not null;uniqueIndex:idx_overwrite_target" json:"type"`
	TargetID  uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_overwrite_target" json:"target_id"`
	Allow     int64         `gorm:"not null;default:0" json:"allow"`
	Deny      int64         `gorm:"not null;default:0" json:"deny"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func init() {
	Register(&User{}, &RefreshToken{}, &UserSettings{}, &Guild{}, &Member{}, &Role{}, &MemberRole{}, &Channel{}, &ChannelOverwrite{})
}
