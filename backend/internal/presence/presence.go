// Package presence 用户在线状态（docs 01 §3.4）：内存实现，单实例假设。
//
// 模型：
//   - 四个可设置状态 online / idle / dnd / invisible，另有派生状态 offline（无任何连接）；
//   - 每条 Gateway 会话（session）持有自己的期望状态（IDENTIFY 默认 online，
//     上行 PRESENCE_UPDATE 帧可改），多端合并优先级 dnd > online > idle > invisible；
//   - 隐身语义（FR-20）：对他人一律掩码为 offline（含 custom_text 一并隐藏），
//     真实 invisible 只出现在发给本人的定向事件与本人视角的快照中；
//   - 会话生命周期与 gateway hub 对齐：连接断开后 resume 窗口内仍视为在线，
//     窗口结束（hub sweep 清理会话）才转 offline 并广播。
//
// 事件发布：对外可见状态（掩码后）变化 → 定向发给共享 guild 的全部成员（不含本人）；
// 本人真实合并状态变化 → 定向发给本人全部端。两路载荷分离，杜绝 invisible 泄露。
package presence

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

const (
	StatusOnline    = "online"
	StatusIdle      = "idle"
	StatusDnd       = "dnd"
	StatusInvisible = "invisible"
	StatusOffline   = "offline" // 派生状态，不可主动设置
)

// statusRank 多端合并优先级（docs 01 FR-25）：dnd > online > idle > invisible。
var statusRank = map[string]int{StatusDnd: 4, StatusOnline: 3, StatusIdle: 2, StatusInvisible: 1}

// ValidStatus 判断是否为可主动设置的状态。
func ValidStatus(status string) bool { _, ok := statusRank[status]; return ok }

// CustomStatus 自定义状态（docs 01 FR-23）：文本 + 可选 emoji + 可选过期时间。
// 过期采用惰性判定（读取时校验），载荷同时携带 expires_at 供客户端自行倒计时。
type CustomStatus struct {
	Text      string
	Emoji     string
	ExpiresAt *time.Time
}

// expired 过期判定（零值 ExpiresAt 表示不过期）。
func (s CustomStatus) expired(now time.Time) bool {
	return s.ExpiresAt != nil && now.After(*s.ExpiresAt)
}

// empty 是否为空状态。
func (s CustomStatus) empty() bool { return s.Text == "" && s.Emoji == "" }

// Info 某用户的一份状态视图（掩码与否由查询方视角决定）。
type Info struct {
	Status          string
	CustomText      string
	CustomEmoji     string
	CustomExpiresAt *time.Time
}

// SharedMemberFunc 返回与 userID 共享至少一个 guild 的全部其他用户 ID（去重、不含本人）。
type SharedMemberFunc func(userID uuid.UUID) ([]uuid.UUID, error)

type userState struct {
	sessions map[string]string // sessionID → 该端期望状态
	custom   CustomStatus
}

// effectiveCustom 惰性过期后的自定义状态。
func (s *userState) effectiveCustom(now time.Time) CustomStatus {
	if s.custom.expired(now) {
		return CustomStatus{}
	}
	return s.custom
}

// merged 多端合并后的真实状态（无会话 → offline）。
func (s *userState) merged() string {
	best := StatusOffline
	bestRank := 0
	for _, status := range s.sessions {
		if rank := statusRank[status]; rank > bestRank {
			best, bestRank = status, rank
		}
	}
	return best
}

// Manager 进程内 Presence 注册表；双认证平面的 Gateway hub 共享同一实例，
// 同一用户跨平面的连接自然参与多端合并。
type Manager struct {
	bus           *eventbus.Bus
	sharedMembers SharedMemberFunc

	mu    sync.Mutex
	users map[uuid.UUID]*userState
}

// NewManager 构造（测试注入 sharedMembers；bus 为 nil 时不发布事件）。
func NewManager(bus *eventbus.Bus, sharedMembers SharedMemberFunc) *Manager {
	return &Manager{bus: bus, sharedMembers: sharedMembers, users: make(map[uuid.UUID]*userState)}
}

// NewDBManager 生产构造：共享 guild 成员查询走 PostgreSQL。
func NewDBManager(db *gorm.DB, bus *eventbus.Bus) *Manager {
	return NewManager(bus, func(userID uuid.UUID) ([]uuid.UUID, error) {
		var ids []uuid.UUID
		err := db.Model(&model.Member{}).Distinct("user_id").
			Where("guild_id IN (?) AND user_id <> ?",
				db.Model(&model.Member{}).Select("guild_id").Where("user_id = ?", userID), userID).
			Pluck("user_id", &ids).Error
		return ids, err
	})
}

// Connect 登记一条 Gateway 会话（IDENTIFY/RESUME 成功后调用），默认 online；
// 幂等：已登记的会话不重置其状态（对冲 RESUME 与清扫竞态）。
func (m *Manager) Connect(userID uuid.UUID, sessionID string) {
	m.mutate(userID, func(state *userState) {
		if _, ok := state.sessions[sessionID]; !ok {
			state.sessions[sessionID] = StatusOnline
		}
	})
}

// Disconnect 注销会话（hub 清扫过期会话时调用，即 resume 窗口结束）。
func (m *Manager) Disconnect(userID uuid.UUID, sessionID string) {
	m.mutate(userID, func(state *userState) {
		delete(state.sessions, sessionID)
	})
}

// SetStatus 设置某端期望状态（Gateway 上行 PRESENCE_UPDATE 帧，兼容旧签名）。
// customText 为该用户级自定义状态文本（最后写入生效）。
func (m *Manager) SetStatus(userID uuid.UUID, sessionID, status, customText string) bool {
	return m.SetStatusFull(userID, sessionID, status, CustomStatus{Text: customText})
}

// SetStatusFull 设置某端期望状态 + 完整自定义状态（文本/emoji/过期时间，docs 01 FR-23）。
// 未登记的会话按新连接补登记（对冲清扫竞态：能发上行帧说明连接存活）。
func (m *Manager) SetStatusFull(userID uuid.UUID, sessionID, status string, custom CustomStatus) bool {
	if !ValidStatus(status) {
		return false
	}
	m.mutate(userID, func(state *userState) {
		state.sessions[sessionID] = status
		state.custom = custom
	})
	return true
}

// mutate 在锁内应用变更并对比前后视图，必要时发布事件（发布在锁外执行）。
func (m *Manager) mutate(userID uuid.UUID, apply func(*userState)) {
	now := time.Now().UTC()
	m.mu.Lock()
	state, ok := m.users[userID]
	if !ok {
		state = &userState{sessions: make(map[string]string)}
		m.users[userID] = state
	}
	prevReal, prevCustom := state.merged(), state.effectiveCustom(now)
	apply(state)
	real, custom := state.merged(), state.effectiveCustom(now)
	if real == StatusOffline {
		state.custom = CustomStatus{}
		custom = CustomStatus{}
		delete(m.users, userID)
	}
	m.mu.Unlock()

	prevDisplayed, prevDisplayedCustom := mask(prevReal, prevCustom)
	displayed, displayedCustom := mask(real, custom)
	if displayed != prevDisplayed || !equalCustom(displayedCustom, prevDisplayedCustom) {
		m.publishToOthers(userID, displayed, displayedCustom)
	}
	if real != prevReal || !equalCustom(custom, prevCustom) {
		m.publishToSelf(userID, real, custom)
	}
}

// equalCustom 值语义比较（ExpiresAt 按时间值而非指针比较）。
func equalCustom(a, b CustomStatus) bool {
	if a.Text != b.Text || a.Emoji != b.Emoji {
		return false
	}
	switch {
	case a.ExpiresAt == nil && b.ExpiresAt == nil:
		return true
	case a.ExpiresAt == nil || b.ExpiresAt == nil:
		return false
	default:
		return a.ExpiresAt.Equal(*b.ExpiresAt)
	}
}

// mask 他人视角掩码：invisible → offline，且 offline 不携带自定义状态。
func mask(status string, custom CustomStatus) (string, CustomStatus) {
	if status == StatusInvisible || status == StatusOffline {
		return StatusOffline, CustomStatus{}
	}
	return status, custom
}

// publishToOthers 定向发给共享 guild 的全部成员（不含本人；离线成员无会话，hub 自然丢弃）。
// 不走 GuildID 广播是刻意的：广播会把同一载荷送达本人，而他人载荷是掩码过的，
// 定向排除本人保证「本人看真实状态、他人看掩码状态」两路彻底分离。
func (m *Manager) publishToOthers(userID uuid.UUID, status string, custom CustomStatus) {
	if m.bus == nil || m.sharedMembers == nil {
		return
	}
	memberIDs, err := m.sharedMembers(userID)
	if err != nil {
		log.Printf("presence: 查询用户 %s 共享成员失败，跳过广播: %v", userID, err)
		return
	}
	if len(memberIDs) == 0 {
		return
	}
	payload := eventbus.NewPresenceUpdatePayload(userID, status, custom.Text)
	payload.CustomEmoji, payload.CustomExpiresAt = custom.Emoji, custom.ExpiresAt
	m.bus.Publish(eventbus.Event{
		Type:    eventbus.EventPresenceUpdate,
		UserIDs: memberIDs,
		Payload: payload,
	})
}

// publishToSelf 定向发给本人全部端（真实状态，含 invisible）。
func (m *Manager) publishToSelf(userID uuid.UUID, status string, custom CustomStatus) {
	if m.bus == nil {
		return
	}
	payload := eventbus.NewPresenceUpdatePayload(userID, status, custom.Text)
	payload.CustomEmoji, payload.CustomExpiresAt = custom.Emoji, custom.ExpiresAt
	m.bus.Publish(eventbus.Event{
		Type:    eventbus.EventPresenceUpdate,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

// Displayed 返回 viewer 视角下一组用户的状态：本人条目为真实合并状态，
// 他人条目做 invisible→offline 掩码；offline 用户不出现在结果中
//（viewer 本人 offline 时同样省略——调用方通常在其连接存活期间查询）。
func (m *Manager) Displayed(viewerID uuid.UUID, userIDs []uuid.UUID) map[uuid.UUID]Info {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[uuid.UUID]Info, len(userIDs))
	for _, id := range userIDs {
		state, ok := m.users[id]
		if !ok {
			continue
		}
		status, custom := state.merged(), state.effectiveCustom(now)
		if id != viewerID {
			status, custom = mask(status, custom)
		}
		if status == StatusOffline {
			continue
		}
		result[id] = Info{
			Status: status, CustomText: custom.Text,
			CustomEmoji: custom.Emoji, CustomExpiresAt: custom.ExpiresAt,
		}
	}
	return result
}
