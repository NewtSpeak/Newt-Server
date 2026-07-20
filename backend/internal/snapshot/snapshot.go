// Package snapshot 组装「按用户可见性过滤」的服务器全量快照与频道快照，
// 供 Gateway READY（docs 14 §7-2）、GUILD_CREATE / CHANNEL_CREATE 事件载荷复用。
// 权限计算统一走 internal/perms（不可见即不存在，docs 06 议题 8）。
package snapshot

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"gorm.io/gorm"
)

// VoiceConfig 语音频道配置快照（舞台模式 / 麦位，docs 11 §2.2；无记录时为默认值）。
type VoiceConfig struct {
	Mode                  string `json:"mode"`
	MaxSpeakers           int    `json:"max_speakers"`
	RequestToSpeakEnabled bool   `json:"request_to_speak_enabled"`
}

// defaultVoiceConfig StageChannelConfig 无记录时的默认值（docs 11 §2.2）。
func defaultVoiceConfig() *VoiceConfig {
	return &VoiceConfig{Mode: model.StageModeFree, MaxSpeakers: 20, RequestToSpeakEnabled: true}
}

// Channel 频道快照：实体（含类型/名称/topic/持久化 position/时间戳）+ 语音配置。
// 排序序号直接使用 model.Channel.Position（持久化，批量排序端点可改）。
type Channel struct {
	model.Channel
	VoiceConfig *VoiceConfig `json:"voice_config,omitempty"` // 仅 VOICE 频道携带
}

// Member 成员快照：实体 + 全量角色绑定。
type Member struct {
	model.Member
	RoleIDs []uuid.UUID `json:"role_ids"`
}

// Presence 成员在线状态条目（READY guilds[].presences 数组元素）。
// Status 已按接收者视角处理：他人 invisible 掩码为 offline 且省略（列表只含非 offline 成员）；
// 本人条目为真实合并状态（可为 invisible）。
type Presence struct {
	UserID     uuid.UUID `json:"user_id"`
	Status     string    `json:"status"`
	CustomText string    `json:"custom_text,omitempty"`
}

// Guild 单个服务器的全量快照（READY guilds 数组元素 / GUILD_CREATE 载荷主体）。
type Guild struct {
	Guild       model.Guild        `json:"guild"`
	Channels    []Channel          `json:"channels"` // 已按该用户 VIEW_CHANNEL 过滤
	Roles       []model.Role       `json:"roles"`    // 全量角色
	Member      Member             `json:"member"`   // 自身成员（含 role_ids、nickname）
	VoiceStates []model.VoiceState `json:"voice_states"`
	// Presences 该服在线成员的当前状态（gateway 在 READY 组装时填充；
	// GUILD_CREATE 等纯 DB 路径为空数组，客户端靠后续 PRESENCE_UPDATE 增量补齐）。
	Presences []Presence `json:"presences"`
}

// BuildGuild 组装某用户视角下一个服务器的全量快照。
// 非成员（且非系统管理员）返回 perms.ErrNotFound；频道列表按 VIEW_CHANNEL 过滤，
// voice_states 仅含可见频道内的语音状态。
func BuildGuild(db *gorm.DB, user model.User, guildID uuid.UUID) (Guild, error) {
	ctx, err := perms.LoadGuild(db, user, guildID)
	if err != nil {
		return Guild{}, err
	}
	visible, err := ctx.VisibleChannels(db)
	if err != nil {
		return Guild{}, err
	}
	channels := make([]Channel, 0, len(visible))
	channelIDs := make([]uuid.UUID, 0, len(visible))
	for _, channel := range visible {
		channels = append(channels, buildChannel(db, channel))
		channelIDs = append(channelIDs, channel.ID)
	}
	var roles []model.Role
	if err := db.Where("guild_id = ?", guildID).Order("position ASC, id ASC").Find(&roles).Error; err != nil {
		return Guild{}, err
	}
	member := Member{RoleIDs: []uuid.UUID{}}
	if ctx.Member != nil {
		member.Member = *ctx.Member
		if err := db.Model(&model.MemberRole{}).Where("member_id = ?", ctx.Member.ID).
			Pluck("role_id", &member.RoleIDs).Error; err != nil {
			return Guild{}, err
		}
		if member.RoleIDs == nil {
			member.RoleIDs = []uuid.UUID{}
		}
	}
	voiceStates := []model.VoiceState{}
	if len(channelIDs) > 0 {
		if err := db.Where("guild_id = ? AND channel_id IN ?", guildID, channelIDs).
			Order("joined_at ASC").Find(&voiceStates).Error; err != nil {
			return Guild{}, err
		}
	}
	return Guild{Guild: ctx.Guild, Channels: channels, Roles: roles, Member: member, VoiceStates: voiceStates, Presences: []Presence{}}, nil
}

// BuildGuilds 批量组装（READY 用）；期间失去可见性的服务器（并发被踢/删服）静默跳过。
func BuildGuilds(db *gorm.DB, user model.User, guildIDs []uuid.UUID) ([]Guild, error) {
	guilds := make([]Guild, 0, len(guildIDs))
	for _, guildID := range guildIDs {
		snapshot, err := BuildGuild(db, user, guildID)
		if errors.Is(err, perms.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		guilds = append(guilds, snapshot)
	}
	return guilds, nil
}

// buildChannel 组装单个频道快照（VOICE 频道附带舞台配置，无记录用默认值）。
func buildChannel(db *gorm.DB, channel model.Channel) Channel {
	result := Channel{Channel: channel}
	if channel.Type != model.ChannelVoice {
		return result
	}
	config := defaultVoiceConfig()
	var stored model.StageChannelConfig
	if err := db.First(&stored, "channel_id = ?", channel.ID).Error; err == nil {
		config = &VoiceConfig{
			Mode:                  stored.Mode,
			MaxSpeakers:           stored.MaxSpeakers,
			RequestToSpeakEnabled: stored.RequestToSpeakEnabled,
		}
	}
	result.VoiceConfig = config
	return result
}

// GuildCreatePayload GUILD_CREATE 载荷：建服 / 加入服务器时对当事人定向发送的全量快照。
type GuildCreatePayload struct {
	Guild
	EventAt time.Time `json:"event_at"`
}

// NewGuildCreatePayload 供建服与邀请加入路径复用（快照按当事人可见性组装）。
func NewGuildCreatePayload(db *gorm.DB, user model.User, guildID uuid.UUID) (GuildCreatePayload, error) {
	guild, err := BuildGuild(db, user, guildID)
	if err != nil {
		return GuildCreatePayload{}, err
	}
	return GuildCreatePayload{Guild: guild, EventAt: time.Now().UTC()}, nil
}

// ChannelPayload CHANNEL_CREATE / CHANNEL_UPDATE 载荷（频道快照 + 事件时间戳）。
type ChannelPayload struct {
	Channel
	EventAt time.Time `json:"event_at"`
}

// NewChannelPayload 供频道创建端点、覆盖变更可见性放宽路径与频道 PATCH 端点复用。
func NewChannelPayload(db *gorm.DB, channel model.Channel) ChannelPayload {
	return ChannelPayload{Channel: buildChannel(db, channel), EventAt: time.Now().UTC()}
}

// ReadState READY read_states 数组元素（docs 15 §7-1 / §8.1）：
// 该用户在某可见频道的已读位置与未读提及数。没有记录的频道不出现在列表中
//（客户端视为「从未读过 / 无提及」）。
type ReadState struct {
	ChannelID         uuid.UUID `json:"channel_id"`
	LastReadMessageID int64     `json:"last_read_message_id,string"`
	MentionCount      int       `json:"mention_count"`
}

// BuildReadStates 组装某用户的 READY read_states：只含给定（已按可见性过滤的）
// 频道集合内已落库的记录——禁看/不可见频道即使有存量记录也不下发（docs 15 US-8）。
func BuildReadStates(db *gorm.DB, userID uuid.UUID, channelIDs []uuid.UUID) ([]ReadState, error) {
	states := []ReadState{}
	if len(channelIDs) == 0 {
		return states, nil
	}
	var rows []model.ReadState
	if err := db.Where("user_id = ? AND channel_id IN ?", userID, channelIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		states = append(states, ReadState{
			ChannelID:         row.ChannelID,
			LastReadMessageID: row.LastReadMessageID,
			MentionCount:      row.MentionCount,
		})
	}
	return states, nil
}

// ChannelViewers 列出对某频道具备 VIEW_CHANNEL 的全部成员 user_id
//（权限覆盖变更前后各算一次即可得到可见性增减集合）。
func ChannelViewers(db *gorm.DB, guildID, channelID uuid.UUID) ([]uuid.UUID, error) {
	var users []model.User
	err := db.Raw(`SELECT users.* FROM users JOIN members ON members.user_id = users.id WHERE members.guild_id = ?`, guildID).
		Scan(&users).Error
	if err != nil {
		return nil, err
	}
	viewers := make([]uuid.UUID, 0, len(users))
	for _, user := range users {
		if perms.CanSeeChannel(db, user, guildID, channelID) {
			viewers = append(viewers, user.ID)
		}
	}
	return viewers, nil
}
