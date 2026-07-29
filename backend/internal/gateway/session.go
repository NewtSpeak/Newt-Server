package gateway

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/newtspeak/newt-server/backend/internal/model"
)

// dispatchRecord 回放缓冲中的单条 DISPATCH：序列号 + 已序列化帧（含 s）+ 入缓冲时间。
type dispatchRecord struct {
	seq   int64
	frame []byte
	at    time.Time
}

// session Gateway 会话：跨连接存活的 seq 计数器 + 事件回放环形缓冲。
// 连接断开后会话保留 ResumeWindow 等待 RESUME（docs 14 §7-4），超时由 hub 清理。
type session struct {
	id   string
	user model.User

	mu             sync.Mutex
	seq            int64
	buffer         []dispatchRecord // 按 seq 升序；容量/时长上限见 options
	conn           *conn            // 断开期间为 nil
	disconnectedAt time.Time        // conn 为 nil 时的断开时刻
}

// dispatch 为该会话分配序列号、写入回放缓冲，并在有连接时入队投递。
// 返回 false 表示连接积压（慢消费者），由调用方断开该连接；事件已入缓冲，
// 客户端 resume 后仍可补齐。
func (s *session) dispatch(eventType string, payload json.RawMessage, bufferLimit int, bufferTTL time.Duration) (delivered bool, target *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	frame, err := json.Marshal(outFrame{Op: opDispatch, T: eventType, S: s.seq, D: payload})
	if err != nil {
		return true, nil // 载荷已在 hub 层整体序列化过，理论不可达
	}
	now := time.Now()
	s.buffer = append(s.buffer, dispatchRecord{seq: s.seq, frame: frame, at: now})
	s.pruneLocked(now, bufferLimit, bufferTTL)
	if s.conn == nil {
		return true, nil
	}
	if !s.conn.enqueue(frame) {
		return false, s.conn
	}
	return true, nil
}

// pruneLocked 淘汰超量 / 超时的缓冲条目（须持有 s.mu）。
func (s *session) pruneLocked(now time.Time, limit int, ttl time.Duration) {
	drop := 0
	if len(s.buffer) > limit {
		drop = len(s.buffer) - limit
	}
	for drop < len(s.buffer) && now.Sub(s.buffer[drop].at) > ttl {
		drop++
	}
	if drop > 0 {
		s.buffer = append(s.buffer[:0:0], s.buffer[drop:]...)
	}
}

// detach 解绑连接（仅当当前绑定的正是该连接时生效，防止旧连接清理顶掉新连接）。
func (s *session) detach(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == c {
		s.conn = nil
		s.disconnectedAt = time.Now()
	}
}

// resumeAttach RESUME 核心：在同一临界区内「取出缺口帧 + 绑定新连接」，
// 保证并发 dispatch 的事件要么进入补发列表、要么进入新连接的发送队列，不会两头落空。
// 缺口不可补（最老缓冲之前仍有缺失）或 lastSeq 超前于当前 seq 时返回 ok=false
//（应回 INVALID_SESSION），此时不绑定连接。replaced 为被顶替的旧连接（由调用方关闭）。
func (s *session) resumeAttach(lastSeq int64, c *conn) (frames [][]byte, replaced *conn, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames, ok = s.replayAfterLocked(lastSeq)
	if !ok {
		return nil, nil, false
	}
	replaced = s.conn
	s.conn = c
	s.disconnectedAt = time.Time{}
	return frames, replaced, true
}

// replayAfterLocked 取出 seq > lastSeq 的全部缓冲帧（须持有 s.mu）。
func (s *session) replayAfterLocked(lastSeq int64) ([][]byte, bool) {
	if lastSeq > s.seq || lastSeq < 0 {
		return nil, false
	}
	if lastSeq == s.seq {
		return nil, true
	}
	// 需要补发 (lastSeq, seq] 区间；缓冲最老条目 seq 必须 ≤ lastSeq+1，否则存在缺口。
	if len(s.buffer) == 0 || s.buffer[0].seq > lastSeq+1 {
		return nil, false
	}
	frames := make([][]byte, 0, len(s.buffer))
	for _, record := range s.buffer {
		if record.seq > lastSeq {
			frames = append(frames, record.frame)
		}
	}
	return frames, true
}

// expired 判断断开中的会话是否超出 resume 保留窗口。
func (s *session) expired(now time.Time, window time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn == nil && !s.disconnectedAt.IsZero() && now.Sub(s.disconnectedAt) > window
}
