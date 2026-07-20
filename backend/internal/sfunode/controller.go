package sfunode

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

// controller sfuctl.Controller 实现：把指令经控制通道发给目标节点连接。
// 所有指令带 command_id，SFU 侧按其幂等（docs 03 §5.1）。
type controller struct {
	hub *Hub
	dir *directory
}

var _ sfuctl.Controller = (*controller)(nil)

func (c *controller) EnsureRoom(nodeID, roomID uuid.UUID) error {
	return c.hub.SendCommand(nodeID, msgEnsureRoom, roomCommandPayload{RoomID: roomID})
}

func (c *controller) CloseRoom(nodeID, roomID uuid.UUID) error {
	return c.hub.SendCommand(nodeID, msgCloseRoom, roomCommandPayload{RoomID: roomID})
}

func (c *controller) DisconnectUser(nodeID, roomID, userID uuid.UUID, reason string) error {
	return c.hub.SendCommand(nodeID, msgDisconnectUser, disconnectUserPayload{RoomID: roomID, UserID: userID, Reason: reason})
}

func (c *controller) UpdateParticipantCaps(nodeID, roomID, userID uuid.UUID, caps []string) error {
	return c.hub.SendCommand(nodeID, msgUpdateCaps, updateCapsPayload{RoomID: roomID, UserID: userID, Caps: caps})
}

// SetAnchorLease 下发根租约给指定节点（docs 08 §3.1）。
func (c *controller) SetAnchorLease(nodeID, roomID, anchorNodeID uuid.UUID, epoch uint64, leaseExpire time.Time) error {
	return c.hub.SendCommand(nodeID, msgSetAnchorLease, anchorLeasePayload{
		RoomID: roomID, AnchorNodeID: anchorNodeID, Epoch: epoch,
		LeaseExpireUnixMs: leaseExpire.UnixMilli(),
	})
}

// SetCascadeEdges 把某 epoch 的边集下发给所有涉边节点（docs 08 §4.2：整体按 epoch 切换）。
func (c *controller) SetCascadeEdges(roomID uuid.UUID, epoch uint64, edges []sfuctl.Edge) error {
	wireEdges := make([]cascadeEdgeWire, 0, len(edges))
	involved := make(map[uuid.UUID]bool)
	for _, edge := range edges {
		wireEdges = append(wireEdges, cascadeEdgeWire{
			ParentNodeID: edge.ParentNodeID, ChildNodeID: edge.ChildNodeID,
			ParentCascadeEndpoint: edge.ParentCascadeEndpoint, CascadeToken: edge.CascadeToken,
		})
		involved[edge.ParentNodeID] = true
		involved[edge.ChildNodeID] = true
	}
	payload := cascadeEdgesPayload{RoomID: roomID, Epoch: epoch, Edges: wireEdges}
	for nodeID := range involved {
		if err := c.hub.SendCommand(nodeID, msgSetCascadeEdge, payload); err != nil {
			return err
		}
	}
	return nil
}

// WaitEdgeUp 本实现已停用（真实链路走 internal/sfubridge），不支持边等待。
func (c *controller) WaitEdgeUp(roomID uuid.UUID, epoch uint64, parentNodeID, childNodeID uuid.UUID, timeout time.Duration) error {
	return fmt.Errorf("sfunode 控制通道已停用，WaitEdgeUp 未实现（请使用 sfubridge）")
}

func (c *controller) DrainNode(nodeID uuid.UUID, _ time.Time) error {
	return c.hub.SendCommand(nodeID, msgDrain, struct{}{})
}

// UndrainNode 本实现已停用（真实链路走 internal/sfubridge）。
func (c *controller) UndrainNode(nodeID uuid.UUID) error {
	return fmt.Errorf("sfunode 控制通道已停用，UndrainNode 未实现（请使用 sfubridge）")
}

// MigrateParticipants 本实现已停用（真实链路走 internal/sfubridge）。
func (c *controller) MigrateParticipants(nodeID, roomID uuid.UUID, migrationID uuid.UUID,
	sessionIDs []string, toNodeID uuid.UUID, phase sfuctl.MigratePhase) error {
	return fmt.Errorf("sfunode 控制通道已停用，MigrateParticipants 未实现（请使用 sfubridge）")
}
