package gateway

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// conn 单条 WebSocket 连接：独立发送 goroutine + 有界缓冲，
// 慢消费者积压满即断开，避免单连接拖垮整体推送。
// 用户身份挂在 session 上（跨连接存活，见 session.go），conn 本身不持有用户信息。
type conn struct {
	ws   *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func newConn(ws *websocket.Conn, sendBuffer int) *conn {
	return &conn{ws: ws, send: make(chan []byte, sendBuffer), done: make(chan struct{})}
}

// enqueue 非阻塞入队；连接已关闭或缓冲已满返回 false（由调用方决定是否断开）。
func (c *conn) enqueue(msg []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- msg:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

// shutdown 幂等关闭：尽力发送关闭帧后关闭底层连接。
// WriteControl 允许与其他写并发（gorilla/websocket 保证），无需与发送 goroutine 互斥。
func (c *conn) shutdown(code int, reason string, writeTimeout time.Duration) {
	c.once.Do(func() {
		close(c.done)
		deadline := time.Now().Add(writeTimeout)
		_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), deadline)
		_ = c.ws.Close()
	})
}

// writePump 唯一的写 goroutine：串行消费发送队列，写失败即关闭连接。
func (c *conn) writePump(writeTimeout time.Duration) {
	for {
		select {
		case msg := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				c.shutdown(websocket.CloseInternalServerErr, "写入失败", writeTimeout)
				return
			}
		case <-c.done:
			return
		}
	}
}
