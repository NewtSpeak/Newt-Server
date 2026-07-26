package sfudeploy

import (
	"strings"
	"sync"
)

// logLimit 单次部署保留的日志上限；超出后从头截断并标注。
const logLimit = 256 * 1024

// logBuffer 累积部署日志并支持按 offset 取增量。
// offset 语义为「自部署开始累计写入的字节数」，前端据此发现事件空洞并回拉补齐。
type logBuffer struct {
	mu       sync.Mutex
	buf      strings.Builder
	written  int
	truncate bool
}

func newLogBuffer() *logBuffer { return &logBuffer{} }

// Append 追加一行（自动补换行），返回追加后的累计 offset。
func (b *logBuffer) Append(line string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(line)
	b.buf.WriteByte('\n')
	b.written += len(line) + 1
	if b.buf.Len() > logLimit {
		content := b.buf.String()
		cut := len(content) - logLimit/2
		if idx := strings.IndexByte(content[cut:], '\n'); idx >= 0 {
			cut += idx + 1
		}
		b.buf.Reset()
		b.buf.WriteString(content[cut:])
		b.truncate = true
	}
	return b.written
}

// Snapshot 返回当前保留的全部日志与累计 offset。
func (b *logBuffer) Snapshot() (string, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	content := b.buf.String()
	if b.truncate {
		content = "…（较早日志已截断）\n" + content
	}
	return content, b.written
}
