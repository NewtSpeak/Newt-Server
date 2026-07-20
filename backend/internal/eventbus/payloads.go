package eventbus

// 结构事件的载荷契约与构造函数（docs 14 §3.2 / FR-07）：
// 所有载荷均携带实体 ID 与 event_at 时间戳（UTC），供客户端做幂等去重
//（resume 补发重叠时以「实体 ID + 时间戳/版本」判重）。
// 需要按用户可见性组装快照的载荷（GUILD_CREATE / CHANNEL_CREATE / CHANNEL_UPDATE）
// 在 internal/snapshot 包中构造（依赖 perms 权限计算），本文件只放纯数据载荷。

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// eventNow 事件时间戳统一取 UTC。
func eventNow() time.Time { return time.Now().UTC() }

// GuildPayload GUILD_UPDATE 载荷（PATCH guild 端点由后续任务接入）。
type GuildPayload struct {
	Guild   model.Guild `json:"guild"`
	EventAt time.Time   `json:"event_at"`
}

// NewGuildUpdatePayload 供 guild PATCH 端点（后续任务）复用。
func NewGuildUpdatePayload(guild model.Guild) GuildPayload {
	return GuildPayload{Guild: guild, EventAt: eventNow()}
}

// GuildDeletePayload GUILD_DELETE 载荷（DELETE guild 端点由后续任务接入）。
type GuildDeletePayload struct {
	GuildID uuid.UUID `json:"guild_id"`
	EventAt time.Time `json:"event_at"`
}

// NewGuildDeletePayload 供 guild DELETE 端点（后续任务）复用。
func NewGuildDeletePayload(guildID uuid.UUID) GuildDeletePayload {
	return GuildDeletePayload{GuildID: guildID, EventAt: eventNow()}
}

// ChannelDeletePayload CHANNEL_DELETE 载荷：频道删除，以及权限覆盖变更导致
// 某用户失去可见性时的定向「频道消失」通知（docs 14 FR-15）。
type ChannelDeletePayload struct {
	GuildID   uuid.UUID `json:"guild_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	EventAt   time.Time `json:"event_at"`
}

// NewChannelDeletePayload 供频道 DELETE 端点（后续任务）与覆盖变更可见性收紧路径复用。
func NewChannelDeletePayload(guildID, channelID uuid.UUID) ChannelDeletePayload {
	return ChannelDeletePayload{GuildID: guildID, ChannelID: channelID, EventAt: eventNow()}
}

// GuildMemberAddPayload GUILD_MEMBER_ADD 载荷（成员实体 + 用户基本资料）。
type GuildMemberAddPayload struct {
	GuildID uuid.UUID    `json:"guild_id"`
	Member  model.Member `json:"member"`
	User    model.User   `json:"user"`
	EventAt time.Time    `json:"event_at"`
}

// NewGuildMemberAddPayload 供加入服务器（邀请 join / 建服）等路径复用。
func NewGuildMemberAddPayload(member model.Member, user model.User) GuildMemberAddPayload {
	return GuildMemberAddPayload{GuildID: member.GuildID, Member: member, User: user, EventAt: eventNow()}
}

// GuildMemberRemovePayload GUILD_MEMBER_REMOVE 载荷；Reason 取 kick / ban / leave。
type GuildMemberRemovePayload struct {
	GuildID  uuid.UUID `json:"guild_id"`
	MemberID uuid.UUID `json:"member_id"`
	UserID   uuid.UUID `json:"user_id"`
	Reason   string    `json:"reason"`
	EventAt  time.Time `json:"event_at"`
}

// NewGuildMemberRemovePayload 供踢出 / 封禁 / 主动退出（后续任务）路径复用。
func NewGuildMemberRemovePayload(member model.Member, reason string) GuildMemberRemovePayload {
	return GuildMemberRemovePayload{
		GuildID: member.GuildID, MemberID: member.ID, UserID: member.UserID,
		Reason: reason, EventAt: eventNow(),
	}
}

// GuildMemberUpdatePayload GUILD_MEMBER_UPDATE 载荷（昵称 / 角色绑定变化，含全量 role_ids）。
type GuildMemberUpdatePayload struct {
	GuildID uuid.UUID    `json:"guild_id"`
	Member  model.Member `json:"member"`
	RoleIDs []uuid.UUID  `json:"role_ids"`
	EventAt time.Time    `json:"event_at"`
}

// NewGuildMemberUpdatePayload 供角色绑定 / 解绑、昵称修改（后续任务）等路径复用。
func NewGuildMemberUpdatePayload(member model.Member, roleIDs []uuid.UUID) GuildMemberUpdatePayload {
	if roleIDs == nil {
		roleIDs = []uuid.UUID{}
	}
	return GuildMemberUpdatePayload{GuildID: member.GuildID, Member: member, RoleIDs: roleIDs, EventAt: eventNow()}
}

// GuildRolePayload GUILD_ROLE_CREATE / GUILD_ROLE_UPDATE 载荷。
type GuildRolePayload struct {
	GuildID uuid.UUID  `json:"guild_id"`
	Role    model.Role `json:"role"`
	EventAt time.Time  `json:"event_at"`
}

// NewGuildRolePayload 供角色创建 / 更新端点复用。
func NewGuildRolePayload(role model.Role) GuildRolePayload {
	return GuildRolePayload{GuildID: role.GuildID, Role: role, EventAt: eventNow()}
}

// GuildRoleDeletePayload GUILD_ROLE_DELETE 载荷（角色 DELETE 端点由后续任务接入）。
type GuildRoleDeletePayload struct {
	GuildID uuid.UUID `json:"guild_id"`
	RoleID  uuid.UUID `json:"role_id"`
	EventAt time.Time `json:"event_at"`
}

// NewGuildRoleDeletePayload 供角色 DELETE 端点（后续任务）复用。
func NewGuildRoleDeletePayload(guildID, roleID uuid.UUID) GuildRoleDeletePayload {
	return GuildRoleDeletePayload{GuildID: guildID, RoleID: roleID, EventAt: eventNow()}
}

// PermissionsUpdatePayload PERMISSIONS_UPDATE 载荷（频道权限覆盖 upsert 触发）。
// 事件按 guild 广播（不带 eventbus ChannelID 过滤——失去可见性的用户也必须收到）；
// 客户端收到后应立即失效本地权限投影并重拉该服频道列表（docs 14 FR-14）；
// 因覆盖变更「获得/失去可见性」的用户会额外收到定向的 CHANNEL_CREATE / CHANNEL_DELETE。
type PermissionsUpdatePayload struct {
	GuildID   uuid.UUID `json:"guild_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	EventAt   time.Time `json:"event_at"`
}

// NewPermissionsUpdatePayload 供权限覆盖 upsert（及后续覆盖 DELETE 端点）复用。
func NewPermissionsUpdatePayload(guildID, channelID uuid.UUID) PermissionsUpdatePayload {
	return PermissionsUpdatePayload{GuildID: guildID, ChannelID: channelID, EventAt: eventNow()}
}

// UserUpdatePayload USER_UPDATE 载荷（用户资料公开投影）：display_name / bio /
// 头像变更时广播给共享 guild 的在线成员并定向发给本人全部端。
// 不含 email / system_admin 等私有字段。
type UserUpdatePayload struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Avatar      string    `json:"avatar"` // 头像可访问 URL（/public-assets/profile/...），空串表示未设置
	Bio         string    `json:"bio"`
	EventAt     time.Time `json:"event_at"`
}

// NewUserUpdatePayload 供 userapi 资料/头像端点复用。
func NewUserUpdatePayload(user model.User) UserUpdatePayload {
	return UserUpdatePayload{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
		Avatar: user.AvatarURL, Bio: user.Bio, EventAt: eventNow(),
	}
}

// PresenceUpdatePayload PRESENCE_UPDATE 载荷。
// Status 取 online / idle / dnd / invisible / offline；发给他人的载荷绝不出现
// invisible（服务端已掩码为 offline），仅本人的定向载荷携带真实 invisible。
// CustomText 自定义状态文本（预留字段，docs 01 FR-23）。
type PresenceUpdatePayload struct {
	UserID     uuid.UUID `json:"user_id"`
	Status     string    `json:"status"`
	CustomText string    `json:"custom_text,omitempty"`
	EventAt    time.Time `json:"event_at"`
}

// NewPresenceUpdatePayload 供 internal/presence 复用。
func NewPresenceUpdatePayload(userID uuid.UUID, status, customText string) PresenceUpdatePayload {
	return PresenceUpdatePayload{UserID: userID, Status: status, CustomText: customText, EventAt: eventNow()}
}

// UserSettingsUpdatePayload USER_SETTINGS_UPDATE 载荷：合并后的全量设置文档
//（其他端收到后整体替换本地副本，无需增量合并）。
type UserSettingsUpdatePayload struct {
	Settings json.RawMessage `json:"settings"`
	EventAt  time.Time       `json:"event_at"`
}

// NewUserSettingsUpdatePayload 供 userapi 设置端点复用。
func NewUserSettingsUpdatePayload(settings json.RawMessage) UserSettingsUpdatePayload {
	return UserSettingsUpdatePayload{Settings: settings, EventAt: eventNow()}
}

// VoiceChannelStatusPayload VOICE_CHANNEL_STATUS 载荷（语音频道人数 / 模式角标，docs 14 §3.2）。
type VoiceChannelStatusPayload struct {
	GuildID   uuid.UUID `json:"guild_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	UserCount int       `json:"user_count"`
	Mode      string    `json:"mode"` // FREE_DISCUSSION / STAGE（docs 11 §2.1）
	EventAt   time.Time `json:"event_at"`
}

// NewVoiceChannelStatusPayload 供语音进出房与舞台模式切换等路径复用。
func NewVoiceChannelStatusPayload(guildID, channelID uuid.UUID, userCount int, mode string) VoiceChannelStatusPayload {
	return VoiceChannelStatusPayload{
		GuildID: guildID, ChannelID: channelID, UserCount: userCount, Mode: mode, EventAt: eventNow(),
	}
}
