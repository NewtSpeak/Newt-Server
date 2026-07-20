package gateway

// session 回放环形缓冲的纯逻辑单元测试（不依赖 WebSocket / 数据库）：
// 序列号递增、条数上限淘汰、TTL 淘汰、缺口判定与 resumeAttach 语义。

import (
	"encoding/json"
	"testing"
	"time"
)

// dispatchN 向会话投递 n 条事件（无连接绑定，只进缓冲）。
func dispatchN(t *testing.T, sess *session, n int, limit int, ttl time.Duration) {
	t.Helper()
	for i := 0; i < n; i++ {
		delivered, target := sess.dispatch("TEST_EVENT", json.RawMessage(`{}`), limit, ttl)
		if !delivered || target != nil {
			t.Fatalf("无连接 dispatch 应恒 delivered=true/target=nil，实际 %v %v", delivered, target)
		}
	}
}

func TestSessionSeqMonotonicAndBufferLimit(t *testing.T) {
	sess := &session{id: "s1"}
	dispatchN(t, sess, 10, 4, time.Minute)
	if sess.seq != 10 {
		t.Fatalf("seq = %d，期待 10", sess.seq)
	}
	if len(sess.buffer) != 4 {
		t.Fatalf("缓冲长度 = %d，期待上限 4", len(sess.buffer))
	}
	// 保留的应是最新的 4 条（seq 7..10）。
	if sess.buffer[0].seq != 7 || sess.buffer[3].seq != 10 {
		t.Fatalf("缓冲区间 = [%d, %d]，期待 [7, 10]", sess.buffer[0].seq, sess.buffer[3].seq)
	}
}

func TestSessionBufferTTLPrune(t *testing.T) {
	sess := &session{id: "s1"}
	dispatchN(t, sess, 3, 512, time.Minute)
	// 手动把前两条做旧，再投递一条触发 prune。
	sess.mu.Lock()
	sess.buffer[0].at = time.Now().Add(-2 * time.Minute)
	sess.buffer[1].at = time.Now().Add(-90 * time.Second)
	sess.mu.Unlock()
	dispatchN(t, sess, 1, 512, time.Minute)
	if len(sess.buffer) != 2 {
		t.Fatalf("TTL 淘汰后缓冲长度 = %d，期待 2（seq 3、4）", len(sess.buffer))
	}
	if sess.buffer[0].seq != 3 {
		t.Fatalf("TTL 淘汰后最老 seq = %d，期待 3", sess.buffer[0].seq)
	}
}

func TestSessionReplayAfter(t *testing.T) {
	sess := &session{id: "s1"}
	dispatchN(t, sess, 10, 4, time.Minute) // 缓冲仅剩 seq 7..10

	// last_seq=8 → 补发 9、10。
	sess.mu.Lock()
	frames, ok := sess.replayAfterLocked(8)
	sess.mu.Unlock()
	if !ok || len(frames) != 2 {
		t.Fatalf("last_seq=8 应补发 2 条，实际 ok=%v n=%d", ok, len(frames))
	}

	// last_seq=10（无缺口）→ 补发 0 条但成功。
	sess.mu.Lock()
	frames, ok = sess.replayAfterLocked(10)
	sess.mu.Unlock()
	if !ok || len(frames) != 0 {
		t.Fatalf("last_seq=seq 应 ok 且无补发，实际 ok=%v n=%d", ok, len(frames))
	}

	// last_seq=5：seq 6 已被淘汰，存在缺口 → 失败。
	sess.mu.Lock()
	_, ok = sess.replayAfterLocked(5)
	sess.mu.Unlock()
	if ok {
		t.Fatal("超出回放窗口（缺口）应失败")
	}

	// last_seq=6：缓冲最老 seq=7=last_seq+1，恰好无缺口 → 补发 7..10。
	sess.mu.Lock()
	frames, ok = sess.replayAfterLocked(6)
	sess.mu.Unlock()
	if !ok || len(frames) != 4 {
		t.Fatalf("last_seq=6 应补发 4 条，实际 ok=%v n=%d", ok, len(frames))
	}

	// last_seq 超前于当前 seq / 为负 → 失败。
	sess.mu.Lock()
	_, okAhead := sess.replayAfterLocked(11)
	_, okNeg := sess.replayAfterLocked(-1)
	sess.mu.Unlock()
	if okAhead || okNeg {
		t.Fatalf("非法 last_seq 应失败：ahead=%v neg=%v", okAhead, okNeg)
	}
}

func TestSessionResumeAttachReplacesConn(t *testing.T) {
	sess := &session{id: "s1"}
	dispatchN(t, sess, 3, 512, time.Minute)

	oldConn := &conn{done: make(chan struct{}), send: make(chan []byte, 4)}
	sess.mu.Lock()
	sess.conn = oldConn
	sess.mu.Unlock()

	newConn := &conn{done: make(chan struct{}), send: make(chan []byte, 4)}
	frames, replaced, ok := sess.resumeAttach(1, newConn)
	if !ok || len(frames) != 2 {
		t.Fatalf("resumeAttach 应补发 seq 2、3，实际 ok=%v n=%d", ok, len(frames))
	}
	if replaced != oldConn {
		t.Fatal("resumeAttach 应返回被顶替的旧连接")
	}
	sess.mu.Lock()
	attached := sess.conn
	sess.mu.Unlock()
	if attached != newConn {
		t.Fatal("resumeAttach 应绑定新连接")
	}

	// 失败路径（seq 超前）不得改变绑定。
	another := &conn{done: make(chan struct{}), send: make(chan []byte, 4)}
	if _, _, ok := sess.resumeAttach(99, another); ok {
		t.Fatal("超前 last_seq 应失败")
	}
	sess.mu.Lock()
	attached = sess.conn
	sess.mu.Unlock()
	if attached != newConn {
		t.Fatal("失败的 resumeAttach 不应顶掉现有连接")
	}
}

func TestSessionExpired(t *testing.T) {
	sess := &session{id: "s1"}
	now := time.Now()
	// 有连接 → 永不过期。
	sess.conn = &conn{done: make(chan struct{})}
	if sess.expired(now, time.Second) {
		t.Fatal("有连接的会话不应过期")
	}
	// 断开 30s、窗口 60s → 未过期；窗口 10s → 过期。
	sess.conn = nil
	sess.disconnectedAt = now.Add(-30 * time.Second)
	if sess.expired(now, 60*time.Second) {
		t.Fatal("窗口内不应过期")
	}
	if !sess.expired(now, 10*time.Second) {
		t.Fatal("超窗应过期")
	}
}
