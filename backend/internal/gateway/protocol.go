package gateway

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/platformbadge"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
)

// WebSocket 帧协议（Discord 风格简化版）：所有帧均为 JSON 文本帧 {"op":..., "t":..., "s":..., "d":...}。
// 仅 DISPATCH 帧携带 t（事件名，取值见 eventbus 事件表，docs 05 §11）与 s（会话内递增序列号）。
//
// 握手时序（docs 14 §3.1 / §7）：
//  1. S→C HELLO {heartbeat_interval_ms}
//  2. C→S IDENTIFY {token}（超时未发或 token 无效则关闭连接）
//     或 C→S RESUME {token, session_id, last_seq}（断线重连续传）
//  3. S→C READY {session_id, user, guild_ids, guilds, read_states}（IDENTIFY 路径，全量快照）
//     或 S→C 按序补发缺口 DISPATCH 后发 RESUMED（RESUME 成功路径）
//     或 S→C INVALID_SESSION 后以 4009 关闭（RESUME 失败：session 不存在 / 用户不符 /
//     超出回放窗口；客户端应重新 IDENTIFY 做全量同步，docs 14 FR-04）
//  4. C→S HEARTBEAT ↔ S→C HEARTBEAT_ACK（两个周期未收到心跳判死）
//  5. S→C DISPATCH {t: 事件名, s: 序列号, d: 载荷}
//
// 会话与回放：每个会话（session_id）在服务端保留事件回放环形缓冲
//（默认每会话最近 512 条或 60s，二者取小，见 options）；连接断开后会话保留
// ResumeWindow（默认 60s）等待 RESUME，超时清理。
const (
	opHello        = "HELLO"
	opIdentify     = "IDENTIFY"
	opResume       = "RESUME"
	opReady        = "READY"
	opResumed      = "RESUMED"
	opHeartbeat    = "HEARTBEAT"
	opHeartbeatACK = "HEARTBEAT_ACK"
	opDispatch     = "DISPATCH"
	// opInvalidSession RESUME 失败（session 不存在 / 用户不符 / 超出回放窗口）；
	// 发送后以 closeInvalidSession 关闭，客户端应重新 IDENTIFY 全量同步。
	opInvalidSession = "INVALID_SESSION"
	// opPresenceUpdate 客户端上行：设置本端在线状态（docs 01 §3.4）。
	// d 为 presenceUpdateData；status 非法时静默忽略（与其他无法解析的上行帧一致）。
	// 服务端多端合并后经 DISPATCH PRESENCE_UPDATE 事件下发（同名事件，方向以帧类型区分）。
	opPresenceUpdate = "PRESENCE_UPDATE"
	// opPresence opPresenceUpdate 的短别名（两者语义完全一致，服务端同时接受）。
	opPresence = "PRESENCE"
)

// 应用层关闭码（4000–4999 供应用自定义）。
const (
	closeHeartbeatDead   = 4000 // 连续两个心跳周期未收到 HEARTBEAT
	closeIdentifyTimeout = 4001 // 超时未 IDENTIFY/RESUME 或首帧不符合协议
	closeAuthFailed      = 4003 // 访问令牌无效或用户不存在
	closeSessionReplaced = 4006 // 同一 session 被新连接 RESUME 接管，旧连接关闭
	closeSlowConsumer    = 4008 // 发送队列积压（慢消费者保护）
	closeInvalidSession  = 4009 // RESUME 失败：session 不存在 / 超出回放窗口 / 用户不符
	closeSessionRevoked  = 4010 // 会话被服务端吊销（账号禁用/密码重置/注销），不可 RESUME，须重新登录
)

// inFrame 客户端上行帧。
type inFrame struct {
	Op string          `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
}

// outFrame 服务端下行帧；S 仅 DISPATCH 帧携带（会话内从 1 起递增）。
type outFrame struct {
	Op string `json:"op"`
	T  string `json:"t,omitempty"`
	S  int64  `json:"s,omitempty"`
	D  any    `json:"d,omitempty"`
}

// helloData HELLO 载荷。
type helloData struct {
	HeartbeatIntervalMS int64 `json:"heartbeat_interval_ms"`
}

// identifyData IDENTIFY 载荷。
type identifyData struct {
	Token string `json:"token"`
}

// resumeData RESUME 载荷：token 重新认证（防 session_id 被冒用）+ 最后收到的序列号。
type resumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	LastSeq   int64  `json:"last_seq"`
}

// presenceUpdateData 上行 PRESENCE_UPDATE 载荷：本端期望状态
//（online/idle/dnd/invisible）+ 可选自定义状态（文本/emoji/过期时间，docs 01 FR-23）。
type presenceUpdateData struct {
	Status          string     `json:"status"`
	CustomText      string     `json:"custom_text"`
	CustomEmoji     string     `json:"custom_emoji"`
	CustomExpiresAt *time.Time `json:"custom_expires_at"`
}

// readyData READY 载荷：会话 ID + 自身用户 + 全量快照（docs 14 §7-2）。
//   - Guilds 每项内嵌可见频道（含类型/名称/排序/语音配置）、全量角色、自身成员
//     （含 role_ids、nickname）、可见频道内的语音状态与该服在线成员状态（presences）；
//   - GuildIDs 为兼容保留的服务器 ID 列表（与 Guilds 一致）；
//   - Presences 各 guild 在线成员状态的并集（按 user_id 去重，只含非 offline；
//     他人 invisible 已掩码，本人条目为真实状态）——与 guilds[].presences 内容一致，
//     提供扁平视图便于客户端一次性建 presence 缓存；
//   - ReadStates 该用户全部可见频道的已读状态（docs 15 §7-1：{channel_id,
//     last_read_message_id, mention_count}，没有记录的频道省略）。
type readyData struct {
	SessionID  string                 `json:"session_id"`
	User       platformbadge.UserView `json:"user"`
	GuildIDs   []uuid.UUID            `json:"guild_ids"`
	Guilds     []snapshot.Guild       `json:"guilds"`
	Presences  []snapshot.Presence    `json:"presences"`
	ReadStates []snapshot.ReadState   `json:"read_states"`
}
