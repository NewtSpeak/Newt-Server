package sfunode

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// 心跳语义：节点每 10s 上报一次，连续 3 次未收到（30s）判定离线（docs 03 §5.1）。
const (
	heartbeatInterval = 10 * time.Second
	heartbeatTimeout  = 3 * heartbeatInterval
)

// liveState 节点实时快照（内存权威，DB 为持久化兜底）。
type liveState struct {
	CurrentUsers     int
	CPUPct           float64
	MemPct           float64
	BandwidthOutMbps float64
	ScreenTracks     int
	NodeRTTMs        map[string]float64
	LastSeen         time.Time
}

type nodeConn struct {
	nodeID  uuid.UUID
	ws      *websocket.Conn
	writeMu sync.Mutex
}

func (c *nodeConn) sendJSON(msg wireMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteJSON(msg)
}

// Hub 控制通道连接注册表：维护在线连接与实时容量快照，承接指令下发。
type Hub struct {
	store controlStore

	mu    sync.RWMutex
	conns map[uuid.UUID]*nodeConn
	live  map[uuid.UUID]liveState
}

func NewHub(store controlStore) *Hub {
	return &Hub{store: store, conns: make(map[uuid.UUID]*nodeConn), live: make(map[uuid.UUID]liveState)}
}

// IsConnected 节点控制通道是否在线。
func (h *Hub) IsConnected(nodeID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[nodeID]
	return ok
}

// Live 返回节点实时快照。
func (h *Hub) Live(nodeID uuid.UUID) (liveState, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state, ok := h.live[nodeID]
	return state, ok
}

// SendCommand 向节点下发带 command_id 的指令；节点不在线返回 ErrNodeOffline。
func (h *Hub) SendCommand(nodeID uuid.UUID, msgType string, payload any) error {
	h.mu.RLock()
	conn, ok := h.conns[nodeID]
	h.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w：%s", ErrNodeOffline, nodeID)
	}
	msg, err := newCommand(msgType, payload)
	if err != nil {
		return fmt.Errorf("序列化控制指令失败: %w", err)
	}
	if err := conn.sendJSON(msg); err != nil {
		return fmt.Errorf("下发控制指令失败: %w", err)
	}
	return nil
}

// Kick 主动断开节点控制连接（吊销/禁用时调用）。
func (h *Hub) Kick(nodeID uuid.UUID, reason string) {
	h.mu.RLock()
	conn, ok := h.conns[nodeID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	message := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason)
	_ = conn.ws.WriteControl(websocket.CloseMessage, message, time.Now().Add(3*time.Second))
	_ = conn.ws.Close()
}

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	// 控制通道走 mTLS 且非浏览器客户端，无需 Origin 校验。
	CheckOrigin: func(*http.Request) bool { return true },
}

// authenticatePeer 从 TLS 状态校验节点身份：证书链已由 tls.Config 验证，
// 这里再校验指纹在节点表且状态允许连接（docs 03 §5.2）。
func (h *Hub) authenticatePeer(tlsState *tls.ConnectionState) (model.SfuNode, error) {
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return model.SfuNode{}, fmt.Errorf("缺少客户端证书")
	}
	fp := FingerprintCert(tlsState.PeerCertificates[0])
	node, err := h.store.NodeByFingerprint(fp)
	if err != nil {
		return model.SfuNode{}, err
	}
	switch node.Status {
	case model.SfuNodePendingEnrollment:
		return model.SfuNode{}, fmt.Errorf("节点尚未完成 enrollment")
	case model.SfuNodeRevoked, model.SfuNodeDisabled:
		return model.SfuNode{}, fmt.Errorf("节点已被吊销或禁用")
	}
	return node, nil
}

// handleControl WebSocket 控制通道入口（GET /control）。
func (h *Hub) handleControl(w http.ResponseWriter, r *http.Request) {
	node, err := h.authenticatePeer(r.TLS)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.serveConn(node, ws)
}

// serveConn 连接主循环：首条消息必须是 Register，此后处理心跳与上报。
func (h *Hub) serveConn(node model.SfuNode, ws *websocket.Conn) {
	defer ws.Close()

	// 首条消息必须在心跳窗口内送达 Register。
	_ = ws.SetReadDeadline(time.Now().Add(heartbeatTimeout))
	var first wireMessage
	if err := ws.ReadJSON(&first); err != nil || first.Type != msgRegister {
		log.Printf("sfunode: 节点 %s 未按协议发送 Register，断开", node.ID)
		return
	}
	var reg registerPayload
	if err := json.Unmarshal(first.Payload, &reg); err != nil {
		log.Printf("sfunode: 节点 %s Register payload 解析失败: %v", node.ID, err)
		return
	}
	// 应用层再绑 node_id：必须与证书身份一致（docs 03 §5.2 第 3 步）。
	if reg.NodeID != node.ID {
		log.Printf("sfunode: 节点声明 node_id=%s 与证书身份 %s 不一致，断开", reg.NodeID, node.ID)
		return
	}
	updated, err := h.store.MarkOnline(node.ID, reg)
	if err != nil {
		log.Printf("sfunode: 标记节点 %s 上线失败: %v", node.ID, err)
		return
	}

	conn := &nodeConn{nodeID: node.ID, ws: ws}
	h.mu.Lock()
	if old, ok := h.conns[node.ID]; ok {
		// 同一节点重复连接：踢掉旧连接，保留新连接。
		_ = old.ws.Close()
	}
	h.conns[node.ID] = conn
	h.live[node.ID] = liveState{LastSeen: time.Now()}
	h.mu.Unlock()

	_ = conn.sendJSON(wireMessage{Type: msgRegisterAck, Payload: mustJSON(registerAckPayload{
		NodeID:             node.ID,
		HeartbeatIntervalS: int(heartbeatInterval.Seconds()),
		Status:             updated.Status,
	})})
	log.Printf("sfunode: 节点 %s (%s) 控制通道已连接", updated.DisplayName, node.ID)

	defer func() {
		h.mu.Lock()
		// 仅当当前连接仍是注册表中的连接时才清理（避免误删重连后的新连接）。
		if h.conns[node.ID] == conn {
			delete(h.conns, node.ID)
			delete(h.live, node.ID)
			h.mu.Unlock()
			if err := h.store.MarkOffline(node.ID); err != nil {
				log.Printf("sfunode: 标记节点 %s 离线失败: %v", node.ID, err)
			}
			log.Printf("sfunode: 节点 %s 控制通道断开", node.ID)
			return
		}
		h.mu.Unlock()
	}()

	for {
		// 任意消息都视为存活信号；超过 3 个心跳周期无消息 → 读超时 → 离线。
		_ = ws.SetReadDeadline(time.Now().Add(heartbeatTimeout))
		var msg wireMessage
		if err := ws.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case msgHeartbeat:
			var hb heartbeatPayload
			if err := json.Unmarshal(msg.Payload, &hb); err != nil {
				log.Printf("sfunode: 节点 %s 心跳解析失败: %v", node.ID, err)
				continue
			}
			h.mu.Lock()
			h.live[node.ID] = liveState{
				CurrentUsers:     hb.CurrentUsers,
				CPUPct:           hb.CPUPct,
				MemPct:           hb.MemPct,
				BandwidthOutMbps: hb.BandwidthOutMbps,
				ScreenTracks:     hb.ScreenTracks,
				NodeRTTMs:        hb.NodeRTTMs,
				LastSeen:         time.Now(),
			}
			h.mu.Unlock()
			if err := h.store.SaveHeartbeat(node.ID, hb); err != nil {
				log.Printf("sfunode: 持久化节点 %s 心跳失败: %v", node.ID, err)
			}
		case msgRoomEvent:
			// 房间事件当前仅记录；语音编排专项接入后经 eventbus 消费。
			var ev roomEventPayload
			if err := json.Unmarshal(msg.Payload, &ev); err == nil {
				log.Printf("sfunode: 节点 %s RoomEvent room=%s event=%s", node.ID, ev.RoomID, ev.Event)
			}
		case msgEdgeStatus:
			var es edgeStatusPayload
			if err := json.Unmarshal(msg.Payload, &es); err == nil {
				log.Printf("sfunode: 节点 %s EdgeStatus room=%s %s→%s state=%s rtt=%.1fms",
					node.ID, es.RoomID, es.ParentNodeID, es.ChildNodeID, es.State, es.RTTMs)
			}
		default:
			log.Printf("sfunode: 节点 %s 发送未知消息类型 %q，忽略", node.ID, msg.Type)
		}
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

// ControlServer 独立于业务 API 的 mTLS 控制服务（cfg.ControlAddress，docs 03 §5.4 端口分离）。
type ControlServer struct {
	server   *http.Server
	listener net.Listener
}

// renewRequest / renewResponse mTLS 身份下的证书轮换（docs 03 §4.4）。
type renewRequest struct {
	CSRPEM string `json:"csr_pem"`
}

type renewResponse struct {
	NodeID      uuid.UUID `json:"node_id"`
	CertPEM     string    `json:"cert_pem"`
	CABundlePEM string    `json:"ca_bundle_pem"`
	NotAfter    time.Time `json:"not_after"`
}

// StartControlServer 监听 mTLS 控制端口：
//   - GET  /control 控制通道 WebSocket
//   - POST /renew   证书轮换（凭现有节点证书）
func StartControlServer(address string, ca *ClusterCA, hub *Hub, svc *Service) (*ControlServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /control", hub.handleControl)
	mux.HandleFunc("POST /renew", func(w http.ResponseWriter, r *http.Request) {
		node, err := hub.authenticatePeer(r.TLS)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var req renewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求体解析失败", http.StatusBadRequest)
			return
		}
		result, err := svc.RenewCertificate(node.ID, []byte(req.CSRPEM))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renewResponse{
			NodeID:      node.ID,
			CertPEM:     string(result.CertPEM),
			CABundlePEM: string(result.CABundlePEM),
			NotAfter:    result.NotAfter,
		})
	})

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("监听控制通道地址 %s 失败: %w", address, err)
	}
	server := &http.Server{
		Handler:           mux,
		TLSConfig:         ca.ServerTLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("sfunode: 控制通道服务退出: %v", err)
		}
	}()
	log.Printf("sfunode: mTLS 控制通道监听 %s", listener.Addr())
	return &ControlServer{server: server, listener: listener}, nil
}

// Addr 实际监听地址。
func (cs *ControlServer) Addr() net.Addr { return cs.listener.Addr() }

// Shutdown 优雅关闭。
func (cs *ControlServer) Shutdown(ctx context.Context) error { return cs.server.Shutdown(ctx) }
