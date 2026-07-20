package owlbot

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Gateway 实时事件连接：HELLO/IDENTIFY/HEARTBEAT 自动处理，断线指数退避重连。
type Gateway struct {
	client *Client

	mu       sync.Mutex
	handlers map[string][]func(json.RawMessage)
	closed   bool
	conn     *websocket.Conn
}

type gatewayFrame struct {
	Op string          `json:"op"`
	T  string          `json:"t,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

// ConnectGateway 连接 Gateway；用 On 注册事件回调后事件即开始分发。
func (c *Client) ConnectGateway() *Gateway {
	g := &Gateway{client: c, handlers: make(map[string][]func(json.RawMessage))}
	go g.run()
	return g
}

// On 注册事件回调：事件名如 MESSAGE_CREATE / MESSAGE_STREAM_DELTA，
// 内置事件 "READY"（连接就绪）。回调收到原始 JSON 载荷。
func (g *Gateway) On(event string, handler func(payload json.RawMessage)) *Gateway {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handlers[event] = append(g.handlers[event], handler)
	return g
}

// Close 关闭连接并停止重连。
func (g *Gateway) Close() {
	g.mu.Lock()
	g.closed = true
	conn := g.conn
	g.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (g *Gateway) emit(event string, payload json.RawMessage) {
	g.mu.Lock()
	handlers := append([]func(json.RawMessage){}, g.handlers[event]...)
	g.mu.Unlock()
	for _, handler := range handlers {
		handler(payload)
	}
}

func (g *Gateway) wsURL() string {
	base := g.client.apiBase
	if strings.HasPrefix(base, "https://") {
		base = "wss://" + strings.TrimPrefix(base, "https://")
	} else {
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + "/gateway"
}

func (g *Gateway) run() {
	backoff := time.Second
	for {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return
		}
		g.mu.Unlock()

		if g.session() {
			backoff = time.Second // 会话曾就绪：重置退避
		}

		g.mu.Lock()
		closed := g.closed
		g.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// session 执行一次完整连接生命周期；返回是否曾进入 READY。
func (g *Gateway) session() bool {
	conn, _, err := websocket.DefaultDialer.Dial(g.wsURL(), nil)
	if err != nil {
		return false
	}
	g.mu.Lock()
	g.conn = conn
	g.mu.Unlock()
	defer conn.Close()

	ready := false
	stopBeat := make(chan struct{})
	defer close(stopBeat)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return ready
		}
		var frame gatewayFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		switch frame.Op {
		case "HELLO":
			identify, _ := json.Marshal(map[string]any{
				"op": "IDENTIFY", "d": map[string]string{"token": g.client.token},
			})
			if err := conn.WriteMessage(websocket.TextMessage, identify); err != nil {
				return ready
			}
			var hello struct {
				HeartbeatIntervalMS int64 `json:"heartbeat_interval_ms"`
			}
			_ = json.Unmarshal(frame.D, &hello)
			interval := time.Duration(hello.HeartbeatIntervalMS) * time.Millisecond
			if interval <= 0 {
				interval = 30 * time.Second
			}
			go g.heartbeatLoop(conn, interval, stopBeat)
		case "READY":
			ready = true
			g.emit("READY", frame.D)
		case "DISPATCH":
			g.emit(frame.T, frame.D)
		}
	}
}

func (g *Gateway) heartbeatLoop(conn *websocket.Conn, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	beat, _ := json.Marshal(map[string]string{"op": "HEARTBEAT"})
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := conn.WriteMessage(websocket.TextMessage, beat); err != nil {
				return
			}
		}
	}
}
