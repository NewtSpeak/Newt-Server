package gateway

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/presence"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
)

// directory 抽象 guild 成员、频道可见性与 READY 快照查询：生产走 PostgreSQL（见 store.go），
// 单测注入 mock 以摆脱数据库依赖。
type directory interface {
	// GuildMemberIDs 返回该服务器全部成员的 user_id。
	GuildMemberIDs(guildID uuid.UUID) ([]uuid.UUID, error)
	// CanSeeChannel 判断用户对频道是否可见（docs 06 议题 8：不可见即不推送）。
	CanSeeChannel(user model.User, guildID, channelID uuid.UUID) bool
	// GuildSnapshots 组装 READY 全量快照（按用户 VIEW_CHANNEL 过滤频道，docs 14 §7-2）。
	GuildSnapshots(user model.User, guildIDs []uuid.UUID) ([]snapshot.Guild, error)
	// ReadStates 该用户在给定（已按可见性过滤的）频道内的已读状态（docs 15 §7-1）。
	ReadStates(userID uuid.UUID, channelIDs []uuid.UUID) ([]snapshot.ReadState, error)
}

// hub 在线会话注册表：session_id → 会话，另按 userID 建索引（同一用户允许多端多会话）。
// 会话在连接断开后保留 resumeWindow 等待 RESUME，由 sweepLoop 定期清理。
type hub struct {
	dir          directory
	writeTimeout time.Duration
	bufferLimit  int
	bufferTTL    time.Duration
	resumeWindow time.Duration
	// presence 在线状态注册表（可为 nil）：会话生命周期即 presence 连接生命周期，
	// 清扫过期会话（resume 窗口结束）时同步注销，触发对外 offline 广播。
	presence *presence.Manager

	mu       sync.RWMutex
	sessions map[string]*session
	byUser   map[uuid.UUID]map[*session]struct{}
}

func newHub(dir directory, opts options) *hub {
	return &hub{
		dir:          dir,
		writeTimeout: opts.WriteTimeout,
		bufferLimit:  opts.ReplayBufferSize,
		bufferTTL:    opts.ReplayTTL,
		resumeWindow: opts.ResumeWindow,
		sessions:     make(map[string]*session),
		byUser:       make(map[uuid.UUID]map[*session]struct{}),
	}
}

// register 登记会话（幂等；RESUME 成功后重复登记可对冲清扫竞态）。
func (h *hub) register(sess *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sess.id] = sess
	set, ok := h.byUser[sess.user.ID]
	if !ok {
		set = make(map[*session]struct{})
		h.byUser[sess.user.ID] = set
	}
	set[sess] = struct{}{}
}

func (h *hub) unregisterLocked(sess *session) {
	delete(h.sessions, sess.id)
	if set, ok := h.byUser[sess.user.ID]; ok {
		delete(set, sess)
		if len(set) == 0 {
			delete(h.byUser, sess.user.ID)
		}
	}
}

// findSession 按 session_id 查找（RESUME 用）。
func (h *hub) findSession(id string) (*session, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	sess, ok := h.sessions[id]
	return sess, ok
}

// userSessions 返回某用户全部会话快照（含断开待 resume 的会话——事件继续入其回放缓冲）。
func (h *hub) userSessions(userID uuid.UUID) []*session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set := h.byUser[userID]
	if len(set) == 0 {
		return nil
	}
	result := make([]*session, 0, len(set))
	for sess := range set {
		result = append(result, sess)
	}
	return result
}

// sweepLoop 定期清理断开超过 resumeWindow 的会话（随进程存活）。
func (h *hub) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for now := range ticker.C {
		h.sweep(now)
	}
}

func (h *hub) sweep(now time.Time) {
	h.mu.Lock()
	var expired []*session
	for _, sess := range h.sessions {
		if sess.expired(now, h.resumeWindow) {
			h.unregisterLocked(sess)
			expired = append(expired, sess)
		}
	}
	h.mu.Unlock()
	// presence 注销放在锁外：Disconnect 内部会发布事件并回查共享成员，避免与 hub 锁交叠。
	if h.presence != nil {
		for _, sess := range expired {
			h.presence.Disconnect(sess.user.ID, sess.id)
		}
	}
}

// closeUserSessions 强制断开某用户的全部 Gateway 会话（账号禁用/密码重置/注销）：
// 立即关闭连接（4010）并从注册表移除会话，令其不可 RESUME（access token 虽未过期，
// 但会话已吊销，客户端必须重新登录/IDENTIFY——届时认证自然失败）。
func (h *hub) closeUserSessions(userID uuid.UUID) {
	h.mu.Lock()
	var revoked []*session
	if set, ok := h.byUser[userID]; ok {
		for sess := range set {
			h.unregisterLocked(sess)
			revoked = append(revoked, sess)
		}
	}
	h.mu.Unlock()
	for _, sess := range revoked {
		sess.mu.Lock()
		target := sess.conn
		sess.conn = nil
		sess.mu.Unlock()
		if target != nil {
			target.shutdown(closeSessionRevoked, "会话已被吊销，请重新登录", h.writeTimeout)
		}
		if h.presence != nil {
			h.presence.Disconnect(sess.user.ID, sess.id)
		}
	}
}

// dispatch 事件总线回调（Register 时 Subscribe 一次），路由规则：
//  1. 内部事件（internal.*）绝不下发客户端；internal.SESSION_REVOKE 例外——
//     在本层消费：强制断开目标用户全部会话（账号禁用/密码重置/注销联动）；
//  2. UserIDs 非空 → 定向推送（Restriction 当事人必推走此路径，docs 12 §6.3/§7）；
//  3. 否则 GuildID 非空 → 广播给该服全部在线成员；若 ChannelID 也非空，
//     再按频道可见性逐用户过滤（不可见者不推，docs 06 议题 8）。
//
// 事件在会话层逐条分配递增序列号 s 并写入回放缓冲（断线期间也持续累积，供 RESUME 补发）。
func (h *hub) dispatch(event eventbus.Event) {
	if event.Type == eventbus.InternalSessionRevoke {
		for _, userID := range event.UserIDs {
			h.closeUserSessions(userID)
		}
		return
	}
	if eventbus.IsInternal(event.Type) {
		return
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		log.Printf("gateway: 序列化事件 %s 失败: %v", event.Type, err)
		return
	}
	if len(event.UserIDs) > 0 {
		for _, userID := range event.UserIDs {
			h.push(h.userSessions(userID), event.Type, payload)
		}
		return
	}
	if event.GuildID == nil {
		return
	}
	memberIDs, err := h.dir.GuildMemberIDs(*event.GuildID)
	if err != nil {
		log.Printf("gateway: 查询 guild %s 成员失败，丢弃事件 %s: %v", event.GuildID, event.Type, err)
		return
	}
	for _, userID := range memberIDs {
		sessions := h.userSessions(userID)
		if len(sessions) == 0 {
			continue
		}
		if event.ChannelID != nil && !h.dir.CanSeeChannel(sessions[0].user, *event.GuildID, *event.ChannelID) {
			continue
		}
		h.push(sessions, event.Type, payload)
	}
}

// push 向一组会话投递；连接入队失败（慢消费者积压）即断开该连接，
// 会话保留（事件已入回放缓冲，客户端可 RESUME 补齐）。
func (h *hub) push(sessions []*session, eventType string, payload json.RawMessage) {
	for _, sess := range sessions {
		delivered, target := sess.dispatch(eventType, payload, h.bufferLimit, h.bufferTTL)
		if !delivered && target != nil {
			target.shutdown(closeSlowConsumer, "消息积压", h.writeTimeout)
			sess.detach(target)
		}
	}
}
