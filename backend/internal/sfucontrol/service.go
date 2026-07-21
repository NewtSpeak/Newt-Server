// Package sfucontrol SFU 控制面 gRPC 服务（docs 03、docs 协议 §3）：
// EnrollmentService（TLS，一次性 token 换证书）+ ControlService（mTLS 双向流），
// 以及内存节点注册表与心跳超时监控。
package sfucontrol

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"
	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
	"github.com/owlspeak/owl-server/backend/internal/ca"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// lastSeenThrottle 心跳落库节流窗口（内存注册表实时，DB 最多 30s 一次）。
const lastSeenThrottle = 30 * time.Second

// Config 控制面服务配置（由 internal/config 映射）。
type Config struct {
	// Address TLS 监听地址（SFU_CONTROL_ADDRESS）。
	Address string
	// PublicEndpoint Enroll 响应下发的对外控制面地址。
	PublicEndpoint string
	// TLSSANs 服务端证书 SAN。
	TLSSANs []string
	// HeartbeatIntervalMS RegisterAck 下发的心跳间隔。
	HeartbeatIntervalMS int
	// EarlyDeath BI.3 提前判死组合规则（docs 15 §5；零值字段取定稿默认）。
	EarlyDeath EarlyDeathConfig
	// AuditIngestURL / AuditIngestToken 经 RegisterAck 下发给 SFU（adminpresence 音频审计）。
	// 为空则不下发，SFU 保持本地配置或仅落盘。
	AuditIngestURL   string
	AuditIngestToken string
}

// Service EnrollmentService + ControlService 的实现。
type Service struct {
	owlsfuv1.UnimplementedEnrollmentServiceServer
	owlsfuv1.UnimplementedControlServiceServer

	db        *gorm.DB
	authority *ca.CA
	tokens    *mediatoken.Manager
	registry  *Registry
	cfg       Config
	logger    *slog.Logger
	// onNodeDown 判死回调（经 SetNodeDownHandler 注入）：由装配层发布
	// eventbus.InternalNodeDown，交给 voice 迁移引擎接管（docs 09 §3、15 BI）。
	onNodeDown func(nodeID uuid.UUID)
	// onScreenTrackActive 屏幕轨发布成功回调（经 SetScreenTrackActiveHandler 注入）。
	onScreenTrackActive func(channelID, userID uuid.UUID)
	// onEdgeDown 级联 EdgeDown 回调（经 SetEdgeDownHandler 注入）：由装配层
	// 发布 eventbus.InternalEdgeDown，voice 编排据此补边或标记房间降级（docs 08 §7.2）。
	onEdgeDown func(es *owlsfuv1.EdgeStatus)
	// onVoiceConnectedChange SFU 上报 PARTICIPANT_JOINED/LEFT 后、已更新 VoiceState.connected
	// 时调用（经 SetVoiceConnectedChangeHandler 注入）：由装配层广播 VOICE_STATE_UPDATE，
	// 使刷新/ICE 断线后的 connected 变化能实时反映到其他客户端名单。
	onVoiceConnectedChange func(vs model.VoiceState)
}

// SetNodeDownHandler 注入节点判死回调（心跳监控在硬判死窗口触发时调用）。
func (s *Service) SetNodeDownHandler(handler func(nodeID uuid.UUID)) { s.onNodeDown = handler }

// SetScreenTrackActiveHandler 注入屏幕轨发布成功回调（docs 14 BC.1 步骤 5：
// SFU 上报 SCREEN_TRACK_ACTIVE → ScreenSlot RESERVED→ACTIVE，防 60s 预留超时误回收）。
func (s *Service) SetScreenTrackActiveHandler(handler func(channelID, userID uuid.UUID)) {
	s.onScreenTrackActive = handler
}

// SetEdgeDownHandler 注入级联 EdgeDown 回调（docs 08 §7.2 / 15 BI.2：
// 边断作为提前信号源之一，voice 编排据此补边或标记降级）。
func (s *Service) SetEdgeDownHandler(handler func(es *owlsfuv1.EdgeStatus)) {
	s.onEdgeDown = handler
}

// SetVoiceConnectedChangeHandler 注入 VoiceState.connected 翻转回调
//（PARTICIPANT_JOINED → true / PARTICIPANT_LEFT|ICE_FAILED → false）。
func (s *Service) SetVoiceConnectedChangeHandler(handler func(vs model.VoiceState)) {
	s.onVoiceConnectedChange = handler
}

func NewService(db *gorm.DB, authority *ca.CA, tokens *mediatoken.Manager, registry *Registry, cfg Config) *Service {
	return &Service{
		db:        db,
		authority: authority,
		tokens:    tokens,
		registry:  registry,
		cfg:       cfg,
		logger:    slog.Default().With("component", "sfucontrol"),
	}
}

// Serve 启动 TLS gRPC 监听并阻塞；心跳超时监控随之启动，ctx 取消时优雅退出。
func (s *Service) Serve(ctx context.Context) error {
	// 启动对账：进程重启后内存注册表为空，库中残留的 ONLINE 不可能有活跃控制流，
	// 统一回落 ENROLLED（离线）。节点重连注册后由 handleRegister 置回 ONLINE，
	// 保证 status（库）与 online（注册表实时视图）口径一致（docs 03 §8 状态机）。
	if err := s.db.Model(&model.SfuNode{}).Where("status = ?", model.SfuNodeOnline).
		Update("status", model.SfuNodeEnrolled).Error; err != nil {
		return err
	}
	serverCert, err := s.authority.ServerCert(s.cfg.TLSSANs)
	if err != nil {
		return err
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		// Enroll 阶段节点尚无证书，Control 流再强制校验客户端证书。
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  s.authority.Pool(),
		MinVersion: tls.VersionTLS12,
	}
	listener, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
	owlsfuv1.RegisterEnrollmentServiceServer(grpcServer, s)
	owlsfuv1.RegisterControlServiceServer(grpcServer, s)

	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	go s.runHeartbeatMonitor(monitorCtx)
	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()
	defer cancelMonitor()

	s.logger.Info("SFU 控制面监听启动", "address", s.cfg.Address)
	return grpcServer.Serve(listener)
}

// ---------- EnrollmentService ----------

// Enroll 一次性 token → CSR → 节点证书（docs 03 §4）。任何校验失败统一 PermissionDenied。
func (s *Service) Enroll(ctx context.Context, req *owlsfuv1.EnrollRequest) (*owlsfuv1.EnrollResponse, error) {
	deny := func(reason string, err error) (*owlsfuv1.EnrollResponse, error) {
		s.logger.Warn("enrollment 被拒绝", "node_id", req.GetNodeId(), "reason", reason, "error", err)
		return nil, status.Error(codes.PermissionDenied, "enrollment 校验失败")
	}
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return deny("node_id 无效", err)
	}
	var node model.SfuNode
	if err := s.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return deny("节点不存在", err)
	}
	if err := validateEnrollment(&node, req.GetEnrollmentToken(), time.Now().UTC()); err != nil {
		return deny("token 校验失败", err)
	}
	certPEM, fingerprint, notAfter, err := s.authority.SignNodeCSR(req.GetCsrPem(), nodeID.String())
	if err != nil {
		return deny("CSR 无效", err)
	}
	applyEnrollment(&node, fingerprint, notAfter)
	// WHERE status=PENDING_ENROLLMENT 保证并发下 token 一次性。
	result := s.db.Model(&model.SfuNode{}).
		Where("id = ? AND status = ?", node.ID, model.SfuNodePendingEnrollment).
		Updates(map[string]any{
			"status":                      node.Status,
			"cert_fingerprint":            node.CertFingerprint,
			"cert_not_after":              node.CertNotAfter,
			"enrolled_at":                 time.Now().UTC(),
			"enrollment_token_hash":       "",
			"enrollment_token_expires_at": nil,
		})
	if result.Error != nil {
		s.logger.Error("enrollment 落库失败", "node_id", node.ID, "error", result.Error)
		return nil, status.Error(codes.Internal, "enrollment 保存失败")
	}
	if result.RowsAffected == 0 {
		return deny("token 已被并发使用", nil)
	}
	s.logger.Info("节点 enrollment 成功", "node_id", node.ID, "fingerprint", fingerprint, "not_after", notAfter)
	return &owlsfuv1.EnrollResponse{
		CertificatePem:  certPEM,
		CaBundlePem:     s.authority.CertPEM(),
		ControlEndpoint: s.cfg.PublicEndpoint,
		MediaTokenKeys:  s.mediaTokenKeys(),
		CertNotAfter:    notAfter.Format(time.RFC3339),
	}, nil
}

// RenewCertificate 已认证节点（mTLS）用现有证书换新证书（docs 03 §4.4）。
func (s *Service) RenewCertificate(ctx context.Context, req *owlsfuv1.RenewCertificateRequest) (*owlsfuv1.RenewCertificateResponse, error) {
	node, err := s.nodeFromPeer(ctx)
	if err != nil {
		return nil, err
	}
	certPEM, fingerprint, notAfter, err := s.authority.SignNodeCSR(req.GetCsrPem(), node.ID.String())
	if err != nil {
		s.logger.Warn("续期 CSR 无效", "node_id", node.ID, "error", err)
		return nil, status.Error(codes.InvalidArgument, "CSR 无效")
	}
	err = s.db.Model(&model.SfuNode{}).Where("id = ?", node.ID).
		Updates(map[string]any{"cert_fingerprint": fingerprint, "cert_not_after": notAfter}).Error
	if err != nil {
		return nil, status.Error(codes.Internal, "证书续期保存失败")
	}
	s.logger.Info("节点证书续期", "node_id", node.ID, "fingerprint", fingerprint)
	return &owlsfuv1.RenewCertificateResponse{
		CertificatePem: certPEM,
		CaBundlePem:    s.authority.CertPEM(),
		CertNotAfter:   notAfter.Format(time.RFC3339),
	}, nil
}

// ---------- ControlService ----------

// Channel 控制通道双向流：mTLS 指纹认证 → Register/Heartbeat/RoomEvent/CommandAck。
func (s *Service) Channel(stream grpc.BidiStreamingServer[owlsfuv1.NodeMessage, owlsfuv1.ServerMessage]) error {
	node, err := s.nodeFromPeer(stream.Context())
	if err != nil {
		return err
	}
	logger := s.logger.With("node_id", node.ID)
	connection := s.registry.Attach(node.ID, stream)
	defer func() {
		s.registry.Detach(node.ID, connection)
		// 流断开 → 在线状态回 ENROLLED（离线）；DRAINING/DISABLED/REVOKED 不动。
		s.db.Model(&model.SfuNode{}).Where("id = ? AND status = ?", node.ID, model.SfuNodeOnline).
			Update("status", model.SfuNodeEnrolled)
		logger.Info("控制流断开")
	}()

	type received struct {
		message *owlsfuv1.NodeMessage
		err     error
	}
	messages := make(chan received)
	go func() {
		for {
			message, err := stream.Recv()
			select {
			case messages <- received{message, err}:
			case <-connection.done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var lastSeenWritten time.Time
	for {
		select {
		case <-connection.done:
			return nil
		case incoming := <-messages:
			if incoming.err != nil {
				return nil
			}
			switch payload := incoming.message.GetPayload().(type) {
			case *owlsfuv1.NodeMessage_Register:
				if err := s.handleRegister(node.ID, connection, payload.Register); err != nil {
					logger.Error("处理 Register 失败", "error", err)
					return err
				}
				lastSeenWritten = time.Now()
				logger.Info("节点上线", "wss_url", payload.Register.GetAdvertise().GetWssUrl())
			case *owlsfuv1.NodeMessage_Heartbeat:
				capacity := capacityFromProto(payload.Heartbeat.GetCapacity())
				s.registry.UpdateCapacity(node.ID, capacity)
				if time.Since(lastSeenWritten) >= lastSeenThrottle {
					lastSeenWritten = time.Now()
					s.db.Model(&model.SfuNode{}).
						Where("id = ? AND status IN ?", node.ID, []string{model.SfuNodeOnline, model.SfuNodeEnrolled}).
						Updates(map[string]any{
							"last_seen_at":  time.Now().UTC(),
							"status":        model.SfuNodeOnline,
							"max_users":     capacity.MaxUsers,
							"current_users": capacity.CurrentUsers,
						})
				}
			case *owlsfuv1.NodeMessage_RoomEvent:
				s.handleRoomEvent(logger, node.ID, payload.RoomEvent)
			case *owlsfuv1.NodeMessage_CommandAck:
				s.registry.Resolve(payload.CommandAck)
			case *owlsfuv1.NodeMessage_EdgeStatus:
				s.handleEdgeStatus(logger, node.ID, payload.EdgeStatus)
			}
		}
	}
}

func (s *Service) handleRegister(nodeID uuid.UUID, connection *conn, register *owlsfuv1.Register) error {
	advertise := register.GetAdvertise()
	capacity := capacityFromProto(register.GetCapacity())
	now := time.Now().UTC()
	err := s.db.Model(&model.SfuNode{}).Where("id = ?", nodeID).Updates(map[string]any{
		"status":            model.SfuNodeOnline,
		"advertise_wss_url": advertise.GetWssUrl(),
		"media_udp_port":    int(advertise.GetMediaUdpPort()),
		"media_ips":         model.SfuStringList(advertise.GetMediaIps()),
		"cascade_endpoint":  advertise.GetCascadeEndpoint(),
		"max_users":         capacity.MaxUsers,
		"current_users":     capacity.CurrentUsers,
		"last_seen_at":      now,
	}).Error
	if err != nil {
		return err
	}
	s.registry.UpdateCapacity(nodeID, capacity)
	return connection.send(&owlsfuv1.ServerMessage{Payload: &owlsfuv1.ServerMessage_RegisterAck{RegisterAck: &owlsfuv1.RegisterAck{
		NodeId:              nodeID.String(),
		HeartbeatIntervalMs: uint32(s.cfg.HeartbeatIntervalMS),
		MediaTokenKeys:      s.mediaTokenKeys(),
		// 音频审计上传配置：SFU 本地未配时采用此处（adminpresence）。
		AuditIngestUrl:   s.cfg.AuditIngestURL,
		AuditIngestToken: s.cfg.AuditIngestToken,
	}}})
}

func (s *Service) handleRoomEvent(logger *slog.Logger, nodeID uuid.UUID, event *owlsfuv1.RoomEvent) {
	sessionID, err := uuid.Parse(event.GetSessionId())
	if err != nil {
		logger.Warn("RoomEvent session_id 无效", "session_id", event.GetSessionId())
		return
	}
	switch event.GetType() {
	case owlsfuv1.RoomEvent_EVENT_TYPE_PARTICIPANT_JOINED:
		s.db.Model(&model.VoiceState{}).Where("voice_session_id = ?", sessionID).Update("connected", true)
		s.notifyVoiceConnectedChange(sessionID)
	case owlsfuv1.RoomEvent_EVENT_TYPE_PARTICIPANT_LEFT,
		owlsfuv1.RoomEvent_EVENT_TYPE_PARTICIPANT_ICE_FAILED:
		// 按 session_id 只标记 connected=false（幂等）；VoiceState 行的 channel_id 保留，
		// 便于客户端刷新后仍显示在房内并自动 rejoin（docs 09 FR-06）；
		// 完整离房由 /voice/leave、管理员踢人或切频道 join 负责。
		s.db.Model(&model.VoiceState{}).Where("voice_session_id = ?", sessionID).Update("connected", false)
		s.notifyVoiceConnectedChange(sessionID)
		// BI.3 提前信号（弱）：节点自报其客户端会话 ICE 失败——媒体面可能异常
		// 但信号来自节点自己，整类只计一个独立源（origin 恒定 "self_ice"），
		// 真正独立的客户端侧上报走 voice 的 /voice/ice-failed（origin 按用户去重）。
		if event.GetType() == owlsfuv1.RoomEvent_EVENT_TYPE_PARTICIPANT_ICE_FAILED {
			s.registry.ReportSuspect(nodeID, "self_ice")
		}
	case owlsfuv1.RoomEvent_EVENT_TYPE_SCREEN_TRACK_ACTIVE:
		// 屏幕轨发布成功：ScreenSlot RESERVED → ACTIVE（docs 14 AZ.4）。
		// room_id = logical_room_id = channel_id（docs 08 A.1）。
		channelID, err1 := uuid.Parse(event.GetRoomId())
		userID, err2 := uuid.Parse(event.GetUserId())
		if err1 != nil || err2 != nil {
			logger.Warn("SCREEN_TRACK_ACTIVE 事件字段无效", "room_id", event.GetRoomId(), "user_id", event.GetUserId())
			return
		}
		if s.onScreenTrackActive != nil {
			s.onScreenTrackActive(channelID, userID)
		}
	default:
		logger.Debug("忽略 RoomEvent", "type", event.GetType(), "room_id", event.GetRoomId())
	}
}

// notifyVoiceConnectedChange 读取最新 VoiceState 并回调（用于广播 VOICE_STATE_UPDATE）。
func (s *Service) notifyVoiceConnectedChange(sessionID uuid.UUID) {
	if s.onVoiceConnectedChange == nil {
		return
	}
	var vs model.VoiceState
	if err := s.db.First(&vs, "voice_session_id = ?", sessionID).Error; err != nil {
		return
	}
	s.onVoiceConnectedChange(vs)
}

// handleEdgeStatus 记录级联边状态（Registry 内存跟踪 + 唤醒 WaitEdgeUp 等待者），
// EdgeDown 时经回调交给 voice 编排处理（docs 08 §6.1 / §7.2），并把
// 「健康邻居对边对端的指控」计入 BI.3 提前信号（docs 15 §5 BI.2 ①）。
func (s *Service) handleEdgeStatus(logger *slog.Logger, reporterID uuid.UUID, es *owlsfuv1.EdgeStatus) {
	s.registry.UpdateEdgeStatus(es)
	if es.GetState() == owlsfuv1.EdgeStatus_STATE_EDGE_DOWN {
		logger.Warn("级联边断开", "room_id", es.GetRoomId(), "epoch", es.GetEpoch(),
			"parent", es.GetParentNodeId(), "child", es.GetChildNodeId())
		s.recordEdgeDownSuspicion(reporterID, es)
		if s.onEdgeDown != nil {
			s.onEdgeDown(es)
		}
		return
	}
	logger.Debug("级联边状态", "room_id", es.GetRoomId(), "epoch", es.GetEpoch(),
		"parent", es.GetParentNodeId(), "child", es.GetChildNodeId(),
		"state", es.GetState().String(), "rtt_ms", es.GetRttMs())
}

// recordEdgeDownSuspicion 级联邻居 EdgeDown 指控（BI.2 ①）：
// 上报者为边的一端且自身心跳健康时，构成对边另一端（被指控节点）的独立信号；
// origin 按上报者去重（多个健康邻居各算一个独立源）。
func (s *Service) recordEdgeDownSuspicion(reporterID uuid.UUID, es *owlsfuv1.EdgeStatus) {
	parentID, err1 := uuid.Parse(es.GetParentNodeId())
	childID, err2 := uuid.Parse(es.GetChildNodeId())
	if err1 != nil || err2 != nil {
		return
	}
	var accused uuid.UUID
	switch reporterID {
	case parentID:
		accused = childID
	case childID:
		accused = parentID
	default:
		return // 上报者不是边的一端，不构成指控
	}
	// 上报者自身必须心跳健康（对端节点自身心跳正常时才构成对故障节点的指控）。
	if snapshot, ok := s.registry.Snapshot(reporterID); !ok || !snapshot.Online {
		return
	}
	s.registry.ReportSuspect(accused, "edge_down:"+reporterID.String())
}

// heartbeatMonitorTick 心跳监控扫描周期。比心跳间隔更细（1s）以便 BI.3
// 提前判死在「1 次心跳丢失 + 信号齐备」后尽快触发，压缩静音窗口。
const heartbeatMonitorTick = time.Second

// runHeartbeatMonitor 每 1s 扫描两条判死路径（判死权威仅 Owl-Server，15 BI.4）：
//   - 硬判死：连续 3 个心跳周期（5s×3=15s 上限，15 BI.1）未上报；
//   - 提前判死（BI.3）：≥2 个独立信号源（级联邻居 EdgeDown 指控 / 客户端 ICE
//     失败上报）+ ≥1 次心跳丢失，无需等满 3 次。
//
// 任一路径命中：标记离线、DB 状态回 ENROLLED，并经 onNodeDown 回调发布
// InternalNodeDown 让 voice 迁移引擎接管（docs 09 §3）。
func (s *Service) runHeartbeatMonitor(ctx context.Context) {
	interval := time.Duration(s.cfg.HeartbeatIntervalMS) * time.Millisecond
	threshold := 3 * interval
	ticker := time.NewTicker(heartbeatMonitorTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, nodeID := range s.registry.MarkStale(threshold) {
				s.logger.Warn("节点心跳超时，判死并标记离线", "node_id", nodeID, "threshold", threshold)
				s.declareNodeDown(nodeID)
			}
			for _, nodeID := range s.registry.MarkEarlyDead(interval, s.cfg.EarlyDeath) {
				s.logger.Warn("节点提前判死（BI.3：多信号源 + 心跳丢失）", "node_id", nodeID,
					"min_sources", s.cfg.EarlyDeath.normalize().MinSources)
				s.declareNodeDown(nodeID)
			}
		}
	}
}

// declareNodeDown 判死收尾：DB 状态回 ENROLLED + 触发 onNodeDown 回调。
func (s *Service) declareNodeDown(nodeID uuid.UUID) {
	s.db.Model(&model.SfuNode{}).Where("id = ? AND status = ?", nodeID, model.SfuNodeOnline).
		Update("status", model.SfuNodeEnrolled)
	if s.onNodeDown != nil {
		s.onNodeDown(nodeID)
	}
}

// ---------- 辅助 ----------

// nodeFromPeer 从 mTLS 客户端证书取指纹并查节点；校验状态可用（docs 03 §5.2）。
func (s *Service) nodeFromPeer(ctx context.Context) (*model.SfuNode, error) {
	peerInfo, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "缺少对端信息")
	}
	tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, status.Error(codes.Unauthenticated, "需要 mTLS 客户端证书")
	}
	peerCert := tlsInfo.State.PeerCertificates[0]
	fingerprint := ca.Fingerprint(peerCert.Raw)
	var node model.SfuNode
	if err := s.db.First(&node, "cert_fingerprint = ?", fingerprint).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.PermissionDenied, "证书指纹未登记")
		}
		return nil, status.Error(codes.Internal, "节点查询失败")
	}
	// 应用层再绑 node_id：证书 CN 必须与登记节点一致（docs 03 §5.2 第 3 步）。
	if peerCert.Subject.CommonName != node.ID.String() {
		return nil, status.Error(codes.PermissionDenied, "证书身份与登记节点不一致")
	}
	switch node.Status {
	case model.SfuNodeRevoked, model.SfuNodeDisabled, model.SfuNodePendingEnrollment:
		return nil, status.Error(codes.PermissionDenied, "节点状态不允许连接")
	}
	return &node, nil
}

func (s *Service) mediaTokenKeys() []*owlsfuv1.MediaTokenKey {
	keys := s.tokens.PublicKeys()
	result := make([]*owlsfuv1.MediaTokenKey, 0, len(keys))
	for _, key := range keys {
		result = append(result, &owlsfuv1.MediaTokenKey{Kid: key.Kid, Ed25519PublicKey: key.Key})
	}
	return result
}

func capacityFromProto(capacity *owlsfuv1.NodeCapacity) Capacity {
	if capacity == nil {
		return Capacity{}
	}
	return Capacity{
		MaxUsers:         int(capacity.GetMaxUsers()),
		CurrentUsers:     int(capacity.GetCurrentUsers()),
		RoomCount:        int(capacity.GetRoomCount()),
		CPUPct:           capacity.GetCpuPct(),
		MemPct:           capacity.GetMemPct(),
		BandwidthOutMbps: capacity.GetBandwidthOutMbps(),
		ScreenTracks:     int(capacity.GetScreenTracks()),
	}
}
