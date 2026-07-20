package sfucontrol

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestEarlyDeathEligible BI.3 组合规则：≥2 独立信号源 + ≥1 次心跳丢失（docs 15 §5）。
func TestEarlyDeathEligible(t *testing.T) {
	cfg := DefaultEarlyDeathConfig()
	interval := 5 * time.Second
	cases := []struct {
		name          string
		sinceLastSeen time.Duration
		sources       int
		want          bool
	}{
		{"信号足但心跳未丢失", 3 * time.Second, 3, false},
		{"心跳丢 1 次但仅 1 个信号源", 6 * time.Second, 1, false},
		{"心跳丢 1 次 + 2 个信号源 → 提前判死", 6 * time.Second, 2, true},
		{"心跳丢 2 次 + 3 个信号源 → 提前判死", 11 * time.Second, 3, true},
		{"无信号源不判", 12 * time.Second, 0, false},
	}
	for _, tc := range cases {
		if got := earlyDeathEligible(tc.sinceLastSeen, interval, tc.sources, cfg); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	// 开关关闭时永不提前判死。
	off := cfg
	off.Enabled = false
	if earlyDeathEligible(time.Minute, interval, 5, off) {
		t.Error("Enabled=false 时不应提前判死")
	}
	// 阈值可配：MinSources=3 时 2 个信号源不够。
	strict := cfg
	strict.MinSources = 3
	if earlyDeathEligible(6*time.Second, interval, 2, strict) {
		t.Error("MinSources=3 时 2 个信号源不应触发")
	}
	if !earlyDeathEligible(6*time.Second, interval, 3, strict) {
		t.Error("MinSources=3 时 3 个信号源应触发")
	}
}

// TestReportSuspectOriginDedup 同一 origin 重复上报只算一个独立源；不同 origin 累计。
func TestReportSuspectOriginDedup(t *testing.T) {
	registry := NewRegistry()
	nodeID := uuid.New()
	registry.Attach(nodeID, &fakeStream{})

	registry.ReportSuspect(nodeID, "edge_down:n1")
	registry.ReportSuspect(nodeID, "edge_down:n1") // 重复：只刷新时间
	if got := registry.SuspicionSources(nodeID, time.Minute); got != 1 {
		t.Fatalf("同 origin 重复上报应计 1 个信号源，got %d", got)
	}
	registry.ReportSuspect(nodeID, "client_ice:u1")
	registry.ReportSuspect(nodeID, "self_ice")
	if got := registry.SuspicionSources(nodeID, time.Minute); got != 3 {
		t.Fatalf("三个不同 origin 应计 3 个信号源，got %d", got)
	}
	// 未知节点上报被忽略（无心跳基线不参与判死）。
	unknown := uuid.New()
	registry.ReportSuspect(unknown, "edge_down:n1")
	if got := registry.SuspicionSources(unknown, time.Minute); got != 0 {
		t.Fatalf("未知节点不应累计信号，got %d", got)
	}
}

// TestSuspicionTTLExpiry 过期信号不计入组合规则。
func TestSuspicionTTLExpiry(t *testing.T) {
	registry := NewRegistry()
	nodeID := uuid.New()
	registry.Attach(nodeID, &fakeStream{})
	registry.ReportSuspect(nodeID, "edge_down:n1")
	registry.mu.Lock()
	registry.states[nodeID].suspicions["edge_down:n1"] = time.Now().Add(-time.Minute)
	registry.mu.Unlock()
	if got := registry.SuspicionSources(nodeID, 30*time.Second); got != 0 {
		t.Fatalf("过期信号应被清理，got %d", got)
	}
}

// TestMarkEarlyDead 组合规则满足 → 标记离线且仅返回一次；硬判死路径不再重复触发。
func TestMarkEarlyDead(t *testing.T) {
	registry := NewRegistry()
	interval := 5 * time.Second
	cfg := DefaultEarlyDeathConfig()

	healthy, suspect := uuid.New(), uuid.New()
	registry.Attach(healthy, &fakeStream{})
	registry.Attach(suspect, &fakeStream{})

	// suspect：2 个独立信号 + 心跳停在 6s 前（丢 1 次）。
	registry.ReportSuspect(suspect, "edge_down:n1")
	registry.ReportSuspect(suspect, "client_ice:u1")
	registry.mu.Lock()
	registry.states[suspect].lastSeen = time.Now().Add(-6 * time.Second)
	// healthy：也有信号但心跳新鲜 → 不判。
	registry.mu.Unlock()
	registry.ReportSuspect(healthy, "edge_down:n1")
	registry.ReportSuspect(healthy, "client_ice:u1")

	dead := registry.MarkEarlyDead(interval, cfg)
	if len(dead) != 1 || dead[0] != suspect {
		t.Fatalf("应提前判死 suspect 一个节点，got %v", dead)
	}
	if snapshot, _ := registry.Snapshot(suspect); snapshot.Online {
		t.Fatal("提前判死节点应离线")
	}
	if snapshot, _ := registry.Snapshot(healthy); !snapshot.Online {
		t.Fatal("心跳健康节点不应被误判")
	}
	// 已判死不重复返回；硬判死扫描也不再返回该节点。
	if again := registry.MarkEarlyDead(interval, cfg); len(again) != 0 {
		t.Fatalf("不应重复提前判死: %v", again)
	}
	registry.mu.Lock()
	registry.states[suspect].lastSeen = time.Now().Add(-time.Minute)
	registry.mu.Unlock()
	if stale := registry.MarkStale(15 * time.Second); len(stale) != 0 {
		t.Fatalf("提前判死后硬判死不应重复触发: %v", stale)
	}

	// 默认关（Enabled=false）不动作。
	off := cfg
	off.Enabled = false
	other := uuid.New()
	registry.Attach(other, &fakeStream{})
	registry.ReportSuspect(other, "a")
	registry.ReportSuspect(other, "b")
	registry.mu.Lock()
	registry.states[other].lastSeen = time.Now().Add(-6 * time.Second)
	registry.mu.Unlock()
	if dead := registry.MarkEarlyDead(interval, off); len(dead) != 0 {
		t.Fatalf("开关关闭时不应提前判死: %v", dead)
	}
}

// TestAttachClearsSuspicions 节点重连（Attach 重建状态）后信号清零——
// 瞬断重连不应带着旧指控被误判。
func TestAttachClearsSuspicions(t *testing.T) {
	registry := NewRegistry()
	nodeID := uuid.New()
	registry.Attach(nodeID, &fakeStream{})
	registry.ReportSuspect(nodeID, "edge_down:n1")
	registry.ReportSuspect(nodeID, "client_ice:u1")
	registry.Attach(nodeID, &fakeStream{}) // 重连
	if got := registry.SuspicionSources(nodeID, time.Minute); got != 0 {
		t.Fatalf("重连后旧信号应清空，got %d", got)
	}
}
