package sfunode

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// api REST 层：错误响应格式与 httpapi 一致 {"error":{"code","message"}}；
// 无权限的资源一律 404（docs 06 议题 8 防扫频）。
type api struct {
	svc  *Service
	hub  *Hub
	deps appdeps.Deps
	// controlPort 控制通道端口（enroll 响应中拼接控制面地址用）。
	controlAddress string
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": errorBody{Code: code, Message: message}})
}

func notFound(c *gin.Context) {
	fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在或不可见")
}

// requireSystemAdmin 系统管理员判定；非管理员返回 404 防扫频。
func (a *api) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.deps.CurrentUser(c).SystemAdmin {
			notFound(c)
			c.Abort()
			return
		}
		c.Next()
	}
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		notFound(c)
		return uuid.Nil, false
	}
	return id, true
}

// nodeView 管理端节点视图：DB 记录 + 实时容量覆盖。
type nodeView struct {
	model.SfuNode
	Online bool `json:"online"`
}

func (a *api) view(node model.SfuNode) nodeView {
	v := nodeView{SfuNode: node}
	// WSS 控制通道已停用（见包注释），在线状态以 DB 持久化状态为准
	//（gRPC 控制面注册/心跳/判死会实时维护 status 列）。
	v.Online = node.Status == model.SfuNodeOnline
	if live, ok := a.hub.Live(node.ID); ok {
		v.Online = true
		v.CurrentUsers = live.CurrentUsers
		v.CPUPct = live.CPUPct
		v.MemPct = live.MemPct
		v.BandwidthOutMbps = live.BandwidthOutMbps
		v.ScreenTracks = live.ScreenTracks
		if live.NodeRTTMs != nil {
			v.NodeRTTMs = live.NodeRTTMs
		}
		last := live.LastSeen.UTC()
		v.LastSeenAt = &last
	}
	return v
}

// ---- 系统管理员：节点生命周期 ----

type createNodeRequest struct {
	DisplayName string            `json:"display_name" binding:"required,min=1,max=100"`
	Labels      map[string]string `json:"labels"`
	TokenTTLS   int               `json:"token_ttl_s"` // 可选，秒；默认 30 分钟
}

type createNodeResponse struct {
	Node            nodeView  `json:"node"`
	EnrollmentToken string    `json:"enrollment_token"` // 明文仅此一次返回
	TokenExpiresAt  time.Time `json:"token_expires_at"`
}

// createNode POST /admin/sfu/nodes：创建占位节点并签发一次性 enrollment token。
func (a *api) createNode(c *gin.Context) {
	var req createNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	ttl := DefaultEnrollmentTokenTTL
	if req.TokenTTLS > 0 {
		ttl = time.Duration(req.TokenTTLS) * time.Second
	}
	node, token, err := a.svc.CreateNode(a.deps.CurrentUser(c), req.DisplayName, req.Labels, ttl)
	if err != nil {
		fail(c, http.StatusInternalServerError, "NODE_CREATE_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusCreated, createNodeResponse{
		Node:            a.view(node),
		EnrollmentToken: token,
		TokenExpiresAt:  *node.EnrollmentTokenExpiresAt,
	})
}

// listNodes GET /admin/sfu/nodes：全部节点（含实时容量）。
func (a *api) listNodes(c *gin.Context) {
	var nodes []model.SfuNode
	if err := a.svc.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询节点列表失败")
		return
	}
	views := make([]nodeView, 0, len(nodes))
	for _, node := range nodes {
		views = append(views, a.view(node))
	}
	c.JSON(http.StatusOK, gin.H{"nodes": views})
}

// topologyAggEdge 按 parent-child 合并多房间边（拓扑图画线用）。
type topologyAggEdge struct {
	ParentNodeID string  `json:"parent_node_id"`
	ChildNodeID  string  `json:"child_node_id"`
	Up           bool    `json:"up"`
	RttMs        float64 `json:"rtt_ms"`
	BytesTx      uint64  `json:"bytes_tx"`
	BytesRx      uint64  `json:"bytes_rx"`
	PathType     string  `json:"path_type"`
	RoomCount    int     `json:"room_count"`
	LocalIP      string  `json:"local_ip,omitempty"`
	RemoteIP     string  `json:"remote_ip,omitempty"`
}

// topologyServerInfo 拓扑图中的 Owl-Server 控制面节点（编排 / 租约 / 边下发）。
type topologyServerInfo struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"display_name"`
	Role               string `json:"role"` // control_plane
	HTTPAddress        string `json:"http_address"`
	SFUControlEndpoint string `json:"sfu_control_endpoint"`
	SFUControlListen   string `json:"sfu_control_listen"`
	PublicBaseURL      string `json:"public_base_url,omitempty"`
	Online             bool   `json:"online"`
	ConnectedSFUCount  int    `json:"connected_sfu_count"`
}

// topologyControlLink Server ↔ SFU 控制通道（gRPC mTLS，无媒体）。
type topologyControlLink struct {
	ServerID string `json:"server_id"`
	NodeID   string `json:"node_id"`
	Up       bool   `json:"up"`
	Kind     string `json:"kind"` // grpc_control
}

// listTopology GET /admin/sfu/topology：Server 控制面 + SFU 节点 + 级联边。
// 前端对累计字节差分得到实时 bps。边状态来自 gRPC 控制面 Registry（SFU EdgeStatus 上报）。
func (a *api) listTopology(c *gin.Context) {
	var nodes []model.SfuNode
	if err := a.svc.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询节点列表失败")
		return
	}
	views := make([]nodeView, 0, len(nodes))
	controlLinks := make([]topologyControlLink, 0, len(nodes))
	connected := 0
	for _, node := range nodes {
		v := a.view(node)
		// 优先以 gRPC Registry 实时在线为准（控制流 attached + 心跳健康）
		if reg := a.deps.SFURegistry; reg != nil {
			if snap, ok := reg.Snapshot(node.ID); ok {
				v.Online = snap.Online
				v.CurrentUsers = snap.Capacity.CurrentUsers
				v.CPUPct = snap.Capacity.CPUPct
				v.MemPct = snap.Capacity.MemPct
				v.BandwidthOutMbps = snap.Capacity.BandwidthOutMbps
				v.ScreenTracks = snap.Capacity.ScreenTracks
				last := snap.LastSeen.UTC()
				v.LastSeenAt = &last
			}
		}
		views = append(views, v)
		controlLinks = append(controlLinks, topologyControlLink{
			ServerID: "owl-server",
			NodeID:   node.ID.String(),
			Up:       v.Online,
			Kind:     "grpc_control",
		})
		if v.Online {
			connected++
		}
	}

	var edges any = []struct{}{}
	aggList := make([]topologyAggEdge, 0)
	if reg := a.deps.SFURegistry; reg != nil {
		raw := reg.ListEdges()
		edges = raw
		type aggKey struct{ parent, child string }
		agg := map[aggKey]*topologyAggEdge{}
		for _, e := range raw {
			k := aggKey{parent: e.ParentNodeID, child: e.ChildNodeID}
			cur, ok := agg[k]
			if !ok {
				cur = &topologyAggEdge{
					ParentNodeID: e.ParentNodeID,
					ChildNodeID:  e.ChildNodeID,
					PathType:     e.PathType,
					LocalIP:      e.LocalIP,
					RemoteIP:     e.RemoteIP,
				}
				agg[k] = cur
			}
			cur.RoomCount++
			cur.BytesTx += e.BytesTx
			cur.BytesRx += e.BytesRx
			if e.Up {
				cur.Up = true
			}
			if e.RttMs > cur.RttMs {
				cur.RttMs = e.RttMs
			}
			cur.PathType = worseTopologyPath(cur.PathType, e.PathType)
			if cur.LocalIP == "" && e.LocalIP != "" {
				cur.LocalIP = e.LocalIP
			}
			if cur.RemoteIP == "" && e.RemoteIP != "" {
				cur.RemoteIP = e.RemoteIP
			}
		}
		for _, v := range agg {
			aggList = append(aggList, *v)
		}
	}

	serverInfo := topologyServerInfo{
		ID:                 "owl-server",
		DisplayName:        "Owl-Server",
		Role:               "control_plane",
		HTTPAddress:        a.deps.Cfg.Address,
		SFUControlEndpoint: a.deps.Cfg.SFUControlPublicEndpoint,
		SFUControlListen:   a.deps.Cfg.SFUControlAddress,
		PublicBaseURL:      a.deps.Cfg.PublicBaseURL,
		Online:             true,
		ConnectedSFUCount:  connected,
	}

	c.JSON(http.StatusOK, gin.H{
		"generated_at":     time.Now().UTC(),
		"server":           serverInfo,
		"nodes":            views,
		"control_links":    controlLinks,
		"edges":            edges,
		"aggregated_edges": aggList,
	})
}

// worseTopologyPath 聚合多房间时取「更贵」路径：wan > lan > unknown。
func worseTopologyPath(a, b string) string {
	rank := func(p string) int {
		switch p {
		case "wan":
			return 3
		case "lan":
			return 2
		default:
			return 1
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	if a == "" {
		return b
	}
	return a
}

// getNode GET /admin/sfu/nodes/:nodeID
func (a *api) getNode(c *gin.Context) {
	nodeID, ok := parseUUIDParam(c, "nodeID")
	if !ok {
		return
	}
	var node model.SfuNode
	if err := a.svc.db.First(&node, "id = ?", nodeID).Error; err != nil {
		notFound(c)
		return
	}
	c.JSON(http.StatusOK, a.view(node))
}

type updateNodeRequest struct {
	DisplayName          *string            `json:"display_name"`
	Labels               *map[string]string `json:"labels"`
	EnabledForScheduling *bool              `json:"enabled_for_scheduling"`
	PlatformDefault      *bool              `json:"platform_default"`
}

// updateNode PATCH /admin/sfu/nodes/:nodeID：调度开关、名称、标签、平台默认池归属。
func (a *api) updateNode(c *gin.Context) {
	nodeID, ok := parseUUIDParam(c, "nodeID")
	if !ok {
		return
	}
	var req updateNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	updates := map[string]any{}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Labels != nil {
		updates["labels"] = model.SfuLabelMap(*req.Labels)
	}
	if req.EnabledForScheduling != nil {
		updates["enabled_for_scheduling"] = *req.EnabledForScheduling
	}
	if req.PlatformDefault != nil {
		updates["platform_default"] = *req.PlatformDefault
	}
	node, err := a.svc.UpdateNode(a.deps.CurrentUser(c), nodeID, updates)
	if err != nil {
		notFound(c)
		return
	}
	c.JSON(http.StatusOK, a.view(node))
}

// nodeAction 吊销 / 排空 / 结束排空 / 禁用 / 启用的统一入口。
func (a *api) nodeAction(action func(model.User, uuid.UUID) (model.SfuNode, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		nodeID, ok := parseUUIDParam(c, "nodeID")
		if !ok {
			return
		}
		node, err := action(a.deps.CurrentUser(c), nodeID)
		if err != nil {
			if strings.Contains(err.Error(), "不存在") {
				notFound(c)
				return
			}
			fail(c, http.StatusConflict, "INVALID_STATE_TRANSITION", err.Error())
			return
		}
		c.JSON(http.StatusOK, a.view(node))
	}
}

// ---- 节点侧：Enrollment（无需登录）----

type enrollRequest struct {
	NodeID string `json:"node_id" binding:"required"`
	Token  string `json:"token" binding:"required"`
	CSRPEM string `json:"csr_pem" binding:"required"`
}

type enrollResponse struct {
	NodeID      uuid.UUID `json:"node_id"`
	CertPEM     string    `json:"cert_pem"`
	CABundlePEM string    `json:"ca_bundle_pem"`
	NotAfter    time.Time `json:"not_after"`
	// ControlURL mTLS 控制通道 WebSocket 地址。
	ControlURL string `json:"control_url"`
	// RenewURL mTLS 证书轮换地址。
	RenewURL           string `json:"renew_url"`
	HeartbeatIntervalS int    `json:"heartbeat_interval_s"`
}

// enroll POST /sfu/enroll：一次性 token 换节点证书（docs 03 §4.1 步骤 3）。
func (a *api) enroll(c *gin.Context) {
	var req enrollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	nodeID, err := uuid.Parse(req.NodeID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "node_id 不是合法 UUID")
		return
	}
	result, err := a.svc.Enroll(nodeID, req.Token, []byte(req.CSRPEM))
	if err != nil {
		// 统一模糊拒绝，避免向探测者泄露 token/节点状态细节。
		fail(c, http.StatusForbidden, "ENROLL_REJECTED", "enrollment 校验失败")
		return
	}
	host := controlHost(a.controlAddress, c.Request.Host)
	c.JSON(http.StatusOK, enrollResponse{
		NodeID:             result.Node.ID,
		CertPEM:            string(result.CertPEM),
		CABundlePEM:        string(result.CABundlePEM),
		NotAfter:           result.NotAfter,
		ControlURL:         "wss://" + host + "/control",
		RenewURL:           "https://" + host + "/renew",
		HeartbeatIntervalS: int(heartbeatInterval.Seconds()),
	})
}

// controlHost 推导控制通道对外地址：监听地址未含主机名时，回落到本次请求的 Host。
func controlHost(controlAddress, requestHost string) string {
	listenHost, listenPort, err := net.SplitHostPort(controlAddress)
	if err != nil {
		return controlAddress
	}
	if listenHost != "" && listenHost != "0.0.0.0" && listenHost != "::" {
		return net.JoinHostPort(listenHost, listenPort)
	}
	requestHostname := requestHost
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		requestHostname = h
	}
	return net.JoinHostPort(requestHostname, listenPort)
}

// ---- 服级节点池 ----

type poolNodeView struct {
	ID          uuid.UUID         `json:"id"`
	DisplayName string            `json:"display_name"`
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Online      bool              `json:"online"`
}

type poolResponse struct {
	GuildID           uuid.UUID      `json:"guild_id"`
	FallbackToDefault bool           `json:"fallback_to_default"`
	Candidates        []poolNodeView `json:"candidates"`
	Selected          []poolNodeView `json:"selected"`
}

func (a *api) poolViews(nodes []model.SfuNode) []poolNodeView {
	views := make([]poolNodeView, 0, len(nodes))
	for _, node := range nodes {
		views = append(views, poolNodeView{
			ID: node.ID, DisplayName: node.DisplayName, Status: node.Status,
			Labels: node.Labels,
			// WSS 控制通道已停用，在线状态以 DB 状态为准（gRPC 控制面维护）。
			Online: node.Status == model.SfuNodeOnline || a.hub.IsConnected(node.ID),
		})
	}
	return views
}

func (a *api) poolResponse(cfg PoolConfig) poolResponse {
	return poolResponse{
		GuildID:           cfg.GuildID,
		FallbackToDefault: cfg.FallbackToDefault,
		Candidates:        a.poolViews(cfg.Candidates),
		Selected:          a.poolViews(cfg.Selected),
	}
}

// adminGetPool GET /admin/guilds/:guildID/node-pool
func (a *api) adminGetPool(c *gin.Context) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	cfg, err := a.svc.LoadPool(guildID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	c.JSON(http.StatusOK, a.poolResponse(cfg))
}

type adminPutPoolRequest struct {
	CandidateNodeIDs  []string  `json:"candidate_node_ids" binding:"required"`
	SelectedNodeIDs   *[]string `json:"selected_node_ids"` // 可选：系统管直接覆盖勾选集
	FallbackToDefault *bool     `json:"fallback_to_default"`
}

// adminPutPool PUT /admin/guilds/:guildID/node-pool：系统管理员授权候选集/覆盖池配置。
func (a *api) adminPutPool(c *gin.Context) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	var req adminPutPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	candidateIDs, err := parseUUIDs(req.CandidateNodeIDs)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	var selectedIDs *[]uuid.UUID
	if req.SelectedNodeIDs != nil {
		ids, err := parseUUIDs(*req.SelectedNodeIDs)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		selectedIDs = &ids
	}
	cfg, err := a.svc.SetAdminPool(a.deps.CurrentUser(c), guildID, candidateIDs, selectedIDs, req.FallbackToDefault)
	if err != nil {
		fail(c, http.StatusBadRequest, "POOL_UPDATE_FAILED", err.Error())
		return
	}
	a.publishPoolUpdate(guildID)
	c.JSON(http.StatusOK, a.poolResponse(cfg))
}

// publishPoolUpdate 节点池变更轻量广播（实时同步专项）：载荷仅 guild_id+event_at，
// 客户端据此重拉 GET /guilds/{gid}/voice/nodes 刷新 RTT 探测候选与管理界面。
func (a *api) publishPoolUpdate(guildID uuid.UUID) {
	if a.deps.Bus == nil {
		return
	}
	a.deps.Bus.Publish(eventbus.Event{
		Type:    eventbus.EventVoiceNodePoolUpdate,
		GuildID: &guildID,
		Payload: map[string]any{"guild_id": guildID, "event_at": time.Now().UTC()},
	})
}

// requireGuildAdmin 服务器管理员（ManageGuild）判定；无权限一律 404。
func (a *api) requireGuildAdmin(c *gin.Context) (uuid.UUID, bool) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return uuid.Nil, false
	}
	ctx, err := perms.LoadGuild(a.svc.db, a.deps.CurrentUser(c), guildID)
	if err != nil {
		notFound(c)
		return uuid.Nil, false
	}
	if !ctx.Has(rbac.ManageGuild) {
		notFound(c)
		return uuid.Nil, false
	}
	return guildID, true
}

// guildGetPool GET /guilds/:guildID/node-pool
func (a *api) guildGetPool(c *gin.Context) {
	guildID, ok := a.requireGuildAdmin(c)
	if !ok {
		return
	}
	cfg, err := a.svc.LoadPool(guildID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	c.JSON(http.StatusOK, a.poolResponse(cfg))
}

type guildPutPoolRequest struct {
	NodeIDs           []string `json:"node_ids" binding:"required"`
	FallbackToDefault *bool    `json:"fallback_to_default"`
}

// guildPutPool PUT /guilds/:guildID/node-pool：服务器管理员只能从授权候选中勾选。
func (a *api) guildPutPool(c *gin.Context) {
	guildID, ok := a.requireGuildAdmin(c)
	if !ok {
		return
	}
	var req guildPutPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	nodeIDs, err := parseUUIDs(req.NodeIDs)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	cfg, err := a.svc.SetGuildPool(a.deps.CurrentUser(c), guildID, nodeIDs, req.FallbackToDefault)
	if err != nil {
		fail(c, http.StatusBadRequest, "POOL_UPDATE_FAILED", err.Error())
		return
	}
	a.publishPoolUpdate(guildID)
	c.JSON(http.StatusOK, a.poolResponse(cfg))
}

func parseUUIDs(raw []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("节点 ID %q 不是合法 UUID", s)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
