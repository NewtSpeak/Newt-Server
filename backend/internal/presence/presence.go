// Package presence 用户在线状态（docs 01 §3.4）+ 结构化活动（Server-18）。
//
// 模型：
//   - 四个可设置状态 online / idle / dnd / invisible，另有派生状态 offline（无任何连接）；
//   - 每条 Gateway 会话持有自己的期望状态与 activities[]；
//   - 自定义状态 custom 为账号级（最后写入覆盖）；
//   - 多端 status 合并优先级 dnd > online > idle > invisible；
//   - activities 跨端合并去重后最多 3 条（见 activity.go）；
//   - 隐身语义：对他人一律掩码为 offline（custom + activities 一并隐藏）；
//   - show_activity_to 隐私：对不可见观察者剥离 activities（status/custom 仍按 mask）。
//
// 事件发布：对外可见状态变化 → 定向发给共享 guild 成员（不含本人，可按隐私拆分载荷）；
// 本人真实合并状态变化 → 定向发给本人全部端。
package presence

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

const (
	StatusOnline    = "online"
	StatusIdle      = "idle"
	StatusDnd       = "dnd"
	StatusInvisible = "invisible"
	StatusOffline   = "offline" // 派生状态，不可主动设置
)

// ShowActivityTo 取值（Server-18 / privacy）。
const (
	ShowActivityEveryone = "everyone"
	ShowActivityFriends  = "friends"
	ShowActivityNobody   = "nobody"
)

// statusRank 多端合并优先级（docs 01 FR-25）：dnd > online > idle > invisible。
var statusRank = map[string]int{StatusDnd: 4, StatusOnline: 3, StatusIdle: 2, StatusInvisible: 1}

// ValidStatus 判断是否为可主动设置的状态。
func ValidStatus(status string) bool { _, ok := statusRank[status]; return ok }

// CustomStatus 自定义状态（docs 01 FR-23）：文本 + 可选 emoji + 可选过期时间。
type CustomStatus struct {
	Text      string
	Emoji     string
	ExpiresAt *time.Time
}

func (s CustomStatus) expired(now time.Time) bool {
	return s.ExpiresAt != nil && now.After(*s.ExpiresAt)
}

func (s CustomStatus) empty() bool { return s.Text == "" && s.Emoji == "" }

// Info 某用户的一份状态视图（掩码与否由查询方视角决定）。
type Info struct {
	Status          string
	CustomText      string
	CustomEmoji     string
	CustomExpiresAt *time.Time
	Activities      []Activity
}

// SharedMemberFunc 返回与 userID 共享至少一个 guild 的全部其他用户 ID（去重、不含本人）。
type SharedMemberFunc func(userID uuid.UUID) ([]uuid.UUID, error)

// ActivityAudienceFunc 返回 subject 的活动可见策略与好友集合。
// showTo: everyone|friends|nobody；friends 为双向好友 ID 集合（可空）。
// 为 nil 时默认 everyone（测试/无隐私注入）。
type ActivityAudienceFunc func(subjectID uuid.UUID) (showTo string, friends map[uuid.UUID]struct{})

type sessionPresence struct {
	status     string
	activities []Activity
}

type userState struct {
	sessions map[string]sessionPresence // sessionID → 该端状态
	custom   CustomStatus
}

func (s *userState) effectiveCustom(now time.Time) CustomStatus {
	if s.custom.expired(now) {
		return CustomStatus{}
	}
	return s.custom
}

func (s *userState) merged() string {
	best := StatusOffline
	bestRank := 0
	for _, sp := range s.sessions {
		if rank := statusRank[sp.status]; rank > bestRank {
			best, bestRank = sp.status, rank
		}
	}
	return best
}

func (s *userState) mergedActivities() []Activity {
	return mergeActivities(s.sessions)
}

// Manager 进程内 Presence 注册表。
type Manager struct {
	bus               *eventbus.Bus
	sharedMembers     SharedMemberFunc
	activityAudience  ActivityAudienceFunc

	mu    sync.Mutex
	users map[uuid.UUID]*userState
}

// NewManager 构造（测试注入 sharedMembers；bus 为 nil 时不发布事件）。
func NewManager(bus *eventbus.Bus, sharedMembers SharedMemberFunc) *Manager {
	return &Manager{bus: bus, sharedMembers: sharedMembers, users: make(map[uuid.UUID]*userState)}
}

// SetActivityAudience 注入活动隐私裁决（生产在 NewDBManager 后设置，或测试覆盖）。
func (m *Manager) SetActivityAudience(fn ActivityAudienceFunc) {
	m.mu.Lock()
	m.activityAudience = fn
	m.mu.Unlock()
}

// NewDBManager 生产构造：共享 guild 成员 + 活动隐私（show_activity_to + 好友）。
func NewDBManager(db *gorm.DB, bus *eventbus.Bus) *Manager {
	m := NewManager(bus, func(userID uuid.UUID) ([]uuid.UUID, error) {
		var ids []uuid.UUID
		err := db.Model(&model.Member{}).Distinct("user_id").
			Where("guild_id IN (?) AND user_id <> ?",
				db.Model(&model.Member{}).Select("guild_id").Where("user_id = ?", userID), userID).
			Pluck("user_id", &ids).Error
		return ids, err
	})
	m.activityAudience = func(subjectID uuid.UUID) (string, map[uuid.UUID]struct{}) {
		showTo := ShowActivityFriends // 默认 friends（Server-18 R2）
		var p model.PrivacySettings
		if err := db.First(&p, "user_id = ?", subjectID).Error; err == nil && p.ShowActivityTo != "" {
			showTo = p.ShowActivityTo
		}
		friends := map[uuid.UUID]struct{}{}
		if showTo == ShowActivityFriends {
			var rows []model.Relationship
			_ = db.Where("user_id = ? AND type = ?", subjectID, model.RelationshipFriend).Find(&rows).Error
			for _, r := range rows {
				friends[r.TargetUserID] = struct{}{}
			}
		}
		return showTo, friends
	}
	return m
}

// Connect 登记一条 Gateway 会话。幂等：已登记的会话不重置其状态/活动。
func (m *Manager) Connect(userID uuid.UUID, sessionID, status string) {
	if !ValidStatus(status) {
		status = StatusOnline
	}
	m.mutate(userID, func(state *userState) {
		if _, ok := state.sessions[sessionID]; !ok {
			state.sessions[sessionID] = sessionPresence{status: status}
		}
	})
}

// Disconnect 注销会话（resume 窗口结束）。
func (m *Manager) Disconnect(userID uuid.UUID, sessionID string) {
	m.mutate(userID, func(state *userState) {
		delete(state.sessions, sessionID)
	})
}

// SetStatus 兼容旧签名：仅 status + custom 文本，不改 activities。
func (m *Manager) SetStatus(userID uuid.UUID, sessionID, status, customText string) bool {
	return m.SetStatusFull(userID, sessionID, status, CustomStatus{Text: customText}, nil)
}

// SetStatusFull 设置某端期望状态 + 完整自定义状态 + 可选活动。
// activities == nil → 不修改该 session 的 activities；非 nil（含空切片）→ 替换。
func (m *Manager) SetStatusFull(userID uuid.UUID, sessionID, status string, custom CustomStatus, activities *[]Activity) bool {
	if !ValidStatus(status) {
		return false
	}
	var sanitized *[]Activity
	if activities != nil {
		clean := SanitizeActivities(*activities)
		sanitized = &clean
	}
	m.mutate(userID, func(state *userState) {
		sp := state.sessions[sessionID]
		sp.status = status
		if sanitized != nil {
			sp.activities = cloneActivities(*sanitized)
		}
		state.sessions[sessionID] = sp
		state.custom = custom
	})
	return true
}

func (m *Manager) mutate(userID uuid.UUID, apply func(*userState)) {
	now := time.Now().UTC()
	m.mu.Lock()
	state, ok := m.users[userID]
	if !ok {
		state = &userState{sessions: make(map[string]sessionPresence)}
		m.users[userID] = state
	}
	prevReal, prevCustom, prevActs := state.merged(), state.effectiveCustom(now), cloneActivities(state.mergedActivities())
	apply(state)
	real, custom, acts := state.merged(), state.effectiveCustom(now), cloneActivities(state.mergedActivities())
	if real == StatusOffline {
		state.custom = CustomStatus{}
		custom = CustomStatus{}
		acts = nil
		delete(m.users, userID)
	}
	m.mu.Unlock()

	prevDisplayed, prevDisplayedCustom, prevDisplayedActs := mask(prevReal, prevCustom, prevActs)
	displayed, displayedCustom, displayedActs := mask(real, custom, acts)
	if displayed != prevDisplayed || !equalCustom(displayedCustom, prevDisplayedCustom) ||
		!equalActivities(displayedActs, prevDisplayedActs) {
		m.publishToOthers(userID, displayed, displayedCustom, displayedActs)
	}
	if real != prevReal || !equalCustom(custom, prevCustom) || !equalActivities(acts, prevActs) {
		m.publishToSelf(userID, real, custom, acts)
	}
}

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

// mask 他人视角：invisible → offline，且 offline 不携带 custom/activities。
func mask(status string, custom CustomStatus, activities []Activity) (string, CustomStatus, []Activity) {
	if status == StatusInvisible || status == StatusOffline {
		return StatusOffline, CustomStatus{}, nil
	}
	return status, custom, activities
}

func (m *Manager) publishToOthers(userID uuid.UUID, status string, custom CustomStatus, activities []Activity) {
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

	withActs, withoutActs := m.partitionAudience(userID, memberIDs, activities)
	if len(withActs) > 0 {
		m.bus.Publish(eventbus.Event{
			Type:    eventbus.EventPresenceUpdate,
			UserIDs: withActs,
			Payload: buildPayload(userID, status, custom, activities),
		})
	}
	if len(withoutActs) > 0 {
		m.bus.Publish(eventbus.Event{
			Type:    eventbus.EventPresenceUpdate,
			UserIDs: withoutActs,
			Payload: buildPayload(userID, status, custom, nil),
		})
	}
}

// partitionAudience 按 show_activity_to 拆分观察者。
func (m *Manager) partitionAudience(subjectID uuid.UUID, memberIDs []uuid.UUID, activities []Activity) (withActs, withoutActs []uuid.UUID) {
	// 无活动或已 mask 为空：全体无活动载荷
	if len(activities) == 0 {
		return nil, memberIDs
	}
	m.mu.Lock()
	fn := m.activityAudience
	m.mu.Unlock()
	if fn == nil {
		return memberIDs, nil
	}
	showTo, friends := fn(subjectID)
	switch showTo {
	case ShowActivityNobody:
		return nil, memberIDs
	case ShowActivityFriends:
		for _, id := range memberIDs {
			if _, ok := friends[id]; ok {
				withActs = append(withActs, id)
			} else {
				withoutActs = append(withoutActs, id)
			}
		}
		return withActs, withoutActs
	default: // everyone 及未知值
		return memberIDs, nil
	}
}

func (m *Manager) publishToSelf(userID uuid.UUID, status string, custom CustomStatus, activities []Activity) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(eventbus.Event{
		Type:    eventbus.EventPresenceUpdate,
		UserIDs: []uuid.UUID{userID},
		Payload: buildPayload(userID, status, custom, activities),
	})
}

func buildPayload(userID uuid.UUID, status string, custom CustomStatus, activities []Activity) eventbus.PresenceUpdatePayload {
	payload := eventbus.NewPresenceUpdatePayload(userID, status, custom.Text)
	payload.CustomEmoji, payload.CustomExpiresAt = custom.Emoji, custom.ExpiresAt
	if len(activities) > 0 {
		payload.Activities = toEventActivities(activities)
	}
	return payload
}

func toEventActivities(in []Activity) []eventbus.PresenceActivity {
	out := make([]eventbus.PresenceActivity, len(in))
	for i, a := range in {
		out[i] = eventbus.PresenceActivity{
			Type: a.Type, Name: a.Name, Details: a.Details, State: a.State,
			ApplicationID: a.ApplicationID, URL: a.URL, Source: a.Source,
		}
		if a.Assets != nil {
			out[i].Assets = &eventbus.PresenceActivityAssets{
				LargeImage: a.Assets.LargeImage, LargeText: a.Assets.LargeText,
				SmallImage: a.Assets.SmallImage, SmallText: a.Assets.SmallText,
			}
		}
		if a.Timestamps != nil {
			out[i].Timestamps = &eventbus.PresenceActivityTimestamps{
				Start: a.Timestamps.Start, End: a.Timestamps.End,
			}
		}
	}
	return out
}

// canViewerSeeActivity viewer 是否可看到 subject 的 activities（不持 m.mu）。
func (m *Manager) canViewerSeeActivity(viewerID, subjectID uuid.UUID) bool {
	if viewerID == subjectID {
		return true
	}
	m.mu.Lock()
	fn := m.activityAudience
	m.mu.Unlock()
	if fn == nil {
		return true
	}
	showTo, friends := fn(subjectID)
	switch showTo {
	case ShowActivityNobody:
		return false
	case ShowActivityFriends:
		_, ok := friends[viewerID]
		return ok
	default:
		return true
	}
}

// Displayed 返回 viewer 视角下一组用户的状态。
// 先在锁内拷贝合并结果，再在锁外按 show_activity_to 剥离 activities（避免查库持锁）。
func (m *Manager) Displayed(viewerID uuid.UUID, userIDs []uuid.UUID) map[uuid.UUID]Info {
	now := time.Now().UTC()
	type raw struct {
		id     uuid.UUID
		status string
		custom CustomStatus
		acts   []Activity
	}
	raws := make([]raw, 0, len(userIDs))
	m.mu.Lock()
	for _, id := range userIDs {
		state, ok := m.users[id]
		if !ok {
			continue
		}
		status, custom, acts := state.merged(), state.effectiveCustom(now), cloneActivities(state.mergedActivities())
		if id != viewerID {
			status, custom, acts = mask(status, custom, acts)
		}
		if status == StatusOffline {
			continue
		}
		raws = append(raws, raw{id: id, status: status, custom: custom, acts: acts})
	}
	m.mu.Unlock()

	result := make(map[uuid.UUID]Info, len(raws))
	for _, r := range raws {
		acts := r.acts
		if r.id != viewerID && len(acts) > 0 && !m.canViewerSeeActivity(viewerID, r.id) {
			acts = nil
		}
		result[r.id] = Info{
			Status: r.status, CustomText: r.custom.Text,
			CustomEmoji: r.custom.Emoji, CustomExpiresAt: r.custom.ExpiresAt,
			Activities: acts,
		}
	}
	return result
}
