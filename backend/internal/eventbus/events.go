package eventbus

// Gateway 下发给客户端的事件类型（对齐 docs 05 §11、08 §8.1、09 §7.1、11 §8.2、12 §6.3、13 §4/§8、14 §8）。
const (
	EventVoiceStateUpdate   = "VOICE_STATE_UPDATE"
	EventVoiceServerUpdate  = "VOICE_SERVER_UPDATE"
	EventVoiceCapsUpdate    = "VOICE_CAPS_UPDATE"
	EventVoiceChannelStatus = "VOICE_CHANNEL_STATUS"
	EventVoiceMigrating     = "VOICE_MIGRATING"
	EventVoiceMigrated      = "VOICE_MIGRATED"
	EventVoicePackPlay      = "VOICE_PACK_PLAY"
	// EventVoiceMove 管理员移动成员到另一语音频道（docs 09 FR-29）：定向发给被移动者，
	// payload 含 guild_id / from_channel_id / to_channel_id；客户端收到后按正常
	// POST /voice/join 流程接入目标频道（join 响应自带新节点与 token）。
	EventVoiceMove = "VOICE_MOVE"

	EventMessageCreate         = "MESSAGE_CREATE"
	EventMessageUpdate         = "MESSAGE_UPDATE"
	EventMessageDelete         = "MESSAGE_DELETE"
	EventMessageReactionAdd    = "MESSAGE_REACTION_ADD"
	EventMessageReactionRemove = "MESSAGE_REACTION_REMOVE"
	EventTypingStart           = "TYPING_START" // 频道内打字指示（用户端 POST /channels/{id}/typing 触发）

	// EventReadStateUpdate 已读状态跨端同步（docs 15 §7-1），两个触发点，均定向（UserIDs）：
	//   - 本人 ack 某频道后发给本人全部端（mention_count=0，其他端据此清除角标）；
	//   - 提及计数增长时发给被提及者全部端（mention_count 为累计后的最新值）。
	// payload 含 user_id / channel_id / last_read_message_id / mention_count / event_at。
	EventReadStateUpdate = "READ_STATE_UPDATE"

	EventRestrictionCreate = "RESTRICTION_CREATE"
	EventRestrictionUpdate = "RESTRICTION_UPDATE"
	EventRestrictionLift   = "RESTRICTION_LIFT"

	EventStageQueueUpdate    = "STAGE_QUEUE_UPDATE"
	EventStageInstanceUpdate = "STAGE_INSTANCE_UPDATE"

	EventScreenShareStart  = "SCREEN_SHARE_START"
	EventScreenShareStop   = "SCREEN_SHARE_STOP"
	EventScreenQuotaUpdate = "SCREEN_QUOTA_UPDATE"

	EventPermissionsUpdate = "PERMISSIONS_UPDATE"

	// 服务器 / 频道 / 成员 / 角色结构事件（docs 14 §3.2 / §7-5）。
	// GUILD_CREATE 在建服与新成员加入时对当事人定向发送（payload 为 snapshot.GuildCreatePayload，
	// 含该用户可见的完整 guild 快照）；GUILD_UPDATE / GUILD_DELETE / CHANNEL_UPDATE /
	// CHANNEL_DELETE / GUILD_ROLE_DELETE 由 internal/guildapi 的结构管理端点发布
	//（guild/channel/role 的 PATCH/DELETE、批量排序与权限覆盖变更）。
	EventGuildCreate       = "GUILD_CREATE"
	EventGuildUpdate       = "GUILD_UPDATE"
	EventGuildDelete       = "GUILD_DELETE"
	EventChannelCreate     = "CHANNEL_CREATE"
	EventChannelUpdate     = "CHANNEL_UPDATE"
	EventChannelDelete     = "CHANNEL_DELETE"
	EventGuildMemberAdd    = "GUILD_MEMBER_ADD"
	EventGuildMemberRemove = "GUILD_MEMBER_REMOVE"
	EventGuildRoleCreate   = "GUILD_ROLE_CREATE"
	EventGuildRoleUpdate   = "GUILD_ROLE_UPDATE"
	EventGuildRoleDelete   = "GUILD_ROLE_DELETE"

	// 封禁事件（docs 08 §8-8）：guild 广播；GUILD_BAN_REMOVE 同时定向被解封者
	//（其若在线可立即感知可重新加入）。载荷 {guild_id, user_id, reason?, event_at}。
	EventGuildBanAdd    = "GUILD_BAN_ADD"
	EventGuildBanRemove = "GUILD_BAN_REMOVE"

	// 服级/频道级配置变更（实时同步专项）：
	//   - GUILD_CONFIG_UPDATE：上传上限 / 消息保留 / 服级语音包配置等 guild 级配置，
	//     guild 广播，载荷 {guild_id, kind, config, event_at}，kind 区分配置域；
	//   - CHANNEL_CONFIG_UPDATE：频道级配置（语音包开关等），带 ChannelID 按可见性过滤。
	EventGuildConfigUpdate   = "GUILD_CONFIG_UPDATE"
	EventChannelConfigUpdate = "CHANNEL_CONFIG_UPDATE"

	// VOICE_PACK_UPDATE 语音包定义变更（CRUD/音频替换/停用）：guild 广播，客户端重拉选包列表。
	// 用户级选包变更（select/@me 清除）以同事件定向本人全部端（载荷带 selection）。
	EventVoicePackUpdate = "VOICE_PACK_UPDATE"

	// VOICE_NODE_POOL_UPDATE 服级节点池变更：guild 广播轻量通知（载荷仅 guild_id+event_at），
	// 客户端据此重拉 GET /guilds/{gid}/voice/nodes 候选列表刷新 RTT 探测目标。
	EventVoiceNodePoolUpdate = "VOICE_NODE_POOL_UPDATE"

	// 机器人与消息流式/卡片（bot 专项）。
	EventMessageStreamStart = "MESSAGE_STREAM_START" // 流式消息开始（占位消息已创建）
	EventMessageStreamDelta = "MESSAGE_STREAM_DELTA" // 流式增量分片
	EventMessageStreamEnd   = "MESSAGE_STREAM_END"   // 流式结束（最终态）

	// 用户资料 / 自定义 / 徽章（customization 专项）。
	EventUserUpdate        = "USER_UPDATE"         // 头像/横幅/强调色变更
	EventGuildMemberUpdate = "GUILD_MEMBER_UPDATE" // 成员昵称/角色/展示变更
	EventBadgeGrant        = "BADGE_GRANT"
	EventBadgeRevoke       = "BADGE_REVOKE"
	// EventBadgeUpdate 徽章定义变更（创建/编辑/删除）：guild 广播，
	// 载荷 {guild_id, badge_id, deleted?, badge?, event_at}，删除时 deleted=true。
	EventBadgeUpdate = "BADGE_UPDATE"

	// 管理员临场（adminpresence 专项）：管理员进入/离开频道的隐身与审计提示。
	EventAdminPresenceUpdate = "ADMIN_PRESENCE_UPDATE"
	EventChannelAuditNotice  = "CHANNEL_AUDIT_NOTICE" // 频道审计提示（可配置是否下发）

	// 用户在线状态（Presence，docs 01 §3.4）：状态变化时下发。
	// 定向发给共享 guild 的成员（对他人的载荷已做 invisible→offline 掩码）
	// 与本人全部端（真实状态）。Payload 为 PresenceUpdatePayload。
	EventPresenceUpdate = "PRESENCE_UPDATE"

	// 用户设置跨端同步（docs 16 §7-1）：PATCH /users/@me/settings 成功后
	// 定向发给本人全部端。Payload 为 UserSettingsUpdatePayload（全量设置文档）。
	EventUserSettingsUpdate = "USER_SETTINGS_UPDATE"

	// 密钥/连接信息同步（keysync 专项）：SyncVault 更新后定向发给本人全部端，
	// 各端据此拉取最新密文实现服务器登录/连接信息的实时多端同步。
	EventVaultUpdate = "VAULT_UPDATE"

	// 贴图与表情包（docs 17）：包/条目变更、库引用软隐藏、服 ban。
	EventStickerPackCreate      = "STICKER_PACK_CREATE"
	EventStickerPackUpdate      = "STICKER_PACK_UPDATE"
	EventStickerPackDelete      = "STICKER_PACK_DELETE" // payload.status 区分 soft_deleted / purged
	EventStickerPackRestore     = "STICKER_PACK_RESTORE"
	EventStickerItemCreate      = "STICKER_ITEM_CREATE"
	EventStickerItemUpdate      = "STICKER_ITEM_UPDATE"
	EventStickerItemDelete      = "STICKER_ITEM_DELETE"
	EventStickerLibraryUpdate   = "STICKER_LIBRARY_UPDATE" // Install 软隐藏/恢复时推给安装者
	EventGuildStickerPackBanAdd = "GUILD_STICKER_PACK_BAN_ADD"
	EventGuildStickerPackBanRemove = "GUILD_STICKER_PACK_BAN_REMOVE"
)

// 内部事件（仅服务内部流转，Gateway 必须过滤，不下发客户端）。
const (
	InternalNodeUp       = "internal.NODE_UP"
	InternalNodeDown     = "internal.NODE_DOWN"
	InternalNodeDraining = "internal.NODE_DRAINING"
	// InternalCapsDirty 由舞台/Restriction/屏幕共享模块发布，
	// 语音编排模块订阅后重算 caps 并向 SFU/客户端推送。
	// Payload 建议为 CapsDirtyPayload。
	InternalCapsDirty = "internal.CAPS_DIRTY"
	// InternalEdgeDown 级联边断开（SFU EdgeStatus 上报，docs 08 §6.1）。
	// voice 编排订阅后补边或标记房间降级（docs 08 §7.2）。Payload 为 EdgeDownPayload。
	InternalEdgeDown = "internal.EDGE_DOWN"
	// InternalSessionRevoke 强制下线：账号禁用/密码重置/注销后由发布方携带
	// UserIDs 发出，各 Gateway hub 消费后立即断开目标用户全部 WS 会话（4010）。
	InternalSessionRevoke = "internal.SESSION_REVOKE"
)

// EdgeDownPayload InternalEdgeDown 的载荷约定（ID 均为字符串形式的 UUID）。
type EdgeDownPayload struct {
	RoomID       string `json:"room_id"`
	Epoch        uint64 `json:"epoch"`
	ParentNodeID string `json:"parent_node_id"`
	ChildNodeID  string `json:"child_node_id"`
}

// CapsDirtyPayload InternalCapsDirty 的载荷约定。
type CapsDirtyPayload struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"` // 为空表示整个频道所有在房用户都需重算
	Reason    string `json:"reason"`
}

// IsInternal 判断事件是否为内部事件（Gateway 据此过滤）。
func IsInternal(eventType string) bool {
	return len(eventType) > 9 && eventType[:9] == "internal."
}
