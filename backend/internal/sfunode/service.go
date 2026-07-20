package sfunode

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNodeOffline 节点不在线（控制通道未连接）。
var ErrNodeOffline = errors.New("SFU 节点不在线")

// nodeEventPayload 节点内部事件载荷（InternalNodeUp/Down/Draining）。
type nodeEventPayload struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

// Service 节点生命周期与节点池的业务逻辑（DB 权威 + 审计 + 内部事件）。
type Service struct {
	db  *gorm.DB
	bus *eventbus.Bus
	ca  *ClusterCA
	hub *Hub // 控制通道连接注册表；Register 时回填
}

func NewService(db *gorm.DB, bus *eventbus.Bus, ca *ClusterCA) *Service {
	return &Service{db: db, bus: bus, ca: ca}
}

func (s *Service) publishNodeEvent(eventType string, node model.SfuNode) {
	s.bus.Publish(eventbus.Event{
		Type:    eventType,
		Payload: nodeEventPayload{NodeID: node.ID.String(), Status: node.Status},
	})
}

// CreateNode 创建占位节点并签发一次性 enrollment token（docs 03 §4.1 步骤 1）。
// 返回的 token 明文仅此一次，库中只存哈希。
func (s *Service) CreateNode(actor model.User, displayName string, labels map[string]string, ttl time.Duration) (model.SfuNode, string, error) {
	if ttl <= 0 {
		ttl = DefaultEnrollmentTokenTTL
	}
	token, hash, err := NewEnrollmentToken()
	if err != nil {
		return model.SfuNode{}, "", err
	}
	expires := time.Now().UTC().Add(ttl)
	node := model.SfuNode{
		ID:                       uuid.New(),
		DisplayName:              displayName,
		Status:                   model.SfuNodePendingEnrollment,
		Labels:                   labels,
		EnrollmentTokenHash:      hash,
		EnrollmentTokenExpiresAt: &expires,
	}
	if err := s.db.Create(&node).Error; err != nil {
		return model.SfuNode{}, "", fmt.Errorf("创建节点失败: %w", err)
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.create", TargetType: "sfu_node", TargetID: node.ID.String(),
		Detail: map[string]any{"display_name": displayName, "token_expires_at": expires},
	})
	return node, token, nil
}

// EnrollResult enrollment 成功后的下发内容。
type EnrollResult struct {
	Node        model.SfuNode
	CertPEM     []byte
	CABundlePEM []byte
	NotAfter    time.Time
}

// Enroll 完成节点 enrollment：校验一次性 token → 签发证书 → 状态转 ENROLLED（docs 03 §4.1 步骤 3）。
// token 校验与作废在同一事务内完成，保证一次性。
func (s *Service) Enroll(nodeID uuid.UUID, token string, csrPEM []byte) (EnrollResult, error) {
	var result EnrollResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var node model.SfuNode
		// 行锁防止并发重放同一 token。
		if err := tx.Clauses(forUpdate()).First(&node, "id = ?", nodeID).Error; err != nil {
			return ErrEnrollTokenMismatch
		}
		if err := ValidateEnrollment(node, token, time.Now().UTC()); err != nil {
			return err
		}
		certPEM, fingerprint, notAfter, err := s.ca.SignNodeCSR(csrPEM, node.ID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"status":                      model.SfuNodeEnrolled,
			"cert_fingerprint":            fingerprint,
			"cert_not_after":              notAfter,
			"enrollment_token_hash":       "", // 一次性：成功即作废
			"enrollment_token_expires_at": nil,
			"enrolled_at":                 now,
		}
		if err := tx.Model(&node).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新节点状态失败: %w", err)
		}
		node.Status = model.SfuNodeEnrolled
		node.CertFingerprint = fingerprint
		node.CertNotAfter = &notAfter
		node.EnrolledAt = &now
		result = EnrollResult{Node: node, CertPEM: certPEM, CABundlePEM: s.ca.CABundlePEM(), NotAfter: notAfter}
		return nil
	})
	if err != nil {
		return EnrollResult{}, err
	}
	audit.Log(s.db, audit.Entry{
		ActorType: "node",
		Action:    "sfu_node.enroll", TargetType: "sfu_node", TargetID: nodeID.String(),
		Detail: map[string]any{"cert_fingerprint": result.Node.CertFingerprint, "cert_not_after": result.NotAfter},
	})
	return result, nil
}

// RenewCertificate 已入册节点凭现有 mTLS 身份轮换证书；保留旧指纹形成短暂双指纹窗口（docs 03 §4.4）。
func (s *Service) RenewCertificate(nodeID uuid.UUID, csrPEM []byte) (EnrollResult, error) {
	var result EnrollResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var node model.SfuNode
		if err := tx.Clauses(forUpdate()).First(&node, "id = ?", nodeID).Error; err != nil {
			return fmt.Errorf("节点不存在")
		}
		switch node.Status {
		case model.SfuNodeEnrolled, model.SfuNodeOnline, model.SfuNodeDraining:
		default:
			return fmt.Errorf("节点状态 %s 不允许轮换证书", node.Status)
		}
		certPEM, fingerprint, notAfter, err := s.ca.SignNodeCSR(csrPEM, node.ID)
		if err != nil {
			return err
		}
		updates := map[string]any{
			"prev_cert_fingerprint": node.CertFingerprint,
			"cert_fingerprint":      fingerprint,
			"cert_not_after":        notAfter,
		}
		if err := tx.Model(&node).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新证书指纹失败: %w", err)
		}
		node.PrevCertFingerprint = node.CertFingerprint
		node.CertFingerprint = fingerprint
		node.CertNotAfter = &notAfter
		result = EnrollResult{Node: node, CertPEM: certPEM, CABundlePEM: s.ca.CABundlePEM(), NotAfter: notAfter}
		return nil
	})
	if err != nil {
		return EnrollResult{}, err
	}
	audit.Log(s.db, audit.Entry{
		ActorType: "node",
		Action:    "sfu_node.renew_certificate", TargetType: "sfu_node", TargetID: nodeID.String(),
		Detail: map[string]any{"cert_fingerprint": result.Node.CertFingerprint, "cert_not_after": result.NotAfter},
	})
	return result, nil
}

// transitionStatus 在事务内校验状态机并落库；返回更新后的节点。
func (s *Service) transitionStatus(nodeID uuid.UUID, to string, extraUpdates map[string]any) (model.SfuNode, error) {
	var node model.SfuNode
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(forUpdate()).First(&node, "id = ?", nodeID).Error; err != nil {
			return fmt.Errorf("节点不存在")
		}
		if _, err := Transition(node.Status, to); err != nil {
			return err
		}
		updates := map[string]any{"status": to}
		for k, v := range extraUpdates {
			updates[k] = v
		}
		if err := tx.Model(&node).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新节点状态失败: %w", err)
		}
		node.Status = to
		return nil
	})
	return node, err
}

// Revoke 吊销节点证书（终态）并踢掉控制连接（docs 03 §4.4 私钥泄露场景）。
func (s *Service) Revoke(actor model.User, nodeID uuid.UUID) (model.SfuNode, error) {
	node, err := s.transitionStatus(nodeID, model.SfuNodeRevoked, map[string]any{
		"enabled_for_scheduling": false,
	})
	if err != nil {
		return node, err
	}
	if s.hub != nil {
		s.hub.Kick(nodeID, "证书已吊销")
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.revoke", TargetType: "sfu_node", TargetID: nodeID.String(),
	})
	s.publishNodeEvent(eventbus.InternalNodeDown, node)
	return node, nil
}

// Drain 节点进入排空：状态转 DRAINING、经 sfuctl（gRPC 桥接）下发 Drain 指令、
// 发布 InternalNodeDraining（voice 迁移引擎据此批量迁出，docs 09 I.6）。
func (s *Service) Drain(actor model.User, nodeID uuid.UUID) (model.SfuNode, error) {
	node, err := s.transitionStatus(nodeID, model.SfuNodeDraining, nil)
	if err != nil {
		return node, err
	}
	// 节点不在线时仅落状态（上线后调度自然排除）；下发失败不回滚，仅记录。
	// deadline = 60s 硬迁时限（docs 09 I.6）；实际硬迁由 voice 迁移引擎兜底。
	if err := sfuctl.Ctl().DrainNode(nodeID, time.Now().Add(60*time.Second)); err != nil {
		audit.Log(s.db, audit.Entry{
			ActorType: "auto", Action: "sfu_node.drain_command_failed",
			TargetType: "sfu_node", TargetID: nodeID.String(),
			Detail: map[string]any{"error": err.Error()},
		})
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.drain", TargetType: "sfu_node", TargetID: nodeID.String(),
	})
	s.publishNodeEvent(eventbus.InternalNodeDraining, node)
	return node, nil
}

// Undrain 排空结束：仍在线 → ONLINE，否则 → ENROLLED（在线判定走 sfuctl 目录，即 gRPC 注册表）。
func (s *Service) Undrain(actor model.User, nodeID uuid.UUID) (model.SfuNode, error) {
	target := model.SfuNodeEnrolled
	if info, err := sfuctl.Dir().Node(nodeID); err == nil && info.Online {
		target = model.SfuNodeOnline
	}
	node, err := s.transitionStatus(nodeID, target, nil)
	if err != nil {
		return node, err
	}
	// 通知节点恢复接受新会话（Drain{cancel=true}）；不在线仅落状态，失败只记录。
	if err := sfuctl.Ctl().UndrainNode(nodeID); err != nil {
		audit.Log(s.db, audit.Entry{
			ActorType: "auto", Action: "sfu_node.undrain_command_failed",
			TargetType: "sfu_node", TargetID: nodeID.String(),
			Detail: map[string]any{"error": err.Error()},
		})
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.undrain", TargetType: "sfu_node", TargetID: nodeID.String(),
	})
	if target == model.SfuNodeOnline {
		s.publishNodeEvent(eventbus.InternalNodeUp, node)
	}
	return node, nil
}

// Disable 管理员禁用节点：拒绝连接并踢掉现有控制连接。
func (s *Service) Disable(actor model.User, nodeID uuid.UUID) (model.SfuNode, error) {
	node, err := s.transitionStatus(nodeID, model.SfuNodeDisabled, map[string]any{
		"enabled_for_scheduling": false,
	})
	if err != nil {
		return node, err
	}
	if s.hub != nil {
		s.hub.Kick(nodeID, "节点已被管理员禁用")
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.disable", TargetType: "sfu_node", TargetID: nodeID.String(),
	})
	s.publishNodeEvent(eventbus.InternalNodeDown, node)
	return node, nil
}

// Enable 解除禁用：DISABLED → ENROLLED（需节点重新连上才 ONLINE）。
func (s *Service) Enable(actor model.User, nodeID uuid.UUID) (model.SfuNode, error) {
	node, err := s.transitionStatus(nodeID, model.SfuNodeEnrolled, nil)
	if err != nil {
		return node, err
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.enable", TargetType: "sfu_node", TargetID: nodeID.String(),
	})
	return node, nil
}

// UpdateNode 更新节点管理属性（调度开关、名称、标签、平台默认池归属）。
func (s *Service) UpdateNode(actor model.User, nodeID uuid.UUID, updates map[string]any) (model.SfuNode, error) {
	var node model.SfuNode
	if err := s.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return node, fmt.Errorf("节点不存在")
	}
	if len(updates) == 0 {
		return node, nil
	}
	if err := s.db.Model(&node).Updates(updates).Error; err != nil {
		return node, fmt.Errorf("更新节点失败: %w", err)
	}
	if err := s.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return node, fmt.Errorf("重新读取节点失败: %w", err)
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "sfu_node.update", TargetType: "sfu_node", TargetID: nodeID.String(),
		Detail: map[string]any{"updates": updates},
	})
	return node, nil
}

// ---- 控制通道回调（controlStore 实现）----

// controlStore Hub 依赖的最小存储接口（便于测试 mock）。
type controlStore interface {
	// NodeByFingerprint 按证书指纹查节点（含轮换窗口内的旧指纹）。
	NodeByFingerprint(fp string) (model.SfuNode, error)
	// MarkOnline 节点完成 Register：更新自报端点/容量并转 ONLINE。
	MarkOnline(nodeID uuid.UUID, reg registerPayload) (model.SfuNode, error)
	// MarkOffline 连接断开/心跳超时：ONLINE → ENROLLED（DRAINING 保持）。
	MarkOffline(nodeID uuid.UUID) error
	// SaveHeartbeat 持久化心跳容量快照与 last_seen_at。
	SaveHeartbeat(nodeID uuid.UUID, hb heartbeatPayload) error
}

func (s *Service) NodeByFingerprint(fp string) (model.SfuNode, error) {
	var node model.SfuNode
	err := s.db.Where("cert_fingerprint = ? OR prev_cert_fingerprint = ?", fp, fp).First(&node).Error
	if err != nil {
		return node, fmt.Errorf("证书指纹未登记")
	}
	return node, nil
}

func (s *Service) MarkOnline(nodeID uuid.UUID, reg registerPayload) (model.SfuNode, error) {
	var node model.SfuNode
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(forUpdate()).First(&node, "id = ?", nodeID).Error; err != nil {
			return fmt.Errorf("节点不存在")
		}
		now := time.Now().UTC()
		updates := map[string]any{"last_seen_at": now}
		if reg.ControlAdvertise != "" {
			updates["control_advertise"] = reg.ControlAdvertise
		}
		if reg.WebRTCHosts != nil {
			updates["webrtc_hosts"] = model.SfuStringList(reg.WebRTCHosts)
		}
		if reg.MaxUsers > 0 {
			updates["max_users"] = reg.MaxUsers
		}
		// DRAINING 节点重连保持 DRAINING；ENROLLED → ONLINE。
		if node.Status == model.SfuNodeEnrolled {
			updates["status"] = model.SfuNodeOnline
			node.Status = model.SfuNodeOnline
		}
		if err := tx.Model(&node).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新节点在线状态失败: %w", err)
		}
		node.LastSeenAt = &now
		return nil
	})
	if err != nil {
		return node, err
	}
	s.publishNodeEvent(eventbus.InternalNodeUp, node)
	return node, nil
}

func (s *Service) MarkOffline(nodeID uuid.UUID) error {
	var node model.SfuNode
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(forUpdate()).First(&node, "id = ?", nodeID).Error; err != nil {
			return fmt.Errorf("节点不存在")
		}
		// 仅 ONLINE 降级为 ENROLLED（离线）；DRAINING 等状态保持不变。
		if node.Status == model.SfuNodeOnline {
			if err := tx.Model(&node).Update("status", model.SfuNodeEnrolled).Error; err != nil {
				return fmt.Errorf("标记节点离线失败: %w", err)
			}
			node.Status = model.SfuNodeEnrolled
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publishNodeEvent(eventbus.InternalNodeDown, node)
	return nil
}

func (s *Service) SaveHeartbeat(nodeID uuid.UUID, hb heartbeatPayload) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"current_users":      hb.CurrentUsers,
		"cpu_pct":            hb.CPUPct,
		"mem_pct":            hb.MemPct,
		"bandwidth_out_mbps": hb.BandwidthOutMbps,
		"screen_tracks":      hb.ScreenTracks,
		"last_seen_at":       now,
	}
	if hb.NodeRTTMs != nil {
		updates["node_rtt_ms"] = model.SfuFloatMap(hb.NodeRTTMs)
	}
	return s.db.Model(&model.SfuNode{}).Where("id = ?", nodeID).Updates(updates).Error
}

// forUpdate SELECT ... FOR UPDATE 行锁（PostgreSQL）。
func forUpdate() clause.Locking { return clause.Locking{Strength: "UPDATE"} }
