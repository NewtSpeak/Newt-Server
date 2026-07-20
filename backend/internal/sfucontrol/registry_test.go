package sfucontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
)

type fakeStream struct {
	mu   sync.Mutex
	sent []*owlsfuv1.ServerMessage
}

func (f *fakeStream) Send(message *owlsfuv1.ServerMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, message)
	return nil
}

func (f *fakeStream) lastCommandID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1].GetCommand().GetCommandId()
}

func TestSendCommandAcked(t *testing.T) {
	registry := NewRegistry()
	nodeID := uuid.New()
	stream := &fakeStream{}
	registry.Attach(nodeID, stream)

	go func() {
		for stream.lastCommandID() == "" {
			time.Sleep(time.Millisecond)
		}
		registry.Resolve(&owlsfuv1.CommandAck{CommandId: stream.lastCommandID(), Ok: true})
	}()

	ack, err := registry.SendCommand(context.Background(), nodeID, &owlsfuv1.Command{
		Payload: &owlsfuv1.Command_EnsureLogicalRoom{EnsureLogicalRoom: &owlsfuv1.EnsureLogicalRoom{RoomId: "room"}},
	})
	if err != nil {
		t.Fatalf("SendCommand 失败: %v", err)
	}
	if !ack.GetOk() {
		t.Fatal("应收到 ok=true 的 Ack")
	}
}

func TestSendCommandTimeout(t *testing.T) {
	registry := NewRegistry()
	registry.commandTimeout = 50 * time.Millisecond
	nodeID := uuid.New()
	registry.Attach(nodeID, &fakeStream{})

	if _, err := registry.SendCommand(context.Background(), nodeID, &owlsfuv1.Command{}); !errors.Is(err, ErrCommandTimeout) {
		t.Fatalf("无 Ack 应超时，实际 %v", err)
	}
}

func TestSendCommandOfflineNode(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.SendCommand(context.Background(), uuid.New(), &owlsfuv1.Command{}); !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("离线节点应返回 ErrNodeOffline，实际 %v", err)
	}
}

func TestDisconnectClosesStream(t *testing.T) {
	registry := NewRegistry()
	nodeID := uuid.New()
	connection := registry.Attach(nodeID, &fakeStream{})
	registry.Disconnect(nodeID)
	select {
	case <-connection.done:
	default:
		t.Fatal("Disconnect 后 done 应已关闭")
	}
}

func TestMarkStale(t *testing.T) {
	registry := NewRegistry()
	nodeID := uuid.New()
	registry.Attach(nodeID, &fakeStream{})
	registry.mu.Lock()
	registry.states[nodeID].lastSeen = time.Now().Add(-time.Minute)
	registry.mu.Unlock()

	stale := registry.MarkStale(15 * time.Second)
	if len(stale) != 1 || stale[0] != nodeID {
		t.Fatalf("应标记 1 个超时节点，实际 %v", stale)
	}
	if snapshot, _ := registry.Snapshot(nodeID); snapshot.Online {
		t.Fatal("超时节点应离线")
	}
	// 已标记过的节点不重复返回。
	if again := registry.MarkStale(15 * time.Second); len(again) != 0 {
		t.Fatalf("不应重复标记: %v", again)
	}
}
