package activity

// tracker 内存聚合：信号入口只加内存计数，flush 周期批量 UPSERT 增量落库，
// 避免每条消息一次 DB 写。单实例部署，崩溃丢失窗口 ≤ flushInterval，可接受。

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const flushInterval = 30 * time.Second

type dimension int

const (
	dimMessage dimension = iota
	dimVoiceMinute
	dimReaction
	dimLogin
	dimCount
)

// bucketKey 归桶键：Track 时按事件时刻算业务日，跨界瞬间新旧两桶并存、各写各行。
type bucketKey struct {
	UserID uuid.UUID
	Day    string
}

type tracker struct {
	svc     *service
	mu      sync.Mutex
	buckets map[bucketKey]*[dimCount]int
	// msgGate 消息计分限流：30s 内多条只计 1 条（防连发刷分）。
	msgGate *userLimiter
}

func newTracker(svc *service) *tracker {
	return &tracker{
		svc:     svc,
		buckets: make(map[bucketKey]*[dimCount]int),
		msgGate: newUserLimiter(1.0/30.0, 1),
	}
}

// track 累加一个活跃度信号；Enabled=false 时短路。
func (t *tracker) track(userID uuid.UUID, dim dimension, n int) {
	cfg := t.svc.config()
	if !cfg.Enabled || n <= 0 {
		return
	}
	key := bucketKey{UserID: userID, Day: bizDay(nowFunc(), cfg.DayOffsetMinutes)}
	// 内存桶防灌水上限：超过 10×日上限后丢弃（结算时反正会被截断）。
	guard := t.dimGuard(dim, cfg)
	t.mu.Lock()
	defer t.mu.Unlock()
	counts, ok := t.buckets[key]
	if !ok {
		counts = &[dimCount]int{}
		t.buckets[key] = counts
	}
	if guard > 0 && counts[dim] >= guard {
		return
	}
	counts[dim] += n
}

// dimGuard 单桶（单 flush 周期内）计数护栏 = 10 × 日上限。
func (t *tracker) dimGuard(dim dimension, cfg *resolvedConfig) int {
	switch dim {
	case dimMessage:
		return cfg.CapMessage * 10
	case dimVoiceMinute:
		return cfg.CapVoiceMinutes * 10
	case dimReaction:
		return cfg.CapReactions * 10
	case dimLogin:
		return 10
	default:
		return 0
	}
}

func (t *tracker) trackMessage(userID uuid.UUID) {
	if !t.msgGate.Allow(userID) {
		return
	}
	t.track(userID, dimMessage, 1)
}

// flushLoop 周期落库（每轮先刷新配置缓存，顺带淘汰闲置令牌桶）。
func (t *tracker) flushLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		t.svc.loadConfig()
		t.flushOnce()
		t.msgGate.prune(24 * time.Hour)
	}
}

// flushOnce swap 内存桶后锁外逐桶 UPSERT 增量；失败的桶合并回下轮重试。
// 成功落库的用户随后定向发 ACTIVITY_UPDATE（30s 周期天然节流）。
func (t *tracker) flushOnce() {
	t.mu.Lock()
	pending := t.buckets
	t.buckets = make(map[bucketKey]*[dimCount]int)
	t.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	cfg := t.svc.config()
	now := nowFunc()
	for key, counts := range pending {
		row := model.UserActivityDay{
			UserID: key.UserID, Day: key.Day,
			MsgCount: counts[dimMessage], VoiceMinutes: counts[dimVoiceMinute],
			ReactionCount: counts[dimReaction], LoginCount: counts[dimLogin],
			CreatedAt: now, UpdatedAt: now,
		}
		err := t.svc.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "day"}},
			DoUpdates: clause.Assignments(map[string]any{
				"msg_count":      gorm.Expr("user_activity_days.msg_count + ?", counts[dimMessage]),
				"voice_minutes":  gorm.Expr("user_activity_days.voice_minutes + ?", counts[dimVoiceMinute]),
				"reaction_count": gorm.Expr("user_activity_days.reaction_count + ?", counts[dimReaction]),
				"login_count":    gorm.Expr("user_activity_days.login_count + ?", counts[dimLogin]),
				"updated_at":     now,
			}),
		}).Create(&row).Error
		if err != nil {
			log.Printf("activity: flush 落库失败（并回下轮重试）user=%s day=%s: %v", key.UserID, key.Day, err)
			t.mergeBack(key, counts)
			continue
		}
		// 回读累计后的行用于实时推送（PK 点查，开销可忽略）。
		var fresh model.UserActivityDay
		if e := t.svc.db.First(&fresh, "user_id = ? AND day = ?", key.UserID, key.Day).Error; e == nil {
			t.svc.publishActivityUpdate(key.UserID, fresh, cfg)
		}
	}
}

func (t *tracker) mergeBack(key bucketKey, counts *[dimCount]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	existing, ok := t.buckets[key]
	if !ok {
		t.buckets[key] = counts
		return
	}
	for i := 0; i < int(dimCount); i++ {
		existing[i] += counts[i]
	}
}
