// Package sfubridge 把业务编排稳定接口层 internal/sfuctl（Directory/Controller）
// 桥接到真实 SFU 对接的 gRPC 控制面 internal/sfucontrol：
//
//   - 节点目录：SfuNode 表（静态属性 + 节点池表）+ sfucontrol.Registry 内存快照（实时在线/容量）；
//   - 控制指令：经 Registry.SendCommand 走 proto/owlsfu/v1 的 Command（EnsureLogicalRoom /
//     DisconnectUser / UpdateParticipantCaps / Drain / 级联指令），等待 CommandAck；
//   - Owl-SFU M1 对级联/迁移指令按协议约定回 UNIMPLEMENTED，本桥视为非致命（记日志放行），
//     M3/M4 实装后自然生效。
//
// 装配位置见 internal/server/server.go；本包取代 internal/sfunode 中基于自研 WSS
// 控制通道的 directory/controller 实现（该协议与真实 Owl-SFU 不兼容，已停用）。
package sfubridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfucontrol"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"gorm.io/gorm"
)

// Bridge 同时实现 sfuctl.Directory 与 sfuctl.Controller。
type Bridge struct {
	db       *gorm.DB
	registry *sfucontrol.Registry
	logger   *slog.Logger
}

var (
	_ sfuctl.Directory  = (*Bridge)(nil)
	_ sfuctl.Controller = (*Bridge)(nil)
)

// New 构建桥接器（不注入）。
func New(db *gorm.DB, registry *sfucontrol.Registry) *Bridge {
	return &Bridge{db: db, registry: registry, logger: slog.Default().With("component", "sfubridge")}
}

// Install 构建并注入为 sfuctl 的全局实现（voice/stage 等模块经 sfuctl.Dir()/Ctl() 消费）。
func Install(db *gorm.DB, registry *sfucontrol.Registry) *Bridge {
	bridge := New(db, registry)
	sfuctl.SetDirectory(bridge)
	sfuctl.SetController(bridge)
	// BI.3 提前信号（docs 15 §5）：客户端侧 ICE/连接失败等上报进控制面判死聚合器。
	sfuctl.SetSuspectReporter(registry.ReportSuspect)
	return bridge
}

// ---------------------------------------------------------------------------
// Directory：节点目录（DB 静态属性 + Registry 实时快照）
// ---------------------------------------------------------------------------

// PoolNodes 返回 Guild 节点池候选（docs 07 专项 2）：
//  1. 服务器管理员有勾选节点 → 返回勾选集合；
//  2. 池为空且回落开关打开（默认打开）→ 平台默认池（platform_default=true）；
//  3. 池为空且回落关闭 → 空列表（调用方按无可用容量处理）。
func (b *Bridge) PoolNodes(guildID uuid.UUID) ([]sfuctl.NodeInfo, error) {
	var selections []model.SfuGuildNodeSelection
	if err := b.db.Where("guild_id = ?", guildID).Find(&selections).Error; err != nil {
		return nil, fmt.Errorf("查询服级节点池失败: %w", err)
	}
	var nodes []model.SfuNode
	if len(selections) > 0 {
		ids := make([]uuid.UUID, 0, len(selections))
		for _, sel := range selections {
			ids = append(ids, sel.NodeID)
		}
		if err := b.db.Where("id IN ?", ids).Find(&nodes).Error; err != nil {
			return nil, fmt.Errorf("查询池内节点失败: %w", err)
		}
	} else {
		fallback := true
		var pool model.SfuGuildNodePool
		if err := b.db.First(&pool, "guild_id = ?", guildID).Error; err == nil {
			fallback = pool.FallbackToDefault
		}
		if !fallback {
			return nil, nil
		}
		if err := b.db.Where("platform_default = ?", true).Find(&nodes).Error; err != nil {
			return nil, fmt.Errorf("查询平台默认池失败: %w", err)
		}
	}
	infos := make([]sfuctl.NodeInfo, 0, len(nodes))
	for _, node := range nodes {
		infos = append(infos, b.toInfo(node))
	}
	return infos, nil
}

func (b *Bridge) Node(nodeID uuid.UUID) (sfuctl.NodeInfo, error) {
	var node model.SfuNode
	if err := b.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return sfuctl.NodeInfo{}, sfuctl.ErrNodeNotFound
	}
	return b.toInfo(node), nil
}

func (b *Bridge) AllNodes() ([]sfuctl.NodeInfo, error) {
	var nodes []model.SfuNode
	if err := b.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询全部节点失败: %w", err)
	}
	infos := make([]sfuctl.NodeInfo, 0, len(nodes))
	for _, node := range nodes {
		infos = append(infos, b.toInfo(node))
	}
	return infos, nil
}

// toInfo DB 记录 + Registry 实时快照 → 调度用 NodeInfo。
// 在线判定以 Registry（活跃 gRPC 控制流 + 心跳）为准；容量实时值覆盖 DB 持久化快照。
func (b *Bridge) toInfo(node model.SfuNode) sfuctl.NodeInfo {
	info := sfuctl.NodeInfo{
		ID:                   node.ID,
		DisplayName:          node.DisplayName,
		Region:               node.Labels["region"],
		Labels:               node.Labels,
		Status:               node.Status,
		Online:               false,
		Draining:             node.Status == model.SfuNodeDraining,
		EnabledForScheduling: node.EnabledForScheduling,
		MaxUsers:             node.MaxUsers,
		CurrentUsers:         node.CurrentUsers,
		CPUPercent:           node.CPUPct,
		MemPercent:           node.MemPct,
		BandwidthOutMbps:     node.BandwidthOutMbps,
		ScreenTracks:         node.ScreenTracks,
		// 下发给客户端的接入端点 = 节点经控制通道 Register 自报的 WSS 信令地址。
		WebRTCEndpoint:  node.AdvertiseWssURL,
		CascadeEndpoint: node.CascadeEndpoint,
		NodeRTTMs:       node.NodeRTTMs,
	}
	if node.LastSeenAt != nil {
		info.LastSeenAt = *node.LastSeenAt
	}
	if snapshot, ok := b.registry.Snapshot(node.ID); ok {
		// 心跳新鲜度以内存注册表为准（DB last_seen_at 有 30s 落库节流，仅作重启兜底）。
		if !snapshot.LastSeen.IsZero() {
			info.LastSeenAt = snapshot.LastSeen
		}
		if snapshot.Online {
			info.Online = true
			capacity := snapshot.Capacity
			if capacity.MaxUsers > 0 {
				info.MaxUsers = capacity.MaxUsers
			}
			info.CurrentUsers = capacity.CurrentUsers
			info.CPUPercent = capacity.CPUPct
			info.MemPercent = capacity.MemPct
			info.BandwidthOutMbps = capacity.BandwidthOutMbps
			info.ScreenTracks = capacity.ScreenTracks
		}
	}
	return info
}

// ---------------------------------------------------------------------------
// Controller：控制通道指令（全部经 gRPC Command + CommandAck，天然幂等）
// ---------------------------------------------------------------------------

func (b *Bridge) EnsureRoom(nodeID, roomID uuid.UUID) error {
	return b.sendCommand(nodeID, &owlsfuv1.Command_EnsureLogicalRoom{
		EnsureLogicalRoom: &owlsfuv1.EnsureLogicalRoom{RoomId: roomID.String()},
	}, false)
}

func (b *Bridge) CloseRoom(nodeID, roomID uuid.UUID) error {
	return b.sendCommand(nodeID, &owlsfuv1.Command_CloseLogicalRoom{
		CloseLogicalRoom: &owlsfuv1.CloseLogicalRoom{RoomId: roomID.String()},
	}, false)
}

// DisconnectUser 断开某用户在房内的全部会话（session_id 留空 = 全部，proto 语义）。
func (b *Bridge) DisconnectUser(nodeID, roomID, userID uuid.UUID, reason string) error {
	return b.sendCommand(nodeID, &owlsfuv1.Command_DisconnectUser{
		DisconnectUser: &owlsfuv1.DisconnectUser{
			RoomId: roomID.String(),
			UserId: userID.String(),
			Reason: disconnectReason(reason),
		},
	}, false)
}

// UpdateParticipantCaps 全量替换某在房用户的 caps（协议 §3）。
// proto 以 session_id 定位会话（SFU 侧会话键 = sid，15 BJ.2），
// 这里从 VoiceState 反查该用户当前会话的 voice_session_id。
func (b *Bridge) UpdateParticipantCaps(nodeID, roomID, userID uuid.UUID, caps []string) error {
	var vs model.VoiceState
	err := b.db.
		Where("channel_id = ? AND user_id = ? AND node_id = ?", roomID, userID, nodeID).
		First(&vs).Error
	if err != nil {
		return fmt.Errorf("用户 %s 在房间 %s（节点 %s）无语音会话，无法更新 caps", userID, roomID, nodeID)
	}
	if vs.VoiceSessionID == nil {
		return fmt.Errorf("用户 %s 的语音状态缺少 voice_session_id", userID)
	}
	return b.sendCommand(nodeID, &owlsfuv1.Command_UpdateParticipantCaps{
		UpdateParticipantCaps: &owlsfuv1.UpdateParticipantCaps{
			RoomId:    roomID.String(),
			SessionId: vs.VoiceSessionID.String(),
			Caps:      capsToProto(caps),
		},
	}, false)
}

// SetAnchorLease 把根租约下发给指定节点（docs 08 §3.1）。SFU 侧每个涉边节点都要求
// 「租约 epoch 匹配且未过期」才执行跨节点转发，编排方须对 anchor 与所有涉边节点分别下发。
func (b *Bridge) SetAnchorLease(nodeID, roomID, anchorNodeID uuid.UUID, epoch uint64, leaseExpire time.Time) error {
	return b.sendCommand(nodeID, &owlsfuv1.Command_SetAnchorLease{
		SetAnchorLease: &owlsfuv1.SetAnchorLease{
			RoomId:            roomID.String(),
			AnchorNodeId:      anchorNodeID.String(),
			Epoch:             epoch,
			LeaseExpireUnixMs: leaseExpire.UnixMilli(),
		},
	}, false)
}

// SetCascadeEdges 把某 epoch 的全量边集下发给所有涉边节点（docs 08 §4.2 整体按 epoch 切换）。
// 空边集无涉边节点，直接成功（房间回收路径经 CloseRoom 拆级联状态）。
func (b *Bridge) SetCascadeEdges(roomID uuid.UUID, epoch uint64, edges []sfuctl.Edge) error {
	wireEdges := make([]*owlsfuv1.CascadeEdge, 0, len(edges))
	involved := make(map[uuid.UUID]bool)
	for _, edge := range edges {
		wireEdges = append(wireEdges, &owlsfuv1.CascadeEdge{
			ParentNodeId:          edge.ParentNodeID.String(),
			ChildNodeId:           edge.ChildNodeID.String(),
			ParentCascadeEndpoint: edge.ParentCascadeEndpoint,
			CascadeToken:          edge.CascadeToken,
		})
		involved[edge.ParentNodeID] = true
		involved[edge.ChildNodeID] = true
	}
	for nodeID := range involved {
		err := b.sendCommand(nodeID, &owlsfuv1.Command_SetCascadeEdges{
			SetCascadeEdges: &owlsfuv1.SetCascadeEdges{
				RoomId: roomID.String(),
				Epoch:  epoch,
				Edges:  wireEdges,
			},
		}, false)
		if err != nil {
			return err
		}
	}
	return nil
}

// WaitEdgeUp 阻塞等待 SFU 上报某边 EdgeUp（docs 08 §4.3 步骤 4），超时报错。
func (b *Bridge) WaitEdgeUp(roomID uuid.UUID, epoch uint64, parentNodeID, childNodeID uuid.UUID, timeout time.Duration) error {
	return b.registry.WaitEdgeUp(context.Background(),
		roomID.String(), epoch, parentNodeID.String(), childNodeID.String(), timeout)
}

// DrainNode 下发排空指令；deadline = 09 I.6 硬迁时限（Server 侧兜底强推，
// SFU 侧仅提示性展示）。
func (b *Bridge) DrainNode(nodeID uuid.UUID, deadline time.Time) error {
	drain := &owlsfuv1.Drain{}
	if !deadline.IsZero() {
		drain.DeadlineUnixMs = deadline.UnixMilli()
	}
	return b.sendCommand(nodeID, &owlsfuv1.Command_Drain{Drain: drain}, false)
}

// UndrainNode 取消排空（Drain{cancel=true}）：SFU 恢复接受新会话。
func (b *Bridge) UndrainNode(nodeID uuid.UUID) error {
	return b.sendCommand(nodeID, &owlsfuv1.Command_Drain{
		Drain: &owlsfuv1.Drain{Cancel: true},
	}, false)
}

// MigrateParticipants 热迁移迁出指令（docs 09 §5.1 / M4）：
// MARK 在 CONNECT 阶段标记源节点上的 sid 迁出中；EXECUTE 在 CLEANUP 阶段摘会话。
func (b *Bridge) MigrateParticipants(nodeID, roomID uuid.UUID, migrationID uuid.UUID,
	sessionIDs []string, toNodeID uuid.UUID, phase sfuctl.MigratePhase) error {
	wirePhase := owlsfuv1.MigrateParticipants_PHASE_EXECUTE
	if phase == sfuctl.MigratePhaseMark {
		wirePhase = owlsfuv1.MigrateParticipants_PHASE_MARK
	}
	return b.sendCommand(nodeID, &owlsfuv1.Command_MigrateParticipants{
		MigrateParticipants: &owlsfuv1.MigrateParticipants{
			MigrationId: migrationID.String(),
			RoomId:      roomID.String(),
			SessionIds:  sessionIDs,
			ToNodeId:    toNodeID.String(),
			Phase:       wirePhase,
		},
	}, false)
}

// sendCommand 下发指令并等待 Ack（Registry 内置 5s 超时）。
func (b *Bridge) sendCommand(nodeID uuid.UUID, payload commandOneof, tolerateUnimplemented bool) error {
	command := &owlsfuv1.Command{}
	switch p := payload.(type) {
	case *owlsfuv1.Command_EnsureLogicalRoom:
		command.Payload = p
	case *owlsfuv1.Command_CloseLogicalRoom:
		command.Payload = p
	case *owlsfuv1.Command_DisconnectUser:
		command.Payload = p
	case *owlsfuv1.Command_UpdateParticipantCaps:
		command.Payload = p
	case *owlsfuv1.Command_SetAnchorLease:
		command.Payload = p
	case *owlsfuv1.Command_SetCascadeEdges:
		command.Payload = p
	case *owlsfuv1.Command_MigrateParticipants:
		command.Payload = p
	case *owlsfuv1.Command_Drain:
		command.Payload = p
	default:
		return fmt.Errorf("未知的控制指令载荷类型 %T", payload)
	}
	ack, err := b.registry.SendCommand(context.Background(), nodeID, command)
	if err != nil {
		return fmt.Errorf("向节点 %s 下发指令失败: %w", nodeID, err)
	}
	if !ack.GetOk() {
		if tolerateUnimplemented && ack.GetErrorCode() == "UNIMPLEMENTED" {
			b.logger.Debug("SFU 尚未实现该指令（M1 允许），忽略", "node_id", nodeID, "error", ack.GetErrorMessage())
			return nil
		}
		return fmt.Errorf("节点 %s 拒绝指令: %s（%s）", nodeID, ack.GetErrorCode(), ack.GetErrorMessage())
	}
	return nil
}

// commandOneof proto Command 的 oneof 载荷（仅作类型收口）。
type commandOneof any

// capsToProto caps 字符串 → proto Cap 枚举（docs 协议 §1 映射表）。
// 协议未定义的能力（如 priority_speaker，仅业务侧语义）不下发 SFU。
func capsToProto(caps []string) []owlsfuv1.Cap {
	mapping := map[string]owlsfuv1.Cap{
		"join":            owlsfuv1.Cap_CAP_JOIN,
		"publish_audio":   owlsfuv1.Cap_CAP_PUBLISH_AUDIO,
		"subscribe_audio": owlsfuv1.Cap_CAP_SUBSCRIBE_AUDIO,
		"publish_screen":  owlsfuv1.Cap_CAP_PUBLISH_SCREEN,
	}
	result := make([]owlsfuv1.Cap, 0, len(caps))
	for _, capName := range caps {
		if value, ok := mapping[capName]; ok {
			result = append(result, value)
		}
	}
	return result
}

// disconnectReason 业务原因串 → proto 枚举（voice 模块使用 LEAVE/MOVED/ADMIN/PERMISSION/MIGRATED 等）。
func disconnectReason(reason string) owlsfuv1.DisconnectUser_Reason {
	switch reason {
	case "ADMIN":
		return owlsfuv1.DisconnectUser_REASON_ADMIN
	case "PERMISSION":
		return owlsfuv1.DisconnectUser_REASON_PERMISSION
	case "LEAVE", "MOVED":
		return owlsfuv1.DisconnectUser_REASON_LEAVE
	case "MIGRATED", "MIGRATION":
		return owlsfuv1.DisconnectUser_REASON_MIGRATION
	case "RESTRICTION":
		return owlsfuv1.DisconnectUser_REASON_RESTRICTION
	default:
		return owlsfuv1.DisconnectUser_REASON_UNSPECIFIED
	}
}
