package sfunode

import (
	"encoding/json"

	"github.com/google/uuid"
)

// 控制通道 JSON 协议（docs 03 §5.3、docs 08 §6.1）。
// 消息封包：{"type": "...", "command_id": "...", "payload": {...}}
// Server→SFU 指令均带 command_id，SFU 侧按 command_id 幂等去重（docs 03 §5.1 重放防护）。

// SFU → Server 消息类型。
const (
	msgRegister   = "register"
	msgHeartbeat  = "heartbeat"
	msgRoomEvent  = "room_event"
	msgEdgeStatus = "edge_status"
)

// Server → SFU 消息类型。
const (
	msgRegisterAck    = "register_ack"
	msgEnsureRoom     = "ensure_room"
	msgCloseRoom      = "close_room"
	msgDisconnectUser = "disconnect_user"
	msgUpdateCaps     = "update_participant_caps"
	msgSetAnchorLease = "set_anchor_lease"
	msgSetCascadeEdge = "set_cascade_edges"
	msgDrain          = "drain"
)

type wireMessage struct {
	Type      string          `json:"type"`
	CommandID string          `json:"command_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// registerPayload SFU 连上控制通道后的首条消息：自报端点与配置容量。
type registerPayload struct {
	NodeID           uuid.UUID `json:"node_id"` // 应用层再绑 node_id，与证书一致（docs 03 §5.2）
	Version          string    `json:"version"`
	ControlAdvertise string    `json:"control_advertise"`
	WebRTCHosts      []string  `json:"webrtc_hosts"`
	MaxUsers         int       `json:"max_users"`
}

// registerAckPayload Server 对 Register 的应答。
type registerAckPayload struct {
	NodeID             uuid.UUID `json:"node_id"`
	HeartbeatIntervalS int       `json:"heartbeat_interval_s"`
	Status             string    `json:"status"`
}

// heartbeatPayload 心跳容量上报（docs 03 §2.2 capacity + docs 10 调度输入）。
type heartbeatPayload struct {
	CurrentUsers     int                `json:"current_users"`
	CPUPct           float64            `json:"cpu_pct"`
	MemPct           float64            `json:"mem_pct"`
	BandwidthOutMbps float64            `json:"bandwidth_out_mbps"`
	ScreenTracks     int                `json:"screen_tracks"`
	NodeRTTMs        map[string]float64 `json:"node_rtt_ms"` // key 为对端 node_id
}

// roomEventPayload 房间事件上报（用户加入/离开媒体、异常）。
type roomEventPayload struct {
	RoomID uuid.UUID      `json:"room_id"`
	UserID *uuid.UUID     `json:"user_id,omitempty"`
	Event  string         `json:"event"`
	Detail map[string]any `json:"detail,omitempty"`
}

// edgeStatusPayload 级联边状态上报（docs 08 §6.1 EdgeStatus）。
type edgeStatusPayload struct {
	RoomID       uuid.UUID `json:"room_id"`
	Epoch        uint64    `json:"epoch"`
	ParentNodeID uuid.UUID `json:"parent_node_id"`
	ChildNodeID  uuid.UUID `json:"child_node_id"`
	State        string    `json:"state"` // EDGE_UP / EDGE_DOWN
	RTTMs        float64   `json:"rtt_ms"`
	LossPct      float64   `json:"loss_pct"`
}

// ---- Server → SFU 指令 payload ----

type roomCommandPayload struct {
	RoomID uuid.UUID `json:"room_id"`
}

type disconnectUserPayload struct {
	RoomID uuid.UUID `json:"room_id"`
	UserID uuid.UUID `json:"user_id"`
	Reason string    `json:"reason"`
}

type updateCapsPayload struct {
	RoomID uuid.UUID `json:"room_id"`
	UserID uuid.UUID `json:"user_id"`
	Caps   []string  `json:"caps"`
}

type anchorLeasePayload struct {
	RoomID            uuid.UUID `json:"room_id"`
	AnchorNodeID      uuid.UUID `json:"anchor_node_id"`
	Epoch             uint64    `json:"epoch"`
	LeaseExpireUnixMs int64     `json:"lease_expire_unix_ms"`
}

type cascadeEdgeWire struct {
	ParentNodeID          uuid.UUID `json:"parent_node_id"`
	ChildNodeID           uuid.UUID `json:"child_node_id"`
	ParentCascadeEndpoint string    `json:"parent_cascade_endpoint,omitempty"`
	CascadeToken          string    `json:"cascade_token,omitempty"`
}

type cascadeEdgesPayload struct {
	RoomID uuid.UUID         `json:"room_id"`
	Epoch  uint64            `json:"epoch"`
	Edges  []cascadeEdgeWire `json:"edges"`
}

func newCommand(msgType string, payload any) (wireMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return wireMessage{}, err
	}
	return wireMessage{Type: msgType, CommandID: uuid.NewString(), Payload: raw}, nil
}
