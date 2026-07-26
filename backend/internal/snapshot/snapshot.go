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
	// LastMessageID 该频道当前最大消息雪花 ID（字符串形态，无消息为 "0"）。
	// 客户端与 read_states 的 last_read_message_id 比较即可恢复「普通未读」白点
	//（docs 15 FR-01：last_message_id > last_read_message_id 即有未读）。
	// 不排除软删消息——该 ID 只是读位置游标，与 ack 语义一致（docs 15 §3.1）。
	LastMessageID int64 `json:"last_message_id,string"`
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
	UserID          uuid.UUID  `json:"user_id"`
	Status          string     `json:"status"`
	CustomText      string     `json:"custom_text,omitempty"`
	CustomEmoji     string     `json:"custom_emoji,omitempty"`
	CustomExpiresAt *time.Time `json:"custom_expires_at,omitempty"`
}

// Guild 单个服务器的全量快照（READY guilds 数组元素 / GUILD_CREATE 载荷主体）。
// Guild 实体含 default_channel_id（默认着陆文字频道，可空），随 model.Guild 一并下发。
type Guild struct {
	Guild    model.Guild  `json:"guild"`
	Channels []Channel    `json:"channels"` // 已按该用户 VIEW_CHANNEL 过滤
	Roles    []model.Role `json:"roles"`    // 全量角色
	// Banners 服务器多 banner 列表（position 升序）：断线重连错过 GUILD_UPDATE 事件的
	// 客户端从快照获得基线，无需另行 REST 拉取。
	Banners     []model.GuildBanner `json:"banners"`
	Member      Member              `json:"member"` // 自身成员（含 role_ids、nickname）
	VoiceStates []model.VoiceState  `json:"voice_states"`
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
	// 每频道最新消息 ID：一条聚合 SQL 批量取 MAX（避免逐频道 N+1）。
	lastMessageIDs, err := ChannelLastMessageIDs(db, channelIDs)
	if err != nil {
		return Guild{}, err
	}
	for i := range channels {
		channels[i].LastMessageID = lastMessageIDs[channels[i].ID]
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
	banners := []model.GuildBanner{}
	if err := db.Where("guild_id = ?", guildID).Order("position ASC, created_at ASC").Find(&banners).Error; err != nil {
		return Guild{}, err
	}
	return Guild{
		Guild: ctx.Guild, Channels: channels, Roles: roles, Banners: banners,
		Member: member, VoiceStates: voiceStates, Presences: []Presence{},
	}, nil
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

// ChannelLastMessageIDs 批量取各频道当前最大消息雪花 ID（单条聚合 SQL）。
// 无消息的频道不出现在结果中（调用方经 map 零值自然得到 0）。
// ephemeral（visible_to 非空）消息不参与：它对非目标用户不可见，计入会产生
// 指向「看不见的消息」的幽灵未读白点（设计文档 2026-07-26，对齐 Discord 不计未读）。
func ChannelLastMessageIDs(db *gorm.DB, channelIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	result := make(map[uuid.UUID]int64, len(channelIDs))
	if len(channelIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ChannelID uuid.UUID
		MaxID     int64
	}
	err := db.Model(&model.Message{}).Select("channel_id, MAX(id) AS max_id").
		Where("channel_id IN ? AND visible_to = '[]'::jsonb", channelIDs).Group("channel_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ChannelID] = row.MaxID
	}
	return result, nil
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
// last_message_id 单频道查询补齐（CHANNEL_UPDATE 载荷若恒为 "0" 会让客户端
// 整体替换本地频道对象时误清未读游标）。
func NewChannelPayload(db *gorm.DB, channel model.Channel) ChannelPayload {
	view := buildChannel(db, channel)
	if lastMessageIDs, err := ChannelLastMessageIDs(db, []uuid.UUID{channel.ID}); err == nil {
		view.LastMessageID = lastMessageIDs[channel.ID]
	}
	return ChannelPayload{Channel: view, EventAt: time.Now().UTC()}
}

// ReadState READY read_states 数组元素（docs 15 §7-1 / §8.1）：
// 该用户在某可见频道的已读位置、未读提及数和普通未读条数。
// 每个可见频道都会下发一条，即使该用户此前没有 read_states 记录；这样客户端
// 重连后无需把「存在未读」退化为 1。
type ReadState struct {
	ChannelID         uuid.UUID `json:"channel_id"`
	LastReadMessageID int64     `json:"last_read_message_id,string"`
	MentionCount      int       `json:"mention_count"`
	LastMessageID     int64     `json:"last_message_id,string"`
	UnreadCount       int64     `json:"unread_count"`
}

// BuildReadStates 组装某用户的 READY read_states：只含给定（已按可见性过滤的）
// 频道集合——禁看/不可见频道即使有存量记录也不下发（docs 15 US-8）。
// 普通未读数用 last_read_message_id 与消息雪花 ID 在同一条聚合查询中计算，避免
// 客户端冷启动时只能得到「有未读」的保底值 1。
func BuildReadStates(db *gorm.DB, userID uuid.UUID, channelIDs []uuid.UUID) ([]ReadState, error) {
	states := []ReadState{}
	if len(channelIDs) == 0 {
		return states, nil
	}
	if err := db.Table("channels AS channels").
		Select(`
			channels.id AS channel_id,
			COALESCE(read_states.last_read_message_id, 0) AS last_read_message_id,
			COALESCE(read_states.mention_count, 0) AS mention_count,
			COALESCE(MAX(messages.id), 0) AS last_message_id,
			COALESCE(SUM(CASE WHEN messages.id > COALESCE(read_states.last_read_message_id, 0) THEN 1 ELSE 0 END), 0) AS unread_count
		`).
		Joins("LEFT JOIN read_states AS read_states ON read_states.user_id = ? AND read_states.channel_id = channels.id", userID).
		Joins("LEFT JOIN messages AS messages ON messages.channel_id = channels.id AND messages.visible_to = '[]'::jsonb").
		Where("channels.id IN ?", channelIDs).
		Group("channels.id, read_states.last_read_message_id, read_states.mention_count").
		Order("channels.id").
		Scan(&states).Error; err != nil {
		return nil, err
	}
	return states, nil
}

// ChannelViewers 列出对某频道具备 VIEW_CHANNEL 的全部成员 user_id
// （权限覆盖变更前后各算一次即可得到可见性增减集合）。
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
