package voice

import (
	"testing"

	"github.com/newtspeak/newt-server/backend/internal/model"
)

// TestNextMigrationState 五段状态机转换表驱动（docs 09 §5.1）。
func TestNextMigrationState(t *testing.T) {
	cases := []struct {
		name  string
		state string
		ev    migrationEvent
		want  string
		ok    bool
	}{
		{"排队启动进 PREPARE", model.MigrationStateQueued, evStart, model.MigrationStatePrepare, true},
		{"PREPARE 完成进 CONNECT", model.MigrationStatePrepare, evPrepared, model.MigrationStateConnect, true},
		{"PREPARE 失败进 FAILED", model.MigrationStatePrepare, evFail, model.MigrationStateFailed, true},
		{"CONNECT 客户端确认进 CUTOVER", model.MigrationStateConnect, evConnectAck, model.MigrationStateCutover, true},
		{"CONNECT 超时也自动推进", model.MigrationStateConnect, evConnectTimeout, model.MigrationStateCutover, true},
		{"CONNECT 失败进 FAILED", model.MigrationStateConnect, evFail, model.MigrationStateFailed, true},
		{"CUTOVER 完成进 CLEANUP", model.MigrationStateCutover, evCutoverDone, model.MigrationStateCleanup, true},
		{"CLEANUP 完成进 DONE", model.MigrationStateCleanup, evCleanupDone, model.MigrationStateDone, true},
		{"FAILED 可重试回 QUEUED", model.MigrationStateFailed, evRetry, model.MigrationStateQueued, true},
		{"进行中可取消", model.MigrationStateConnect, evCancel, model.MigrationStateCanceled, true},
		{"FAILED 可取消", model.MigrationStateFailed, evCancel, model.MigrationStateCanceled, true},
		{"DONE 不可取消", model.MigrationStateDone, evCancel, model.MigrationStateDone, false},
		{"非法转换被拒：QUEUED 直接 ack", model.MigrationStateQueued, evConnectAck, model.MigrationStateQueued, false},
		{"非法转换被拒：DONE 再推进", model.MigrationStateDone, evCleanupDone, model.MigrationStateDone, false},
		{"非法转换被拒：PREPARE 跳 CUTOVER", model.MigrationStatePrepare, evCutoverDone, model.MigrationStatePrepare, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nextMigrationState(tc.state, tc.ev)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("nextMigrationState(%s,%s)=(%s,%v)，期望 (%s,%v)", tc.state, tc.ev, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestMigrationPriority 死亡 > 分区 > Drain > 手动 > 过载（docs 09 K.4）。
func TestMigrationPriority(t *testing.T) {
	order := []string{
		model.MigrationReasonDeath, model.MigrationReasonPartition,
		model.MigrationReasonDrain, model.MigrationReasonManual, model.MigrationReasonOverload,
	}
	for i := 0; i < len(order)-1; i++ {
		if migrationPriority(order[i]) <= migrationPriority(order[i+1]) {
			t.Fatalf("%s 优先级应高于 %s", order[i], order[i+1])
		}
	}
}

// TestRetryBackoff 前 3 次快速重试无退避，之后指数退避且封顶。
func TestRetryBackoff(t *testing.T) {
	for attempt := 1; attempt < maxQuickRetries; attempt++ {
		if retryBackoff(attempt) != 0 {
			t.Fatalf("attempt=%d 应立即换目标重试", attempt)
		}
	}
	prev := retryBackoff(maxQuickRetries)
	if prev <= 0 {
		t.Fatal("超过快速重试上限后应有退避")
	}
	for attempt := maxQuickRetries + 1; attempt < maxQuickRetries+6; attempt++ {
		cur := retryBackoff(attempt)
		if cur < prev {
			t.Fatalf("退避应单调不减：attempt=%d %v < %v", attempt, cur, prev)
		}
		prev = cur
	}
	if retryBackoff(100) > retryBackoff(101) || retryBackoff(100) <= 0 {
		t.Fatal("退避应封顶且非负")
	}
}

// TestIsTerminalMigrationState 终态判定。
func TestIsTerminalMigrationState(t *testing.T) {
	if !isTerminalMigrationState(model.MigrationStateDone) || !isTerminalMigrationState(model.MigrationStateCanceled) {
		t.Fatal("DONE/CANCELED 是终态")
	}
	for _, s := range []string{model.MigrationStateQueued, model.MigrationStatePrepare, model.MigrationStateConnect, model.MigrationStateFailed} {
		if isTerminalMigrationState(s) {
			t.Fatalf("%s 不是终态", s)
		}
	}
}
