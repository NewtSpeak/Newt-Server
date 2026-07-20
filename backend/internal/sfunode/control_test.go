package sfunode

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

// fakeStore controlStore 的内存 mock（隔离数据库）。
type fakeStore struct {
	mu       sync.Mutex
	nodes    map[string]model.SfuNode // key: cert fingerprint
	statuses map[uuid.UUID]string
	beats    map[uuid.UUID]heartbeatPayload
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		nodes:    make(map[string]model.SfuNode),
		statuses: make(map[uuid.UUID]string),
		beats:    make(map[uuid.UUID]heartbeatPayload),
	}
}

func (f *fakeStore) addNode(fp string, node model.SfuNode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[fp] = node
	f.statuses[node.ID] = node.Status
}

func (f *fakeStore) NodeByFingerprint(fp string) (model.SfuNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.nodes[fp]
	if !ok {
		return model.SfuNode{}, errors.New("证书指纹未登记")
	}
	node.Status = f.statuses[node.ID]
	return node, nil
}

func (f *fakeStore) MarkOnline(nodeID uuid.UUID, _ registerPayload) (model.SfuNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[nodeID] = model.SfuNodeOnline
	return model.SfuNode{ID: nodeID, Status: model.SfuNodeOnline}, nil
}

func (f *fakeStore) MarkOffline(nodeID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[nodeID] = model.SfuNodeEnrolled
	return nil
}

func (f *fakeStore) SaveHeartbeat(nodeID uuid.UUID, hb heartbeatPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats[nodeID] = hb
	return nil
}

func (f *fakeStore) status(nodeID uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses[nodeID]
}

// nodeClient 模拟一个已 enroll 的 SFU 节点：持有集群 CA 签发的证书。
type nodeClient struct {
	nodeID uuid.UUID
	cert   tls.Certificate
	roots  *x509.CertPool
}

func newNodeClient(t *testing.T, ca *ClusterCA, nodeID uuid.UUID) (*nodeClient, string) {
	t.Helper()
	key, csrPEM := newNodeKeyAndCSR(t, "node")
	certPEM, fingerprint, _, err := ca.SignNodeCSR(csrPEM, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CABundlePEM())
	return &nodeClient{nodeID: nodeID, cert: cert, roots: roots}, fingerprint
}

func (nc *nodeClient) dial(t *testing.T, addr string) (*websocket.Conn, error) {
	t.Helper()
	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			RootCAs:      nc.roots,
			ServerName:   "localhost",
			Certificates: []tls.Certificate{nc.cert},
		},
		HandshakeTimeout: 5 * time.Second,
	}
	ws, _, err := dialer.Dial("wss://"+addr+"/control", nil)
	return ws, err
}

func (nc *nodeClient) register(t *testing.T, ws *websocket.Conn, nodeID uuid.UUID) {
	t.Helper()
	payload, _ := json.Marshal(registerPayload{
		NodeID:           nodeID,
		Version:          "test",
		ControlAdvertise: "127.0.0.1:9999",
		WebRTCHosts:      []string{"127.0.0.1:50000"},
		MaxUsers:         500,
	})
	if err := ws.WriteJSON(wireMessage{Type: msgRegister, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func readMessage(t *testing.T, ws *websocket.Conn) (wireMessage, error) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var msg wireMessage
	err := ws.ReadJSON(&msg)
	return msg, err
}

func startTestControl(t *testing.T) (*ClusterCA, *fakeStore, *Hub, string) {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	hub := NewHub(store)
	cs, err := StartControlServer("127.0.0.1:0", ca, hub, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.listener.Close() })
	return ca, store, hub, cs.Addr().String()
}

func TestControlChannelLifecycle(t *testing.T) {
	ca, store, hub, addr := startTestControl(t)

	nodeID := uuid.New()
	client, fp := newNodeClient(t, ca, nodeID)
	store.addNode(fp, model.SfuNode{ID: nodeID, Status: model.SfuNodeEnrolled, CertFingerprint: fp})

	ws, err := client.dial(t, addr)
	if err != nil {
		t.Fatalf("mTLS WebSocket 连接失败: %v", err)
	}
	defer ws.Close()

	// Register → RegisterAck，状态转 ONLINE。
	client.register(t, ws, nodeID)
	ack, err := readMessage(t, ws)
	if err != nil || ack.Type != msgRegisterAck {
		t.Fatalf("期望 register_ack，得到 %+v err=%v", ack, err)
	}
	var ackPayload registerAckPayload
	if err := json.Unmarshal(ack.Payload, &ackPayload); err != nil {
		t.Fatal(err)
	}
	if ackPayload.NodeID != nodeID || ackPayload.HeartbeatIntervalS != 10 {
		t.Fatalf("register_ack 内容不符: %+v", ackPayload)
	}
	if !hub.IsConnected(nodeID) {
		t.Fatal("Hub 应记录节点在线")
	}
	if store.status(nodeID) != model.SfuNodeOnline {
		t.Fatalf("节点状态应为 ONLINE，实际 %s", store.status(nodeID))
	}

	// Heartbeat → 实时快照更新。
	hb, _ := json.Marshal(heartbeatPayload{
		CurrentUsers: 42, CPUPct: 33.5, MemPct: 60, BandwidthOutMbps: 120,
		ScreenTracks: 3, NodeRTTMs: map[string]float64{uuid.NewString(): 25.5},
	})
	if err := ws.WriteJSON(wireMessage{Type: msgHeartbeat, Payload: hb}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		live, ok := hub.Live(nodeID)
		if ok && live.CurrentUsers == 42 {
			if live.CPUPct != 33.5 || live.ScreenTracks != 3 {
				t.Fatalf("心跳快照不符: %+v", live)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("心跳未在期限内更新到内存快照")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Server→SFU 指令：带 command_id 下发到连接。
	if err := hub.SendCommand(nodeID, msgEnsureRoom, roomCommandPayload{RoomID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	cmd, err := readMessage(t, ws)
	if err != nil || cmd.Type != msgEnsureRoom {
		t.Fatalf("期望 ensure_room，得到 %+v err=%v", cmd, err)
	}
	if cmd.CommandID == "" {
		t.Fatal("指令必须带 command_id（幂等键）")
	}

	// 断开 → 离线（ONLINE → ENROLLED）。
	ws.Close()
	deadline = time.Now().Add(3 * time.Second)
	for hub.IsConnected(nodeID) {
		if time.Now().After(deadline) {
			t.Fatal("断开后 Hub 未清理连接")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if store.status(nodeID) != model.SfuNodeEnrolled {
		t.Fatalf("断开后状态应回 ENROLLED，实际 %s", store.status(nodeID))
	}
	if err := hub.SendCommand(nodeID, msgDrain, struct{}{}); err == nil {
		t.Fatal("节点离线后下发指令应失败")
	}
}

func TestControlChannelRejectsBadIdentity(t *testing.T) {
	ca, store, hub, addr := startTestControl(t)

	t.Run("未登记指纹被拒绝", func(t *testing.T) {
		nodeID := uuid.New()
		client, _ := newNodeClient(t, ca, nodeID) // 指纹不放进 store
		if _, err := client.dial(t, addr); err == nil {
			t.Fatal("未登记指纹应无法升级 WebSocket")
		}
	})

	t.Run("REVOKED 节点被拒绝", func(t *testing.T) {
		nodeID := uuid.New()
		client, fp := newNodeClient(t, ca, nodeID)
		store.addNode(fp, model.SfuNode{ID: nodeID, Status: model.SfuNodeRevoked, CertFingerprint: fp})
		if _, err := client.dial(t, addr); err == nil {
			t.Fatal("REVOKED 节点应被拒绝")
		}
	})

	t.Run("Register 声明的 node_id 与证书不一致被断开", func(t *testing.T) {
		nodeID := uuid.New()
		client, fp := newNodeClient(t, ca, nodeID)
		store.addNode(fp, model.SfuNode{ID: nodeID, Status: model.SfuNodeEnrolled, CertFingerprint: fp})
		ws, err := client.dial(t, addr)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		client.register(t, ws, uuid.New()) // 伪造别的 node_id
		if _, err := readMessage(t, ws); err == nil {
			t.Fatal("身份不一致时服务端应断开连接")
		}
		if hub.IsConnected(nodeID) {
			t.Fatal("身份不一致的连接不应注册进 Hub")
		}
	})
}

// TestControllerOfflineError 确认 sfuctl.Controller 对不在线节点返回错误。
func TestControllerOfflineError(t *testing.T) {
	hub := NewHub(newFakeStore())
	ctl := &controller{hub: hub}
	if err := ctl.EnsureRoom(uuid.New(), uuid.New()); err == nil || !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("期望 ErrNodeOffline，得到 %v", err)
	}
	// SetCascadeEdges 涉边节点不在线 → 报错。
	err := ctl.SetCascadeEdges(uuid.New(), 1, []sfuctl.Edge{{ParentNodeID: uuid.New(), ChildNodeID: uuid.New()}})
	if err == nil || !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("期望 ErrNodeOffline，得到 %v", err)
	}
}
