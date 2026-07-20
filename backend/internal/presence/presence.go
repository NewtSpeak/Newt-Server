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

// Info 某用户的一份状态视图（掩码与否由查询方视角决定）。
type Info struct {
	Status     string
	CustomText string
}

// SharedMemberFunc 返回与 userID 共享至少一个 guild 的全部其他用户 ID（去重、不含本人）。
type SharedMemberFunc func(userID uuid.UUID) ([]uuid.UUID, error)

type userState struct {
	sessions   map[string]string // sessionID → 该端期望状态
	customText string
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

// SetStatus 设置某端期望状态（Gateway 上行 PRESENCE_UPDATE 帧）。
// customText 为该用户级自定义状态文本（最后写入生效，预留字段）。
// 未登记的会话按新连接补登记（对冲清扫竞态：能发上行帧说明连接存活）。
func (m *Manager) SetStatus(userID uuid.UUID, sessionID, status, customText string) bool {
	if !ValidStatus(status) {
		return false
	}
	m.mutate(userID, func(state *userState) {
		state.sessions[sessionID] = status
		state.customText = customText
	})
	return true
}

// mutate 在锁内应用变更并对比前后视图，必要时发布事件（发布在锁外执行）。
func (m *Manager) mutate(userID uuid.UUID, apply func(*userState)) {
	m.mu.Lock()
	state, ok := m.users[userID]
	if !ok {
		state = &userState{sessions: make(map[string]string)}
		m.users[userID] = state
	}
	prevReal, prevText := state.merged(), state.customText
	apply(state)
	real, text := state.merged(), state.customText
	if real == StatusOffline {
		state.customText = ""
		text = ""
		delete(m.users, userID)
	}
	m.mu.Unlock()

	prevDisplayed, prevDisplayedText := mask(prevReal, prevText)
	displayed, displayedText := mask(real, text)
	if displayed != prevDisplayed || displayedText != prevDisplayedText {
		m.publishToOthers(userID, displayed, displayedText)
	}
	if real != prevReal || text != prevText {
		m.publishToSelf(userID, real, text)
	}
}

// mask 他人视角掩码：invisible → offline，且 offline 不携带 custom_text。
func mask(status, customText string) (string, string) {
	if status == StatusInvisible || status == StatusOffline {
		return StatusOffline, ""
	}
	return status, customText
}

// publishToOthers 定向发给共享 guild 的全部成员（不含本人；离线成员无会话，hub 自然丢弃）。
// 不走 GuildID 广播是刻意的：广播会把同一载荷送达本人，而他人载荷是掩码过的，
// 定向排除本人保证「本人看真实状态、他人看掩码状态」两路彻底分离。
func (m *Manager) publishToOthers(userID uuid.UUID, status, customText string) {
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
	m.bus.Publish(eventbus.Event{
		Type:    eventbus.EventPresenceUpdate,
		UserIDs: memberIDs,
		Payload: eventbus.NewPresenceUpdatePayload(userID, status, customText),
	})
}

// publishToSelf 定向发给本人全部端（真实状态，含 invisible）。
func (m *Manager) publishToSelf(userID uuid.UUID, status, customText string) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(eventbus.Event{
		Type:    eventbus.EventPresenceUpdate,
		UserIDs: []uuid.UUID{userID},
		Payload: eventbus.NewPresenceUpdatePayload(userID, status, customText),
	})
}

// Displayed 返回 viewer 视角下一组用户的状态：本人条目为真实合并状态，
// 他人条目做 invisible→offline 掩码；offline 用户不出现在结果中
//（viewer 本人 offline 时同样省略——调用方通常在其连接存活期间查询）。
func (m *Manager) Displayed(viewerID uuid.UUID, userIDs []uuid.UUID) map[uuid.UUID]Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[uuid.UUID]Info, len(userIDs))
	for _, id := range userIDs {
		state, ok := m.users[id]
		if !ok {
			continue
		}
		status, text := state.merged(), state.customText
		if id != viewerID {
			status, text = mask(status, text)
		}
		if status == StatusOffline {
			continue
		}
		result[id] = Info{Status: status, CustomText: text}
	}
	return result
}
