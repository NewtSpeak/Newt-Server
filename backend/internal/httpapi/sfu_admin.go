package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	owlsfuv1 "github.com/newtspeak/newt-server/backend/gen/owlsfu/v1"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/mediatoken"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfucontrol"
	"github.com/newtspeak/newt-server/backend/internal/sfudeploy"
	"github.com/newtspeak/newt-server/backend/internal/voice"
)

// SFUOptions SFU 配套子系统依赖（由 main 装配注入）。
type SFUOptions struct {
	Registry    *sfucontrol.Registry
	MediaTokens *mediatoken.Manager
	// Cfg 可选：用于发布目录 / PublicBaseURL 推导下载链接。
	Cfg *config.Config
	// Deploy 可选：SFU 节点一键部署编排器（internal/sfudeploy）；不传时部署路由 503。
	Deploy *sfudeploy.Manager
}

// AttachSFU 注入 SFU 节点注册表与 Media Token 签发器；未注入时相关路由返回 503。
func (a *API) AttachSFU(opts SFUOptions) {
	a.sfu = &opts
	if opts.Deploy != nil {
		a.AttachSfuDeploy(opts.Deploy)
	}
}

// requireSFU SFU 子系统未装配时直接 503（理论上仅测试环境出现）。
func (a *API) requireSFU(c *gin.Context) bool {
	if a.sfu == nil || a.sfu.Registry == nil || a.sfu.MediaTokens == nil {
		fail(c, http.StatusServiceUnavailable, "SFU_UNAVAILABLE", "SFU 子系统未启用")
		return false
	}
	return true
}

func (a *API) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !currentUser(c).SystemAdmin {
			fail(c, http.StatusForbidden, "SYSTEM_ADMIN_REQUIRED", "需要系统管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

type createSfuNodeRequest struct {
	DisplayName string            `json:"display_name" binding:"required,min=1,max=100"`
	Labels      map[string]string `json:"labels"` // 可选：region / network 等，供调度与后台展示
}

type createSfuNodeResponse struct {
	NodeID          uuid.UUID `json:"node_id"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// createSfuNode 创建节点占位并签发一次性 enrollment token（明文仅此一次返回，库中只存哈希）。
// 核心逻辑在 sfucontrol.CreateNodeWithEnrollment（与自动部署编排共用）。
func (a *API) createSfuNode(c *gin.Context) {
	var input createSfuNodeRequest
	if !bind(c, &input) {
		return
	}
	node, token, expiresAt, err := sfucontrol.CreateNodeWithEnrollment(a.db, input.DisplayName, input.Labels)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	c.JSON(http.StatusCreated, createSfuNodeResponse{NodeID: node.ID, EnrollmentToken: token, ExpiresAt: expiresAt})
}

type sfuNodeSummary struct {
	ID                   uuid.UUID           `json:"id"`
	DisplayName          string              `json:"display_name"`
	Status               string              `json:"status"`
	Labels               map[string]string   `json:"labels"`
	NodeVersion          string              `json:"node_version"`
	EnabledForScheduling bool                `json:"enabled_for_scheduling"`
	PlatformDefault      bool                `json:"platform_default"`
	Online               bool                `json:"online"`
	Capacity             sfucontrol.Capacity `json:"capacity"`
	AdvertiseWssURL      string              `json:"advertise_wss_url"`
	MediaUDPPort         int                 `json:"media_udp_port"`
	MediaIPs             []string            `json:"media_ips"`
	CascadeEndpoint      string              `json:"cascade_endpoint"`
	CertNotAfter         *time.Time          `json:"cert_not_after"`
	LastSeenAt           *time.Time          `json:"last_seen_at"`
	CreatedAt            time.Time           `json:"created_at"`
}

func (a *API) sfuNodeSummary(node model.SfuNode) sfuNodeSummary {
	labels := map[string]string{}
	for k, v := range node.Labels {
		labels[k] = v
	}
	summary := sfuNodeSummary{
		ID:                   node.ID,
		DisplayName:          node.DisplayName,
		Status:               node.Status,
		Labels:               labels,
		NodeVersion:          node.NodeVersion,
		EnabledForScheduling: node.EnabledForScheduling,
		PlatformDefault:      node.PlatformDefault,
		AdvertiseWssURL:      node.AdvertiseWssURL,
		MediaUDPPort:         node.MediaUDPPort,
		MediaIPs:             node.MediaIPs,
		CascadeEndpoint:      node.CascadeEndpoint,
		CertNotAfter:         node.CertNotAfter,
		LastSeenAt:           node.LastSeenAt,
		CreatedAt:            node.CreatedAt,
	}
	if snapshot, ok := a.sfu.Registry.Snapshot(node.ID); ok {
		summary.Online = snapshot.Online
		summary.Capacity = snapshot.Capacity
		// 控制通道仍连着时，对外状态以在线为准（避免仅 DB 短暂滞后）。
		if snapshot.Online && (summary.Status == model.SfuNodeEnrolled || summary.Status == model.SfuNodeOnline) {
			summary.Status = model.SfuNodeOnline
		}
	}
	return summary
}

func (a *API) listSfuNodes(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var nodes []model.SfuNode
	if err := a.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 SFU 节点失败")
		return
	}
	result := make([]sfuNodeSummary, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, a.sfuNodeSummary(node))
	}
	c.JSON(http.StatusOK, result)
}

// sfuTopologyResponse 管理台拓扑面板：Server 控制面 + SFU 节点 + 级联边。
// 前端对累计字节差分得到实时 bps。
type sfuTopologyResponse struct {
	GeneratedAt  time.Time                 `json:"generated_at"`
	Server       sfuTopologyServerInfo     `json:"server"`
	Nodes        []sfuNodeSummary          `json:"nodes"`
	ControlLinks []sfuTopologyControlLink  `json:"control_links"`
	Edges        []sfucontrol.EdgeSnapshot `json:"edges"`
	// AggregatedEdges 按 parent-child 合并多房间边的流量（拓扑图连线用）。
	AggregatedEdges []sfuTopologyAggEdge `json:"aggregated_edges"`
}

type sfuTopologyServerInfo struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"display_name"`
	Role               string `json:"role"`
	HTTPAddress        string `json:"http_address"`
	SFUControlEndpoint string `json:"sfu_control_endpoint"`
	SFUControlListen   string `json:"sfu_control_listen"`
	PublicBaseURL      string `json:"public_base_url,omitempty"`
	Online             bool   `json:"online"`
	ConnectedSFUCount  int    `json:"connected_sfu_count"`
}

type sfuTopologyControlLink struct {
	ServerID string `json:"server_id"`
	NodeID   string `json:"node_id"`
	Up       bool   `json:"up"`
	Kind     string `json:"kind"`
}

type sfuTopologyAggEdge struct {
	ParentNodeID string  `json:"parent_node_id"`
	ChildNodeID  string  `json:"child_node_id"`
	Up           bool    `json:"up"`
	RttMs        float64 `json:"rtt_ms"`
	BytesTx      uint64  `json:"bytes_tx"` // parent → child 累计
	BytesRx      uint64  `json:"bytes_rx"` // child → parent 累计
	PathType     string  `json:"path_type"` // lan | wan | unknown
	RoomCount    int     `json:"room_count"`
	LocalIP      string  `json:"local_ip,omitempty"`
	RemoteIP     string  `json:"remote_ip,omitempty"`
}

// listSfuTopology GET /admin/sfu/topology：实时级联拓扑 + 节点活跃用户 + Server 控制面。
func (a *API) listSfuTopology(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var nodes []model.SfuNode
	if err := a.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 SFU 节点失败")
		return
	}
	summaries := make([]sfuNodeSummary, 0, len(nodes))
	controlLinks := make([]sfuTopologyControlLink, 0, len(nodes))
	connected := 0
	for _, node := range nodes {
		s := a.sfuNodeSummary(node)
		summaries = append(summaries, s)
		controlLinks = append(controlLinks, sfuTopologyControlLink{
			ServerID: "owl-server",
			NodeID:   node.ID.String(),
			Up:       s.Online,
			Kind:     "grpc_control",
		})
		if s.Online {
			connected++
		}
	}
	edges := a.sfu.Registry.ListEdges()
	// 按 parent|child 聚合多房间边
	type aggKey struct{ parent, child string }
	agg := map[aggKey]*sfuTopologyAggEdge{}
	for _, e := range edges {
		k := aggKey{parent: e.ParentNodeID, child: e.ChildNodeID}
		cur, ok := agg[k]
		if !ok {
			cur = &sfuTopologyAggEdge{
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
		// 路径优先级：lan > wan > unknown（只要有一路内网就标 lan 偏乐观；
		// 更准确的是 per-room，聚合层取「最差」：有 wan 则 wan）
		cur.PathType = worsePath(cur.PathType, e.PathType)
		if cur.LocalIP == "" && e.LocalIP != "" {
			cur.LocalIP = e.LocalIP
		}
		if cur.RemoteIP == "" && e.RemoteIP != "" {
			cur.RemoteIP = e.RemoteIP
		}
	}
	aggList := make([]sfuTopologyAggEdge, 0, len(agg))
	for _, v := range agg {
		aggList = append(aggList, *v)
	}
	httpAddr, controlEP, controlListen, publicURL := "", "", "", ""
	if a.sfu != nil && a.sfu.Cfg != nil {
		httpAddr = a.sfu.Cfg.Address
		controlEP = a.sfu.Cfg.SFUControlPublicEndpoint
		controlListen = a.sfu.Cfg.SFUControlAddress
		publicURL = a.sfu.Cfg.PublicBaseURL
	}
	c.JSON(http.StatusOK, sfuTopologyResponse{
		GeneratedAt: time.Now().UTC(),
		Server: sfuTopologyServerInfo{
			ID:                 "owl-server",
			DisplayName:        "Newt-Server",
			Role:               "control_plane",
			HTTPAddress:        httpAddr,
			SFUControlEndpoint: controlEP,
			SFUControlListen:   controlListen,
			PublicBaseURL:      publicURL,
			Online:             true,
			ConnectedSFUCount:  connected,
		},
		Nodes:           summaries,
		ControlLinks:    controlLinks,
		Edges:           edges,
		AggregatedEdges: aggList,
	})
}

// worsePath 聚合多房间时取「更贵」路径：wan > lan > unknown。
func worsePath(a, b string) string {
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

type updateSfuNodeRequest struct {
	DisplayName          *string            `json:"display_name"`
	Labels               *map[string]string `json:"labels"` // 全量替换；传 {} 清空
	EnabledForScheduling *bool              `json:"enabled_for_scheduling"`
	PlatformDefault      *bool              `json:"platform_default"`
	Status               *string            `json:"status"`
}

// updateSfuNode PATCH 节点：调度开关、标签、名称、平台默认池与状态迁移。
// 调度开关（enabled_for_scheduling）与生命周期状态（ONLINE/ENROLLED/DISABLED…）正交：
// 仅关调度不会把 ONLINE 打成 ENROLLED。DISABLED/REVOKED 会强制关调度并断开控制流。
func (a *API) updateSfuNode(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var input updateSfuNodeRequest
	if !bind(c, &input) {
		return
	}
	if input.EnabledForScheduling == nil && input.Status == nil &&
		input.DisplayName == nil && input.Labels == nil && input.PlatformDefault == nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "至少提供一项可更新字段")
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	updates := map[string]any{}
	if input.DisplayName != nil {
		name := *input.DisplayName
		if name == "" || len(name) > 100 {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "display_name 长度须为 1–100")
			return
		}
		updates["display_name"] = name
	}
	if input.Labels != nil {
		labels := model.SfuLabelMap{}
		for k, v := range *input.Labels {
			if k == "" {
				continue
			}
			labels[k] = v
		}
		updates["labels"] = labels
	}
	if input.EnabledForScheduling != nil {
		updates["enabled_for_scheduling"] = *input.EnabledForScheduling
	}
	if input.PlatformDefault != nil {
		updates["platform_default"] = *input.PlatformDefault
	}
	disconnect := false
	if input.Status != nil {
		if node.Status == model.SfuNodeRevoked {
			fail(c, http.StatusConflict, "NODE_REVOKED", "REVOKED 为终态，不允许再变更状态")
			return
		}
		switch *input.Status {
		case model.SfuNodeEnrolled:
			// 解除禁用：仅证书已签发的 DISABLED/DRAINING 节点可回到 ENROLLED，等待重连上线。
			if node.CertFingerprint == "" || (node.Status != model.SfuNodeDisabled && node.Status != model.SfuNodeDraining) {
				fail(c, http.StatusConflict, "INVALID_STATUS_TRANSITION", "当前状态不允许迁移到 ENROLLED")
				return
			}
			updates["status"] = model.SfuNodeEnrolled
		case model.SfuNodeDisabled:
			updates["status"] = model.SfuNodeDisabled
			updates["enabled_for_scheduling"] = false
			disconnect = true
		case model.SfuNodeRevoked:
			updates["status"] = model.SfuNodeRevoked
			updates["enabled_for_scheduling"] = false
			updates["enrollment_token_hash"] = ""
			updates["enrollment_token_expires_at"] = nil
			disconnect = true
		default:
			fail(c, http.StatusBadRequest, "INVALID_STATUS", "status 仅支持 ENROLLED（解除禁用）、DISABLED、REVOKED")
			return
		}
	}
	if err := a.db.Model(&node).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新 SFU 节点失败")
		return
	}
	if disconnect {
		a.sfu.Registry.Disconnect(node.ID)
	}
	if err := a.db.First(&node, "id = ?", node.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 SFU 节点失败")
		return
	}
	c.JSON(http.StatusOK, a.sfuNodeSummary(node))
}

// deleteSfuNode 仅允许删除尚未完成 enrollment 的占位节点（docs 03 §8：PENDING 超时/取消）。
func (a *API) deleteSfuNode(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	if node.Status != model.SfuNodePendingEnrollment {
		fail(c, http.StatusConflict, "NODE_NOT_PENDING", "仅 PENDING_ENROLLMENT 状态的节点可删除，其余请使用禁用或吊销")
		return
	}
	if err := a.db.Delete(&node).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除 SFU 节点失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// revokeSfuNode 吊销节点：状态置 REVOKED（终态）并立即断开其控制流。
func (a *API) revokeSfuNode(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	err := a.db.Model(&node).Updates(map[string]any{
		"status":                      model.SfuNodeRevoked,
		"enabled_for_scheduling":      false,
		"enrollment_token_hash":       "",
		"enrollment_token_expires_at": nil,
	}).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销 SFU 节点失败")
		return
	}
	a.sfu.Registry.Disconnect(node.ID)
	node.Status = model.SfuNodeRevoked
	c.JSON(http.StatusOK, a.sfuNodeSummary(node))
}

// ---- 节点程序版本 / 远程升级 ----

var sfuVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type updateSfuBinaryRequest struct {
	// TargetVersion 目标版本号；若留空且使用本地发布目录，必须显式填写。
	TargetVersion string `json:"target_version"`
	// DownloadURL 二进制直链；为空时从 SFU_RELEASE_DIR 按
	// owl-sfu-<version>-linux-amd64 查找并生成 /sfu-releases/... 链接。
	DownloadURL string `json:"download_url"`
	// SHA256Hex 可选；本地发布文件会自动计算。
	SHA256Hex string `json:"sha256_hex"`
	// Force 即使节点当前版本相同也强制重装。
	Force bool `json:"force"`
	// GOOS/GOARCH 仅用于从本地发布目录解析文件名；默认 linux/amd64。
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	// DrainFirst 升级前先排空：把本节点用户均匀迁到附近节点，实现滚动更新。
	// 默认 true；显式传 false 可跳过（紧急热修 / 空节点）。
	DrainFirst *bool `json:"drain_first"`
	// DrainTimeoutSec 等待迁空秒数，默认 90；超时仍会继续升级（残留会话由硬迁兜底）。
	DrainTimeoutSec int `json:"drain_timeout_sec"`
}

type updateSfuBinaryResponse struct {
	NodeID        uuid.UUID `json:"node_id"`
	TargetVersion string    `json:"target_version"`
	DownloadURL   string    `json:"download_url"`
	SHA256Hex     string    `json:"sha256_hex,omitempty"`
	CommandID     string    `json:"command_id,omitempty"`
	OK            bool      `json:"ok"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	// Note 说明节点将自重启；版本以重连后 Register 上报为准。
	Note string `json:"note,omitempty"`
	// Drain 滚动更新排空摘要（drain_first=true 时填充）。
	Drain *voice.RollingUpgradeResult `json:"drain,omitempty"`
}

// updateSfuBinary POST /admin/sfu/nodes/:nodeID/update-binary
// 向在线节点下发 UpdateBinary 指令：下载 → 校验 → 替换 → 自重启。
func (a *API) updateSfuBinary(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	var node model.SfuNode
	if err := a.db.First(&node, "id = ?", c.Param("nodeID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "SFU 节点不存在")
		return
	}
	var input updateSfuBinaryRequest
	if !bind(c, &input) {
		return
	}
	target := strings.TrimSpace(input.TargetVersion)
	downloadURL := strings.TrimSpace(input.DownloadURL)
	sha := strings.TrimSpace(input.SHA256Hex)
	goos := strings.TrimSpace(input.GOOS)
	goarch := strings.TrimSpace(input.GOARCH)

	if downloadURL == "" {
		if target == "" || !sfuVersionPattern.MatchString(target) {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "target_version 无效；或直接提供 download_url")
			return
		}
		// 未指定平台时：按本机平台（内嵌 SFU）→ linux/amd64（常见公网节点）→ 目录内唯一匹配自动选择。
		resolved, sum, pickedGOOS, pickedGOARCH, err := a.resolveLocalRelease(c, target, goos, goarch)
		if err != nil {
			fail(c, http.StatusBadRequest, "RELEASE_NOT_FOUND", err.Error())
			return
		}
		downloadURL = resolved
		goos, goarch = pickedGOOS, pickedGOARCH
		if sha == "" {
			sha = sum
		}
	} else if target == "" {
		// 仅 URL 时允许省略版本，用 "external" 标记；节点仍会用 URL 安装。
		target = "external"
	}

	// 默认开启滚动更新：先排空再升级。显式 drain_first=false 可跳过。
	drainFirst := true
	if input.DrainFirst != nil {
		drainFirst = *input.DrainFirst
	}

	// 升级下载可能较久：超时放宽到 13 分钟（节点侧 12 分钟）+ 排空等待。
	totalTimeout := 13 * time.Minute
	if drainFirst {
		totalTimeout += 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), totalTimeout)
	defer cancel()

	resp := updateSfuBinaryResponse{
		NodeID:        node.ID,
		TargetVersion: target,
		DownloadURL:   downloadURL,
		SHA256Hex:     sha,
	}

	if drainFirst {
		drainTimeout := time.Duration(input.DrainTimeoutSec) * time.Second
		if drainTimeout <= 0 {
			drainTimeout = 90 * time.Second
		}
		// 排空子上下文：与总超时取更短。
		drainCtx, drainCancel := context.WithTimeout(ctx, drainTimeout+5*time.Second)
		drainResult, err := voice.DrainForRollingUpgrade(drainCtx, node.ID, voice.RollingUpgradeOptions{
			Timeout: drainTimeout,
		})
		drainCancel()
		resp.Drain = &drainResult
		if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			fail(c, http.StatusInternalServerError, "DRAIN_FAILED", "升级前排空失败: "+err.Error())
			return
		}
	}

	cmd := &owlsfuv1.Command{
		Payload: &owlsfuv1.Command_UpdateBinary{UpdateBinary: &owlsfuv1.UpdateBinary{
			TargetVersion: target,
			DownloadUrl:   downloadURL,
			Sha256Hex:     sha,
			Force:         input.Force,
		}},
	}
	ack, err := a.sfu.Registry.SendCommand(ctx, node.ID, cmd)
	if err != nil {
		if err == sfucontrol.ErrNodeOffline {
			fail(c, http.StatusConflict, "NODE_OFFLINE", "节点不在线，无法升级")
			return
		}
		if err == sfucontrol.ErrCommandTimeout {
			fail(c, http.StatusGatewayTimeout, "COMMAND_TIMEOUT", "等待节点升级确认超时（可能仍在下载，请稍后刷新版本）")
			return
		}
		fail(c, http.StatusBadGateway, "COMMAND_FAILED", err.Error())
		return
	}
	resp.CommandID = ack.GetCommandId()
	resp.OK = ack.GetOk()
	resp.ErrorCode = ack.GetErrorCode()
	resp.ErrorMessage = ack.GetErrorMessage()
	if ack.GetOk() {
		note := "节点已接受升级；进程将自重启，版本以重连后上报为准"
		if resp.Drain != nil {
			if resp.Drain.Drained {
				note = fmt.Sprintf("已将 %d 个会话迁至附近节点后开始升级；重启后自动恢复调度", resp.Drain.SessionsBefore)
			} else if resp.Drain.Forced {
				note = fmt.Sprintf("排空超时（残留 %d 会话）仍继续升级；重启后自动恢复调度", resp.Drain.SessionsAfter)
			}
		}
		resp.Note = note
		// 节点重启连上后恢复调度（异步轮询，避免阻塞响应）。
		go restoreSchedulingAfterBinaryUpdate(node.ID)
		c.JSON(http.StatusOK, resp)
		return
	}
	// 旧版 SFU 不认识 UpdateBinary 时会回 BAD_COMMAND：给出可操作提示。
	if resp.ErrorCode == "BAD_COMMAND" || strings.Contains(strings.ToLower(resp.ErrorMessage), "unknown") {
		resp.ErrorMessage = fmt.Sprintf(
			"%s（节点当前版本 %q 可能过旧，不支持远程升级；请先手动替换该节点 owl-sfu 为含 UpdateBinary 的版本，再点升级。本机内嵌 SFU 请重编 data/embedded-sfu/bin/owl-sfu）",
			resp.ErrorMessage, node.NodeVersion,
		)
	}
	c.JSON(http.StatusConflict, resp)
}

// restoreSchedulingAfterBinaryUpdate 轮询等待节点重新上线后恢复调度与 undrain。
// 幂等：节点尚未 Register 时仅写 enabled=true + ENROLLED；上线后刷 ONLINE + Undrain。
func restoreSchedulingAfterBinaryUpdate(nodeID uuid.UUID) {
	// 下载 + 重启通常 < 60s；最多盯 3 分钟。
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		voice.RestoreSchedulingAfterUpgrade(nodeID)
	}
	// 最后再补一次，覆盖临界重启窗口。
	voice.RestoreSchedulingAfterUpgrade(nodeID)
}

// listSfuReleases GET /admin/sfu/releases：列出本地发布目录中的可用版本工件。
func (a *API) listSfuReleases(c *gin.Context) {
	if !a.requireSFU(c) {
		return
	}
	dir := a.sfuReleaseDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"releases": []any{}, "release_dir": dir})
			return
		}
		fail(c, http.StatusInternalServerError, "IO_ERROR", "读取发布目录失败")
		return
	}
	type release struct {
		Filename string `json:"filename"`
		Version  string `json:"version"`
		GOOS     string `json:"goos"`
		GOARCH   string `json:"goarch"`
		Size     int64  `json:"size"`
		URL      string `json:"url"`
	}
	// 文件名约定：owl-sfu-<version>-<goos>-<goarch>
	re := regexp.MustCompile(`^owl-sfu-(.+)-(linux|darwin|windows)-(amd64|arm64)$`)
	out := make([]release, 0)
	base := a.publicBaseURL(c)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, release{
			Filename: name,
			Version:  m[1],
			GOOS:     m[2],
			GOARCH:   m[3],
			Size:     info.Size(),
			URL:      strings.TrimRight(base, "/") + "/sfu-releases/" + name,
		})
	}
	c.JSON(http.StatusOK, gin.H{"releases": out, "release_dir": dir})
}

func (a *API) sfuReleaseDir() string {
	if a.sfu != nil && a.sfu.Cfg != nil && a.sfu.Cfg.SFUReleaseDir != "" {
		return a.sfu.Cfg.SFUReleaseDir
	}
	return filepath.Join("data", "sfu-releases")
}

func (a *API) publicBaseURL(c *gin.Context) string {
	if a.sfu != nil && a.sfu.Cfg != nil && a.sfu.Cfg.PublicBaseURL != "" {
		return strings.TrimRight(a.sfu.Cfg.PublicBaseURL, "/")
	}
	// 回落请求 Host（开发隧道场景可能需要管理员显式设 PUBLIC_BASE_URL）。
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return scheme + "://" + c.Request.Host
}

// resolveLocalRelease 从发布目录解析版本工件，返回节点可下载的 URL、sha256 与实际平台。
// goos/goarch 可空：自动在本机平台、linux/amd64、目录内其它匹配中择优。
func (a *API) resolveLocalRelease(c *gin.Context, version, goos, goarch string) (url, sha, pickedGOOS, pickedGOARCH string, err error) {
	if !sfuVersionPattern.MatchString(version) {
		return "", "", "", "", fmt.Errorf("非法版本号")
	}
	dir := a.sfuReleaseDir()
	type cand struct {
		goos, goarch, name, path string
	}
	var matches []cand
	prefix := "owl-sfu-" + version + "-"
	entries, readErr := os.ReadDir(dir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", "", "", "", fmt.Errorf("读取发布目录失败: %w", readErr)
	}
	re := regexp.MustCompile(`^owl-sfu-` + regexp.QuoteMeta(version) + `-(linux|darwin|windows)-(amd64|arm64)$`)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		matches = append(matches, cand{goos: m[1], goarch: m[2], name: e.Name(), path: filepath.Join(dir, e.Name())})
	}

	pick := func(wantOS, wantArch string) *cand {
		for i := range matches {
			if matches[i].goos == wantOS && matches[i].goarch == wantArch {
				return &matches[i]
			}
		}
		return nil
	}

	var chosen *cand
	if goos != "" && goarch != "" {
		chosen = pick(goos, goarch)
		if chosen == nil {
			return "", "", "", "", fmt.Errorf("本地发布文件不存在: %s%s-%s（请放入 SFU_RELEASE_DIR）", prefix, goos, goarch)
		}
	} else {
		// 优先级：本机平台（内嵌 SFU）→ linux/amd64（公网节点）→ 唯一匹配 → 任意第一个
		hostOS, hostArch := runtime.GOOS, runtime.GOARCH
		for _, try := range [][2]string{
			{hostOS, hostArch},
			{"linux", "amd64"},
			{"linux", "arm64"},
			{"darwin", "arm64"},
			{"darwin", "amd64"},
		} {
			if c := pick(try[0], try[1]); c != nil {
				chosen = c
				break
			}
		}
		if chosen == nil && len(matches) == 1 {
			chosen = &matches[0]
		}
		if chosen == nil && len(matches) > 0 {
			chosen = &matches[0]
		}
		if chosen == nil {
			return "", "", "", "", fmt.Errorf(
				"本地无版本 %s 的发布文件（目录 %s）。请放入 owl-sfu-%s-<goos>-<goarch>，例如本机用 owl-sfu-%s-%s-%s",
				version, dir, version, version, hostOS, hostArch,
			)
		}
	}

	f, err := os.Open(chosen.path)
	if err != nil {
		return "", "", "", "", fmt.Errorf("打开发布文件失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", "", "", fmt.Errorf("计算 sha256 失败: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	base := a.publicBaseURL(c)
	return strings.TrimRight(base, "/") + "/sfu-releases/" + chosen.name, sum, chosen.goos, chosen.goarch, nil
}
