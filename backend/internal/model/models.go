package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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
	Audience string `gorm:"size:16;not null;default:'admin'"`
	// SessionID 登录会话链 ID：登录时生成，refresh 轮换后的新 token 继承同一值，
	// 使「一次登录」在多次轮换后仍可作为单个会话被列出/吊销（docs 01 FR-27/FR-28）。
	// access token 的 sid claim 与此对应。历史数据靠 gen_random_uuid() 回填（每行独立会话）。
	SessionID uuid.UUID `gorm:"type:uuid;not null;default:gen_random_uuid();index"`
	// SessionCreatedAt 会话链首次登录时间（轮换时继承）；行自身的 CreatedAt 即该会话
	// 最近一次签发/轮换时间，会话列表以此作为「最近使用」展示。
	SessionCreatedAt time.Time `gorm:"not null;default:now()"`
	// 会话设备元数据（Owl-Desktop docs 01 FR-27）：登录时从 User-Agent/RemoteAddr 采集，
	// refresh 轮换时继承；会话列表据此展示「设备 · 平台 · IP」。历史数据为空串。
	DeviceName string     `gorm:"size:128;not null;default:''"`
	Platform   string     `gorm:"size:32;not null;default:''"`
	IPAddress  string     `gorm:"size:64;not null;default:''"`
	ExpiresAt  time.Time  `gorm:"not null;index"`
	RevokedAt  *time.Time `gorm:"index"`
	CreatedAt  time.Time
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
	ID   uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Name string    `gorm:"size:100;not null" json:"name"`
	// Description 服务器简介（设置页概览可编辑，Owl-Desktop docs 02 FR-13）。
	Description string    `gorm:"size:1024;not null;default:''" json:"description"`
	OwnerUserID uuid.UUID `gorm:"type:uuid;not null;index" json:"owner_user_id"`
	// IconURL / BannerURL 服务器图标与横幅（Owl-Desktop docs 02 FR-13/§8-9）：
	// 走 /public-assets/profile 公开路径（与用户头像同存储约定），空串表示未设置。
	IconURL   string `gorm:"size:512;not null;default:''" json:"icon_url"`
	BannerURL string `gorm:"size:512;not null;default:''" json:"banner_url"`
	// RestrictionBadgeVisible 「受限徽章」服级开关（Owl-Desktop docs 08 AM.4/§8-6）：
	// 关闭后客户端不在成员列表渲染受限标识（服务端仍照常下发脱敏 RESTRICTION 事件）。
	RestrictionBadgeVisible bool `gorm:"not null;default:true" json:"restriction_badge_visible"`
	// RestrictionReasonRequired Restriction 创建是否强制填写 reason
	//（Owl-Desktop docs 08 AI.2/§8-9：系统管可按服配置，默认强制）。
	RestrictionReasonRequired bool      `gorm:"not null;default:true" json:"restriction_reason_required"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type Member struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_member_guild_user" json:"guild_id"`
	UserID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_member_guild_user" json:"user_id"`
	Nickname string    `gorm:"size:64" json:"nickname"`
	// NameStyleRoleID 本人选用的用户名样式来源角色（必须是自己持有的角色，含 @everyone）；
	// 为空则自动取「持有角色中 position 最高且配置了 style」的角色。
	// 仅影响用户名/徽章样式展示，不改变角色绑定。
	NameStyleRoleID *uuid.UUID `gorm:"type:uuid" json:"name_style_role_id"`
	CreatedAt       time.Time  `json:"created_at"`
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
	Style string `gorm:"type:jsonb;not null;default:'{}'" json:"style"`
	// Color 角色主色（Owl-Desktop docs 04 §8：#RRGGBB 十六进制，空串=默认色）；
	// 与 Style 并存：Color 为基础色（成员列表分组/用户名着色），Style 为高级渐变样式。
	Color string `gorm:"size:16;not null;default:''" json:"color"`
	// Hoist 是否在成员列表单独分组显示（Owl-Desktop docs 02 FR-22 / 04 §8）。
	Hoist bool `gorm:"not null;default:false" json:"hoist"`
	// Mentionable 是否允许任何人 @提及该角色（Owl-Desktop docs 04 §8）。
	Mentionable bool `gorm:"not null;default:false" json:"mentionable"`
	// Managed 内置角色标记（建服自动创建的「管理员」角色，见 internal/guildseed）：
	// 不可删除、permissions 锁定；客户端据此渲染锁定态（锁图标、禁用删除按钮）。
	Managed   bool      `gorm:"not null;default:false" json:"managed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MemberRole struct {
	MemberID uuid.UUID `gorm:"type:uuid;primaryKey"`
	RoleID   uuid.UUID `gorm:"type:uuid;primaryKey"`
}

type ChannelType string

const (
	ChannelText     ChannelType = "TEXT"
	ChannelVoice    ChannelType = "VOICE"
	ChannelCategory ChannelType = "CATEGORY"
	// ChannelDM / ChannelGroupDM 为私信类型（Server-16 BN）：
	// GuildID 固定为 uuid.Nil（零 UUID，非 NULL，避免大面积 nullable 改造）。
	ChannelDM      ChannelType = "DM"
	ChannelGroupDM ChannelType = "GROUP_DM"
)

// IsPrivate 是否为私信域频道（无 guild RBAC）。
func (t ChannelType) IsPrivate() bool {
	return t == ChannelDM || t == ChannelGroupDM
}

type Channel struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	// GuildID 服内频道为真实服 ID；DM/GROUP_DM 为 uuid.Nil（JSON 常输出为 0000…）。
	GuildID uuid.UUID   `gorm:"type:uuid;not null;index" json:"guild_id"`
	Name    string      `gorm:"size:100;not null" json:"name"`
	Type    ChannelType `gorm:"size:16;not null" json:"type"`
	// Topic 文本频道主题（≤1024 字符，Owl-Desktop docs 03 FR-10）。
	Topic string `gorm:"size:1024;not null;default:''" json:"topic"`
	// Position 频道排序序号（拖拽批量排序，Owl-Desktop docs 03 FR-12）；
	// 历史数据默认 0，列表排序按 position ASC, created_at ASC 兜底稳定。
	Position int `gorm:"not null;default:0" json:"position"`
	// ParentID 所属分类频道 ID（Owl-Desktop docs 03 FR-01/FR-03/FR-13）：
	// 仅 TEXT/VOICE 频道可设，指向本服 CATEGORY 频道；分类被删除时子频道上浮（置空）。
	ParentID *uuid.UUID `gorm:"type:uuid;index" json:"parent_id"`
	// UserLimit 语音频道人数上限（Owl-Desktop docs 09 FR-40）：0=不限（仍受全局硬顶
	// 200 约束）；1–99 为频道级可配上限，满员后普通用户 join 拒绝 CHANNEL_FULL。
	UserLimit int `gorm:"not null;default:0" json:"user_limit"`
	// RateLimitPerUser 文本频道慢速模式（Owl-Desktop docs 03 §8-9/05 FR-08）：
	// 每用户两条消息之间的最小间隔秒数，0=关闭；上限 21600（6 小时，对标 Discord）。
	RateLimitPerUser int `gorm:"not null;default:0" json:"rate_limit_per_user"`
	// RateLimitExemptRoleIDs 慢速模式豁免角色；为空表示慢速模式对所有成员生效。
	// 角色必须属于当前服务器；配置 @everyone 角色时全体成员豁免。
	RateLimitExemptRoleIDs UUIDList `gorm:"type:jsonb;not null;default:'[]'" json:"rate_limit_exempt_role_ids"`
	// PasswordHash 频道访问密码（argon2）；空串表示未上锁。永不序列化到 JSON。
	// 上锁后对 TEXT/VOICE 均生效：可见（VIEW_CHANNEL）但访问内容需先解锁。
	PasswordHash string `gorm:"size:255;not null;default:''" json:"-"`
	// Locked 是否已上锁（不入库；AfterFind / SyncLocked 根据 PasswordHash 填充）。
	Locked bool `gorm:"-" json:"locked"`
	// VoiceNote 语音频道活动注释（管理员手写，列表在线成员区顶部展示，≤200 字符）。
	// 仅 VOICE 有意义；TEXT/CATEGORY 恒为空串。
	VoiceNote string    `gorm:"size:200;not null;default:''" json:"voice_note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AfterFind 填充 Locked 公开标志（密码哈希本身不暴露）。
func (c *Channel) AfterFind(tx *gorm.DB) error {
	c.Locked = c.PasswordHash != ""
	return nil
}

// SyncLocked 在 Create/Updates 路径手动刷新 Locked（AfterFind 不触发时）。
func (c *Channel) SyncLocked() {
	c.Locked = c.PasswordHash != ""
}

// ChannelUnlock 用户对上锁频道的解锁记录（输入正确密码后写入；改密/关锁时清空）。
type ChannelUnlock struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	ChannelID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_channel_unlock_user" json:"channel_id"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_channel_unlock_user;index" json:"user_id"`
	UnlockedAt time.Time `gorm:"not null" json:"unlocked_at"`
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
	Register(&User{}, &RefreshToken{}, &UserSettings{}, &Guild{}, &Member{}, &Role{}, &MemberRole{}, &Channel{}, &ChannelOverwrite{}, &ChannelUnlock{})
}
