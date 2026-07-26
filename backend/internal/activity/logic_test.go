package activity

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

func cfgWith(setting model.ActivitySetting) *resolvedConfig {
	return &resolvedConfig{ActivitySetting: setting, Thresholds: parseThresholds(setting.LevelThresholdsJSON)}
}

func TestBizDay(t *testing.T) {
	// UTC 2026-07-25 23:00 在 UTC+8 已是 07-26；UTC 2026-07-25 15:59 仍是 07-25。
	cases := []struct {
		utc    string
		offset int
		want   string
	}{
		{"2026-07-25T23:00:00Z", 480, "2026-07-26"},
		{"2026-07-25T15:59:00Z", 480, "2026-07-25"},
		{"2026-07-25T16:00:00Z", 480, "2026-07-26"}, // 北京 0 点整
		{"2026-07-25T23:59:00Z", 0, "2026-07-25"},
		{"2026-12-31T20:00:00Z", 480, "2027-01-01"}, // 跨年
		{"2026-07-26T02:00:00Z", -480, "2026-07-25"}, // 负偏移（UTC-8）
	}
	for _, tc := range cases {
		ts, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatal(err)
		}
		if got := bizDay(ts, tc.offset); got != tc.want {
			t.Fatalf("bizDay(%s, %d) = %s，期待 %s", tc.utc, tc.offset, got, tc.want)
		}
	}
}

func TestLevelFor(t *testing.T) {
	thresholds := []int64{100, 300, 600}
	cases := []struct {
		total int64
		want  int
	}{
		{0, 0}, {99, 0}, {100, 1}, {299, 1}, {300, 2}, {600, 3}, {10000, 3},
	}
	for _, tc := range cases {
		if got := levelFor(tc.total, thresholds); got != tc.want {
			t.Fatalf("levelFor(%d) = %d，期待 %d", tc.total, got, tc.want)
		}
	}
}

func TestParseThresholdsFallback(t *testing.T) {
	def := defaultThresholds()
	if len(def) != maxLevel || def[0] != 100 || def[maxLevel-1] != 127500 {
		t.Fatalf("默认曲线异常: len=%d first=%d last=%d", len(def), def[0], def[maxLevel-1])
	}
	// 空/非法/非递增均回退默认
	for _, raw := range []string{"", "[]", "not-json", "[100,100]", "[300,100]", "[0,100]"} {
		got := parseThresholds(raw)
		if len(got) != maxLevel || got[0] != def[0] {
			t.Fatalf("parseThresholds(%q) 应回退默认曲线", raw)
		}
	}
	// 合法数组原样返回
	got := parseThresholds("[10,20,30]")
	if len(got) != 3 || got[2] != 30 {
		t.Fatalf("合法门槛数组解析错误: %v", got)
	}
}

func TestScoreOfCaps(t *testing.T) {
	cfg := cfgWith(defaultSetting())
	row := model.UserActivityDay{
		MsgCount: 100, VoiceMinutes: 500, ReactionCount: 100, LoginCount: 5,
	}
	// 全部触顶：60×10 + 240×2 + 30×1 + 1×20 = 1130
	if got := scoreOf(row, cfg); got != 1130 {
		t.Fatalf("触顶得分 = %d，期待 1130", got)
	}
	// 未触顶按实际计
	row = model.UserActivityDay{MsgCount: 3, VoiceMinutes: 10, ReactionCount: 2, LoginCount: 1}
	if got := scoreOf(row, cfg); got != 3*10+10*2+2+20 {
		t.Fatalf("未触顶得分 = %d，期待 %d", got, 3*10+10*2+2+20)
	}
	if got := scoreOf(model.UserActivityDay{}, cfg); got != 0 {
		t.Fatalf("零活跃应 0 分，实际 %d", got)
	}
}

func TestBonusAndPoints(t *testing.T) {
	cfg := cfgWith(defaultSetting()) // rate=0.1, 每级 2%, 封顶 100%
	if got := bonusPct(0, cfg); got != 0 {
		t.Fatalf("0 级加成应 0，实际 %v", got)
	}
	if got := bonusPct(10, cfg); got != 20 {
		t.Fatalf("10 级加成应 20%%，实际 %v", got)
	}
	if got := bonusPct(100, cfg); got != 100 {
		t.Fatalf("加成应封顶 100%%，实际 %v", got)
	}
	// floor 语义：300 分 0 级 → 30；300 分 10 级 → floor(300×0.1×1.2)=36
	if got := pointsFor(300, 0, cfg); got != 30 {
		t.Fatalf("300 分 0 级应 30 积分，实际 %d", got)
	}
	if got := pointsFor(300, 10, cfg); got != 36 {
		t.Fatalf("300 分 10 级应 36 积分，实际 %d", got)
	}
	// floor 显式断言：105 分 0 级 → floor(10.5)=10
	if got := pointsFor(105, 0, cfg); got != 10 {
		t.Fatalf("105 分应 floor 到 10 积分，实际 %d", got)
	}
	if got := pointsFor(0, 5, cfg); got != 0 {
		t.Fatalf("0 分应 0 积分，实际 %d", got)
	}
}

func TestTrackerCrossDayBuckets(t *testing.T) {
	svc := &service{}
	svc.cfg.Store(cfgWith(defaultSetting()))
	tr := newTracker(svc)

	// 注入假时钟：北京时间 23:59:50 → 业务日 D；随后 00:00:10 → 业务日 D+1
	base, _ := time.Parse(time.RFC3339, "2026-07-25T15:59:50Z") // 北京 23:59:50
	current := base
	orig := nowFunc
	nowFunc = func() time.Time { return current }
	defer func() { nowFunc = orig }()

	userID := uuid.New()
	tr.track(userID, dimReaction, 1)
	current = base.Add(20 * time.Second) // 跨过北京 0 点
	tr.track(userID, dimReaction, 1)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.buckets) != 2 {
		t.Fatalf("跨天应产生两个桶，实际 %d", len(tr.buckets))
	}
	if _, ok := tr.buckets[bucketKey{UserID: userID, Day: "2026-07-25"}]; !ok {
		t.Fatal("缺少跨天前的业务日桶")
	}
	if _, ok := tr.buckets[bucketKey{UserID: userID, Day: "2026-07-26"}]; !ok {
		t.Fatal("缺少跨天后的业务日桶")
	}
}

func TestTrackerMessageGate(t *testing.T) {
	svc := &service{}
	svc.cfg.Store(cfgWith(defaultSetting()))
	tr := newTracker(svc)
	userID := uuid.New()
	// 30s 窗口内连发 5 条只计 1 条
	for i := 0; i < 5; i++ {
		tr.trackMessage(userID)
	}
	tr.mu.Lock()
	total := 0
	for _, c := range tr.buckets {
		total += c[dimMessage]
	}
	tr.mu.Unlock()
	if total != 1 {
		t.Fatalf("30s 内连发应只计 1 条，实际 %d", total)
	}
}

func TestTrackerDisabledShortCircuit(t *testing.T) {
	setting := defaultSetting()
	setting.Enabled = false
	svc := &service{}
	svc.cfg.Store(cfgWith(setting))
	tr := newTracker(svc)
	tr.track(uuid.New(), dimReaction, 1)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.buckets) != 0 {
		t.Fatal("Enabled=false 时不应累计")
	}
}
