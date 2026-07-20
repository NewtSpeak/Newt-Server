package voice

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

const (
	// anchorLeaseTTL Anchor 租约时长。语义上 Server 可随时吊销（docs 08 §3.1）；
	// 由 leaseMaintenanceLoop 周期续约并重新下发，到期未续 SFU 停止跨节点转发。
	anchorLeaseTTL = 5 * time.Minute
	// cascadeTokenTTL 级联凭证 TTL（分钟级，docs 08 E.4 / 15 BG.2）；
	// 随续约循环周期性轮换（SetCascadeEdges 幂等重发不拆边）。
	cascadeTokenTTL = 5 * time.Minute
	// leaseRenewInterval 续约与凭证轮换周期（远小于 TTL，保证不脱租）。
	leaseRenewInterval = time.Minute
	// edgeUpTimeout 下发边集后等待 EdgeUp 的时限（docs 08 §4.3 步骤 4）；
	// 超时上层走 fallback 节点或 503，不留半残状态（docs 08 §10）。
	edgeUpTimeout = 5 * time.Second
)

// anchorCandidate Anchor 选举候选（docs 08 B.2）。
type anchorCandidate struct {
	NodeID     uuid.UUID
	RoomUsers  int
	CPUPercent float64
	Region     string
}

// electAnchor 选举规则：用户数最多 → 负载更低 → region 偏好 → node_id 字典序（稳定 tie-break）。
func electAnchor(candidates []anchorCandidate, preferRegion string) (uuid.UUID, bool) {
	if len(candidates) == 0 {
		return uuid.Nil, false
	}
	sorted := make([]anchorCandidate, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.RoomUsers != b.RoomUsers {
			return a.RoomUsers > b.RoomUsers
		}
		if a.CPUPercent != b.CPUPercent {
			return a.CPUPercent < b.CPUPercent
		}
		if preferRegion != "" && (a.Region == preferRegion) != (b.Region == preferRegion) {
			return a.Region == preferRegion
		}
		return a.NodeID.String() < b.NodeID.String()
	})
	return sorted[0].NodeID, true
}

// nextEpoch 换根 / 重构图必须先升 epoch，旧边随之作废（docs 08 §3.1）。
func nextEpoch(current int64) int64 { return current + 1 }

// starEdges 纯星型边集：所有非 anchor 节点直接挂 anchor（深度 1）。
// 注：docs 08 C.2 允许 region hub 组成深度 2 树；首期实现纯星型，
// hub 策略（region 叶节点数超阈值时插入 hub）留待后续，模型字段已兼容。
func starEdges(anchorNodeID uuid.UUID, memberNodes []uuid.UUID) []sfuctl.Edge {
	edges := make([]sfuctl.Edge, 0, len(memberNodes))
	for _, node := range memberNodes {
		if node == anchorNodeID {
			continue
		}
		edges = append(edges, sfuctl.Edge{ParentNodeID: anchorNodeID, ChildNodeID: node})
	}
	return edges
}

// ---------------------------------------------------------------------------
// 级联下发（租约 + 边集 + token 签发）
// ---------------------------------------------------------------------------

// pushLease 把租约下发给单个节点（SFU 侧每个涉边节点都要求租约匹配才转发）。
func (s *Service) pushLease(nodeID uuid.UUID, lease model.VoiceAnchorLease) error {
	return sfuctl.Ctl().SetAnchorLease(nodeID, lease.RoomID, lease.AnchorNodeID,
		uint64(lease.Epoch), lease.LeaseExpiresAt)
}

// pushCascadeState 把某房间当前 epoch 的完整级联状态（租约 + 全量边集）下发给
// 所有涉边节点（docs 08 §4.2：语义以 epoch 为准；§6.2：边带 Server 签发的 cascade token）。
// 单节点下发失败不中断其余节点（尽力推送），聚合错误返回；进房路径的权威门槛
// 是随后的 WaitEdgeUp，推送失败最终会表现为等边超时。
func (s *Service) pushCascadeState(lease model.VoiceAnchorLease) error {
	roomID := lease.RoomID
	var rows []model.VoiceCascadeEdge
	if err := s.db.Find(&rows, "room_id = ? AND epoch = ?", roomID, lease.Epoch).Error; err != nil {
		return err
	}

	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// 组装边集：parent 级联端点（Register 自报）+ cascade token（绑定 room+epoch+edge）。
	edges := make([]sfuctl.Edge, 0, len(rows))
	involved := map[uuid.UUID]bool{lease.AnchorNodeID: true}
	for _, row := range rows {
		involved[row.ParentNodeID] = true
		involved[row.ChildNodeID] = true
		parentInfo, err := sfuctl.Dir().Node(row.ParentNodeID)
		if err != nil {
			keep(fmt.Errorf("父节点 %s 不可用: %w", row.ParentNodeID, err))
			continue
		}
		if parentInfo.CascadeEndpoint == "" {
			keep(fmt.Errorf("父节点 %s 未上报级联端点", row.ParentNodeID))
			continue
		}
		token, err := s.tokens.SignCascade(roomID.String(), uint64(lease.Epoch),
			row.ParentNodeID.String(), row.ChildNodeID.String(), cascadeTokenTTL)
		if err != nil {
			keep(fmt.Errorf("签发级联 token 失败: %w", err))
			continue
		}
		edges = append(edges, sfuctl.Edge{
			ParentNodeID: row.ParentNodeID, ChildNodeID: row.ChildNodeID,
			ParentCascadeEndpoint: parentInfo.CascadeEndpoint,
			CascadeToken:          token,
		})
	}

	// 先租约后边集（SFU 侧 epoch 匹配 + 未过期才开转发，docs 08 §3.1）。
	for nodeID := range involved {
		if err := s.pushLease(nodeID, lease); err != nil {
			keep(fmt.Errorf("向节点 %s 下发租约失败: %w", nodeID, err))
		}
	}
	if len(edges) > 0 {
		keep(sfuctl.Ctl().SetCascadeEdges(roomID, uint64(lease.Epoch), edges))
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// 进房挂边（docs 08 §4.3 顺序）
// ---------------------------------------------------------------------------

// ensureCascade 保证 nodeID 在 roomID 的级联图中可达 anchor；边未就绪不放行进房
// （EnsureLogicalRoom 已由调用方完成；本函数负责：租约 → 边集 → 等 EdgeUp）。
// 必须已持有 s.mu。
func (s *Service) ensureCascade(guildID, roomID, nodeID uuid.UUID) error {
	var lease model.VoiceAnchorLease
	err := s.db.First(&lease, "room_id = ?", roomID).Error
	if err != nil {
		// 首人进房：其节点即初始 Anchor（docs 08 B.3），epoch 从 1 开始，无边可等。
		lease = model.VoiceAnchorLease{
			RoomID:         roomID,
			GuildID:        guildID,
			AnchorNodeID:   nodeID,
			Epoch:          1,
			LeaseExpiresAt: time.Now().Add(anchorLeaseTTL),
		}
		if err := s.db.Create(&lease).Error; err != nil {
			return err
		}
		return s.pushLease(nodeID, lease)
	}

	// 续约（周期续约由 leaseMaintenanceLoop 负责，这里保证进房路径租约新鲜）。
	lease.LeaseExpiresAt = time.Now().Add(anchorLeaseTTL)
	if err := s.db.Model(&model.VoiceAnchorLease{}).Where("room_id = ?", roomID).
		Update("lease_expires_at", lease.LeaseExpiresAt).Error; err != nil {
		return err
	}
	if nodeID == lease.AnchorNodeID {
		return s.pushLease(nodeID, lease)
	}

	// 已有到根的边：不重复挂边，但仍以「边就绪」为进房门槛（docs 08 C.4/E.3）。
	var existing model.VoiceCascadeEdge
	if s.db.First(&existing, "room_id = ? AND child_node_id = ?", roomID, nodeID).Error == nil {
		if err := sfuctl.Ctl().WaitEdgeUp(roomID, uint64(lease.Epoch),
			existing.ParentNodeID, nodeID, edgeUpTimeout); err != nil {
			return err
		}
		return nil
	}

	// 级联不出池校验（docs 08 §6.3）：建边两端必须 ∈ 该 Guild 节点池。
	if !s.nodeInPool(guildID, nodeID) || !s.nodeInPool(guildID, lease.AnchorNodeID) {
		return fmt.Errorf("级联边两端必须都在服务器节点池内")
	}

	// 星型深度 1：新节点直接挂 anchor（hub 深度 2 的插入策略留接口，docs 08 §4.1）。
	edge := model.VoiceCascadeEdge{
		ID:           uuid.New(),
		RoomID:       roomID,
		ChildNodeID:  nodeID,
		ParentNodeID: lease.AnchorNodeID,
		Epoch:        lease.Epoch,
	}
	if err := s.db.Create(&edge).Error; err != nil {
		return err
	}
	if err := s.pushCascadeState(lease); err != nil {
		// 推送部分失败只记录：真正的门槛是下面的 WaitEdgeUp。
		log.Printf("voice: 房间 %s 级联状态下发部分失败: %v", roomID, err)
	}
	if err := sfuctl.Ctl().WaitEdgeUp(roomID, uint64(lease.Epoch),
		lease.AnchorNodeID, nodeID, edgeUpTimeout); err != nil {
		// 边未就绪：摘边回滚并重发边集，交由上层换 fallback 节点或 503
		//（docs 08 §10：不留半残状态）。
		s.db.Delete(&model.VoiceCascadeEdge{}, "id = ?", edge.ID)
		if pushErr := s.pushCascadeState(lease); pushErr != nil {
			log.Printf("voice: 房间 %s 回滚边集下发失败: %v", roomID, pushErr)
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// 离房 / 换根
// ---------------------------------------------------------------------------

// cascadeAfterLeave 用户离开（或迁出）后维护级联图。必须已持有 s.mu。
// - 节点上同房再无用户：摘边；
// - 摘的是 anchor 且房内还有人：走 §7.1 换根；
// - 房间空了：吊销租约、清边、CloseRoom。
func (s *Service) cascadeAfterLeave(guildID, roomID, nodeID uuid.UUID) {
	roomUsers, err := s.roomUsersByNode(roomID)
	if err != nil {
		return
	}
	if len(roomUsers) == 0 {
		// 房间无人：整体回收（CloseLogicalRoom 同步拆节点侧级联状态与边）。
		var lease model.VoiceAnchorLease
		if s.db.First(&lease, "room_id = ?", roomID).Error == nil {
			var edges []model.VoiceCascadeEdge
			s.db.Find(&edges, "room_id = ?", roomID)
			closed := map[uuid.UUID]bool{}
			for _, e := range edges {
				for _, n := range []uuid.UUID{e.ParentNodeID, e.ChildNodeID} {
					if !closed[n] {
						closed[n] = true
						_ = sfuctl.Ctl().CloseRoom(n, roomID)
					}
				}
			}
			if !closed[lease.AnchorNodeID] {
				closed[lease.AnchorNodeID] = true
				_ = sfuctl.Ctl().CloseRoom(lease.AnchorNodeID, roomID)
			}
			if nodeID != uuid.Nil && !closed[nodeID] {
				_ = sfuctl.Ctl().CloseRoom(nodeID, roomID)
			}
		} else if nodeID != uuid.Nil {
			_ = sfuctl.Ctl().CloseRoom(nodeID, roomID)
		}
		s.db.Delete(&model.VoiceCascadeEdge{}, "room_id = ?", roomID)
		s.db.Delete(&model.VoiceAnchorLease{}, "room_id = ?", roomID)
		return
	}
	if nodeID == uuid.Nil || roomUsers[nodeID] > 0 {
		return // 节点上还有同房用户，图不变。
	}
	var lease model.VoiceAnchorLease
	if s.db.First(&lease, "room_id = ?", roomID).Error != nil {
		return
	}
	if lease.AnchorNodeID == nodeID {
		// 空掉的是根：换根（docs 08 §7）。
		if err := s.reRoot(guildID, roomID, map[uuid.UUID]bool{nodeID: true}); err != nil {
			log.Printf("voice: 房间 %s 换根失败: %v", roomID, err)
		}
		_ = sfuctl.Ctl().CloseRoom(nodeID, roomID)
		return
	}
	// 叶节点空了：摘边并重发当前边集（同 epoch 幂等重发，SFU 拆除被移除的边）。
	s.db.Delete(&model.VoiceCascadeEdge{}, "room_id = ? AND child_node_id = ?", roomID, nodeID)
	if err := s.pushCascadeState(lease); err != nil {
		log.Printf("voice: 房间 %s 摘边后下发失败: %v", roomID, err)
	}
	_ = sfuctl.Ctl().CloseRoom(nodeID, roomID)
}

// reRoot 换根，严格按 docs 08 §7.1 顺序：
// 冻结旧图（本函数持锁期间不接受变更）→ 选新根（人最多优先，排除 exclude）
// → 升 epoch → SetAnchorLease → 重建星型边并下发 → 用户迁移由调用方（迁移引擎）接续。
// 必须已持有 s.mu。
func (s *Service) reRoot(guildID, roomID uuid.UUID, exclude map[uuid.UUID]bool) error {
	var lease model.VoiceAnchorLease
	if err := s.db.First(&lease, "room_id = ?", roomID).Error; err != nil {
		return err
	}
	roomUsers, err := s.roomUsersByNode(roomID)
	if err != nil {
		return err
	}
	// 候选 = 房内有用户、未被排除（死亡/Drain）、仍在池内的节点。
	candidates := make([]anchorCandidate, 0, len(roomUsers))
	nodeIDs := make([]uuid.UUID, 0, len(roomUsers))
	for nodeID, count := range roomUsers {
		if exclude[nodeID] {
			continue
		}
		info, err := sfuctl.Dir().Node(nodeID)
		if err != nil || !s.nodeInPool(guildID, nodeID) {
			continue
		}
		candidates = append(candidates, anchorCandidate{
			NodeID:     nodeID,
			RoomUsers:  count,
			CPUPercent: info.CPUPercent,
			Region:     info.Region,
		})
		nodeIDs = append(nodeIDs, nodeID)
	}
	newAnchor, ok := electAnchor(candidates, "")
	if !ok {
		return fmt.Errorf("房间 %s 无可用节点担任新 anchor", roomID)
	}
	epoch := nextEpoch(lease.Epoch)
	expire := time.Now().Add(anchorLeaseTTL)
	if err := s.db.Model(&model.VoiceAnchorLease{}).Where("room_id = ?", roomID).Updates(map[string]any{
		"anchor_node_id":   newAnchor,
		"epoch":            epoch,
		"lease_expires_at": expire,
		"degraded":         false,
	}).Error; err != nil {
		return err
	}
	lease.AnchorNodeID, lease.Epoch, lease.LeaseExpiresAt, lease.Degraded = newAnchor, epoch, expire, false
	// 重建星型边（存活节点挂新根）。
	s.db.Delete(&model.VoiceCascadeEdge{}, "room_id = ?", roomID)
	newEdges := starEdges(newAnchor, nodeIDs)
	for _, edge := range newEdges {
		s.db.Create(&model.VoiceCascadeEdge{
			ID:           uuid.New(),
			RoomID:       roomID,
			ChildNodeID:  edge.ChildNodeID,
			ParentNodeID: edge.ParentNodeID,
			Epoch:        epoch,
		})
	}
	// 新 epoch 租约 + 边集整体下发（docs 08 §7.1 步骤 3–4）。
	if err := s.pushCascadeState(lease); err != nil {
		log.Printf("voice: 房间 %s 换根后级联状态下发部分失败: %v", roomID, err)
	}
	// 步骤 5：等关键 EdgeUp——新图每条边就绪后才放行后续用户迁移
	//（调用方 migrateNode 在本函数返回后才创建迁移 job）。
	// 单边超时仅记录不中断：迁移 PREPARE 阶段的 ensureCascade 会再次以
	// 「边就绪」为门槛兜底（docs 08 C.4/E.3），SFU child 侧拨号循环自动重连。
	for _, edge := range newEdges {
		if err := sfuctl.Ctl().WaitEdgeUp(roomID, uint64(epoch),
			edge.ParentNodeID, edge.ChildNodeID, edgeUpTimeout); err != nil {
			log.Printf("voice: 房间 %s 换根后关键边 %s→%s 未在时限内就绪: %v",
				roomID, edge.ParentNodeID, edge.ChildNodeID, err)
		}
	}
	log.Printf("voice: 房间 %s 完成换根：anchor=%s epoch=%d 边数=%d（docs 08 §7.1）",
		roomID, newAnchor, epoch, len(newEdges))
	return nil
}

// ---------------------------------------------------------------------------
// EdgeDown 处理（docs 08 §7.2 / 15 BI.2）
// ---------------------------------------------------------------------------

// handleEdgeDown 级联边断开：
//   - 旧 epoch / 已不在图中的边：忽略（迟到消息）；
//   - 根故障：仅标记 degraded + 日志（完整切根属 M4；节点判死后由
//     InternalNodeDown → migrateNode → reRoot 兜底）；
//   - 叶边断且根存活：重发当前边集（刷新 token/端点），SFU child 侧自动退避重拨。
func (s *Service) handleEdgeDown(p eventbus.EdgeDownPayload) {
	roomID, err1 := uuid.Parse(p.RoomID)
	parentID, err2 := uuid.Parse(p.ParentNodeID)
	childID, err3 := uuid.Parse(p.ChildNodeID)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var lease model.VoiceAnchorLease
	if s.db.First(&lease, "room_id = ?", roomID).Error != nil {
		return
	}
	if p.Epoch < uint64(lease.Epoch) {
		return // 旧图残报
	}
	var edge model.VoiceCascadeEdge
	if s.db.First(&edge, "room_id = ? AND parent_node_id = ? AND child_node_id = ?",
		roomID, parentID, childID).Error != nil {
		return // 边已不在当前图中
	}

	if parentID == lease.AnchorNodeID {
		if info, err := sfuctl.Dir().Node(parentID); err != nil || !info.Online {
			// 根疑似死亡：标记降级（docs 08 §7.1 完整切根属 M4 边界，本轮只标记 + 日志；
			// 心跳判死后 InternalNodeDown → migrateNode → reRoot 完成实际恢复）。
			if !lease.Degraded {
				s.db.Model(&model.VoiceAnchorLease{}).Where("room_id = ?", roomID).
					Update("degraded", true)
				log.Printf("voice: 房间 %s 根节点 %s 疑似故障（EdgeDown 且离线），标记 degraded，等待判死切根",
					roomID, parentID)
			}
			return
		}
	}
	// 叶边断且根存活：重发边或触发迁移（docs 08 §7.2）。首选重发边集（幂等，
	// 刷新 cascade token 与端点），SFU child 拨号循环会自动重连。
	log.Printf("voice: 房间 %s 级联边 %s→%s 断开，重发边集修复", roomID, parentID, childID)
	if err := s.pushCascadeState(lease); err != nil {
		log.Printf("voice: 房间 %s EdgeDown 修复下发失败: %v", roomID, err)
	}
	// 分区升级（docs 09 §3.3）：同一叶边 60s 内 ≥3 次 EdgeDown（重拨修复无效）
	// → 该叶节点上本房用户 MIGRATE_LEAF（reason=PARTITION）；升级后冷却防震荡。
	flapKey := roomID.String() + "/" + parentID.String() + ">" + childID.String()
	if s.edgeFlaps.record(flapKey, time.Now()) {
		s.escalatePartition(lease.GuildID, roomID, childID)
	}
}

// ---------------------------------------------------------------------------
// 租约续约循环（docs 08 §3.1：心跳续约由 Server 驱动）
// ---------------------------------------------------------------------------

// leaseMaintenanceLoop 周期续约所有活跃房间的租约并重发级联状态
// （顺带轮换 cascade token；SetCascadeEdges 同 epoch 幂等重发不拆边）。
func (s *Service) leaseMaintenanceLoop() {
	ticker := time.NewTicker(leaseRenewInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.renewLeases()
	}
}

// renewLeases 续约一轮。持锁串行化与编排动作互斥。
func (s *Service) renewLeases() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var leases []model.VoiceAnchorLease
	if err := s.db.Find(&leases).Error; err != nil {
		return
	}
	for _, lease := range leases {
		lease.LeaseExpiresAt = time.Now().Add(anchorLeaseTTL)
		if err := s.db.Model(&model.VoiceAnchorLease{}).Where("room_id = ?", lease.RoomID).
			Update("lease_expires_at", lease.LeaseExpiresAt).Error; err != nil {
			continue
		}
		if err := s.pushCascadeState(lease); err != nil {
			log.Printf("voice: 房间 %s 租约续约下发部分失败: %v", lease.RoomID, err)
		}
	}
}
