package activity

// 集成测试：需要真实 PostgreSQL（与 userapi/cosmetics 集成测试同约定）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/activity/
//
// 未设置时自动跳过。测试直接构造 service（不经 ensureService），
// 不启动 flush/采样/结算后台 goroutine，保证断言确定性。

import (
	"fmt"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type intEnv struct {
	db     *gorm.DB
	svc    *service
	events *intCollector
}

type intCollector struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (c *intCollector) handle(event eventbus.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *intCollector) wait(t *testing.T, desc string, match func(eventbus.Event) bool) eventbus.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, e := range c.events {
			if match(e) {
				c.mu.Unlock()
				return e
			}
		}
		c.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待事件超时: %s", desc)
	return eventbus.Event{}
}

func newIntEnv(t *testing.T) *intEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	bus := eventbus.New()
	collector := &intCollector{}
	bus.Subscribe(collector.handle)
	svc := &service{db: db, bus: bus}
	svc.tracker = newTracker(svc)
	svc.loadConfig()
	return &intEnv{db: db, svc: svc, events: collector}
}

func (env *intEnv) newUser(t *testing.T, isBot bool) model.User {
	t.Helper()
	name := fmt.Sprintf("act%d%04d", time.Now().UnixNano()%1e10, rand.Intn(10000))
	user := model.User{
		ID: uuid.New(), Username: name, Email: name + "@test.local",
		PasswordHash: "x", IsBot: isBot,
	}
	if err := env.db.Create(&user).Error; err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	return user
}

func (env *intEnv) dayRow(t *testing.T, userID uuid.UUID, day string) model.UserActivityDay {
	t.Helper()
	var row model.UserActivityDay
	if err := env.db.First(&row, "user_id = ? AND day = ?", userID, day).Error; err != nil {
		t.Fatalf("查每日行失败 user=%s day=%s: %v", userID, day, err)
	}
	return row
}

func TestFlushAccumulates(t *testing.T) {
	env := newIntEnv(t)
	user := env.newUser(t, false)
	cfg := env.svc.config()
	today := bizDay(nowFunc(), cfg.DayOffsetMinutes)

	env.svc.tracker.track(user.ID, dimMessage, 2)
	env.svc.tracker.track(user.ID, dimReaction, 1)
	env.svc.tracker.track(user.ID, dimLogin, 1)
	env.svc.tracker.flushOnce()

	row := env.dayRow(t, user.ID, today)
	if row.MsgCount != 2 || row.ReactionCount != 1 || row.LoginCount != 1 {
		t.Fatalf("首轮 flush 计数异常: %+v", row)
	}

	// 第二轮增量累加而非覆盖
	env.svc.tracker.track(user.ID, dimMessage, 3)
	env.svc.tracker.track(user.ID, dimVoiceMinute, 5)
	env.svc.tracker.flushOnce()
	row = env.dayRow(t, user.ID, today)
	if row.MsgCount != 5 || row.VoiceMinutes != 5 || row.ReactionCount != 1 {
		t.Fatalf("第二轮 flush 应增量累加: %+v", row)
	}

	// flush 后发 ACTIVITY_UPDATE 定向事件
	env.events.wait(t, "ACTIVITY_UPDATE", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventActivityUpdate && len(e.UserIDs) == 1 && e.UserIDs[0] == user.ID
	})
}

func TestSettleIdempotentWithBonusAndLevelUp(t *testing.T) {
	env := newIntEnv(t)
	user := env.newUser(t, false)
	yesterday := bizDay(nowFunc().Add(-24*time.Hour), env.svc.config().DayOffsetMinutes)

	// 造昨日活跃：60 条消息 + 100 分钟语音 + 10 反应 + 登录 = 600+200+10+20 = 830 分
	day := model.UserActivityDay{
		UserID: user.ID, Day: yesterday,
		MsgCount: 60, VoiceMinutes: 100, ReactionCount: 10, LoginCount: 1,
		CreatedAt: nowFunc(), UpdatedAt: nowFunc(),
	}
	if err := env.db.Create(&day).Error; err != nil {
		t.Fatalf("造每日行失败: %v", err)
	}
	// 预置汇总：Lv10（5500 分），加成 20%；830 分结算后 6330 分应升至 Lv10 以上验证升级事件
	// 默认曲线 Lv10=5500、Lv11=6600：6330 未到 Lv11 → 不该升级。改造总分让其跨级：
	// 预置 6500，结算后 7330 ≥ 6600 → Lv11，触发 LEVEL_UP。
	stat := model.UserActivityStat{UserID: user.ID, TotalScore: 6500, Level: 10, UpdatedAt: nowFunc()}
	if err := env.db.Create(&stat).Error; err != nil {
		t.Fatalf("造汇总行失败: %v", err)
	}

	settled, err := env.svc.runSettleOnce()
	if err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	if settled < 1 {
		t.Fatalf("应至少结算 1 行，实际 %d", settled)
	}

	row := env.dayRow(t, user.ID, yesterday)
	if !row.Granted || row.Score != 830 {
		t.Fatalf("结算后行状态异常: granted=%v score=%d", row.Granted, row.Score)
	}
	// 加成按结算前 Lv10（+20%）：floor(830×0.1×1.2) = 99
	if row.GrantedPoints != 99 {
		t.Fatalf("发放积分应为 99（Lv10 +20%%），实际 %d", row.GrantedPoints)
	}
	// ledger 恰好一条 refID=业务日
	var ledgerCount int64
	env.db.Model(&model.UserPointsLedger{}).
		Where("user_id = ? AND reason = ? AND ref_id = ?", user.ID, "activity_daily", yesterday).
		Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("积分流水应恰好 1 条，实际 %d", ledgerCount)
	}
	var points model.UserPoints
	if err := env.db.First(&points, "user_id = ?", user.ID).Error; err != nil || points.Balance != 99 {
		t.Fatalf("余额应为 99，实际 %+v err=%v", points, err)
	}
	// 汇总与升级
	var freshStat model.UserActivityStat
	_ = env.db.First(&freshStat, "user_id = ?", user.ID).Error
	if freshStat.TotalScore != 7330 || freshStat.Level != 11 {
		t.Fatalf("汇总应 7330 分 Lv11，实际 %d 分 Lv%d", freshStat.TotalScore, freshStat.Level)
	}
	env.events.wait(t, "ACTIVITY_LEVEL_UP", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventActivityLevelUp && len(e.UserIDs) == 1 && e.UserIDs[0] == user.ID
	})
	env.events.wait(t, "COSMETIC_POINTS_UPDATE(delta)", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventCosmeticPointsUpdate && len(e.UserIDs) == 1 && e.UserIDs[0] == user.ID
	})

	// 幂等：重复结算不重复发分、总分不重复累计
	if _, err := env.svc.runSettleOnce(); err != nil {
		t.Fatalf("重复结算失败: %v", err)
	}
	env.db.Model(&model.UserPointsLedger{}).
		Where("user_id = ? AND reason = ?", user.ID, "activity_daily").Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("重复结算后流水应仍为 1 条，实际 %d", ledgerCount)
	}
	_ = env.db.First(&freshStat, "user_id = ?", user.ID).Error
	if freshStat.TotalScore != 7330 {
		t.Fatalf("重复结算后总分不应变化，实际 %d", freshStat.TotalScore)
	}
}

func TestSettleSkipsTodayAndDisabled(t *testing.T) {
	env := newIntEnv(t)
	user := env.newUser(t, false)
	cfg := env.svc.config()
	today := bizDay(nowFunc(), cfg.DayOffsetMinutes)
	day := model.UserActivityDay{
		UserID: user.ID, Day: today, MsgCount: 10,
		CreatedAt: nowFunc(), UpdatedAt: nowFunc(),
	}
	if err := env.db.Create(&day).Error; err != nil {
		t.Fatalf("造今日行失败: %v", err)
	}
	if _, err := env.svc.runSettleOnce(); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	if row := env.dayRow(t, user.ID, today); row.Granted {
		t.Fatal("今日行不应被结算（业务日未过界）")
	}
}

func TestVoiceSampleFilters(t *testing.T) {
	env := newIntEnv(t)
	human := env.newUser(t, false)
	bot := env.newUser(t, true)
	stealth := env.newUser(t, false)
	offline := env.newUser(t, false)
	guildID := uuid.New()
	channelID := uuid.New()

	mkState := func(userID uuid.UUID, channel *uuid.UUID, connected bool) {
		vs := model.VoiceState{
			ID: uuid.New(), GuildID: guildID, UserID: userID,
			ChannelID: channel, Connected: connected,
		}
		if err := env.db.Create(&vs).Error; err != nil {
			t.Fatalf("造语音状态失败: %v", err)
		}
	}
	mkState(human.ID, &channelID, true)
	mkState(bot.ID, &channelID, true)      // bot：排除
	mkState(stealth.ID, &channelID, true)  // 隐身：排除
	mkState(offline.ID, &channelID, false) // 未连接：排除

	origStealth := StealthCheck
	StealthCheck = func(g, u uuid.UUID) bool { return u == stealth.ID }
	defer func() { StealthCheck = origStealth }()

	env.svc.sampleOnce()

	env.svc.tracker.mu.Lock()
	defer env.svc.tracker.mu.Unlock()
	minutes := func(userID uuid.UUID) int {
		total := 0
		for key, c := range env.svc.tracker.buckets {
			if key.UserID == userID {
				total += c[dimVoiceMinute]
			}
		}
		return total
	}
	if minutes(human.ID) != 1 {
		t.Fatalf("正常用户应记 1 分钟，实际 %d", minutes(human.ID))
	}
	for name, id := range map[string]uuid.UUID{"bot": bot.ID, "隐身": stealth.ID, "未连接": offline.ID} {
		if minutes(id) != 0 {
			t.Fatalf("%s 用户不应计入语音活跃", name)
		}
	}
}

func TestHooksExcludeBotAndNilService(t *testing.T) {
	env := newIntEnv(t)
	bot := env.newUser(t, true)
	human := env.newUser(t, false)

	// 临时接管包级单例（还原以免影响其他测试）
	origShared := sharedService
	sharedService = env.svc
	defer func() { sharedService = origShared }()

	TrackMessage(bot)
	TrackReaction(bot)
	TrackLogin(bot)
	TrackMessage(human)

	env.svc.tracker.mu.Lock()
	botTotal, humanMsg := 0, 0
	for key, c := range env.svc.tracker.buckets {
		if key.UserID == bot.ID {
			for i := 0; i < int(dimCount); i++ {
				botTotal += c[i]
			}
		}
		if key.UserID == human.ID {
			humanMsg += c[dimMessage]
		}
	}
	env.svc.tracker.mu.Unlock()
	if botTotal != 0 {
		t.Fatalf("bot 不应累计任何活跃度，实际 %d", botTotal)
	}
	if humanMsg != 1 {
		t.Fatalf("真人消息应计 1，实际 %d", humanMsg)
	}

	// 单例为 nil 时静默忽略不 panic
	sharedService = nil
	TrackMessage(human)
	TrackReaction(human)
	TrackLogin(human)
}

func TestLevelOfProjection(t *testing.T) {
	env := newIntEnv(t)
	user := env.newUser(t, false)
	if got := LevelOf(env.db, user.ID); got != 0 {
		t.Fatalf("无记录用户等级应 0，实际 %d", got)
	}
	stat := model.UserActivityStat{UserID: user.ID, TotalScore: 5500, Level: 10, UpdatedAt: nowFunc()}
	if err := env.db.Create(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if got := LevelOf(env.db, user.ID); got != 10 {
		t.Fatalf("等级投影应 10，实际 %d", got)
	}
}
