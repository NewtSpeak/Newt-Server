package voice

// 候选节点池列表下发（docs 13 §7.1 / FR-22、docs 10 S.1）：
// 客户端需要知道本服候选节点的 node_id + RTT 探测地址，才能做后台周期探测与
// 进房前补测；探测结果经 POST /voice/rtt 上报（样本 TTL 60s）。
// 安全边界：调度决策权在 Server（docs 10 X.3 fallback 列表不下发客户端），
// 本端点只暴露探测所需的最小字段，绝不泄露容量/负载/调度开关等内部信息。

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

// voiceNodeView 下发给客户端的候选节点视图：仅探测所需最小字段。
type voiceNodeView struct {
	NodeID uuid.UUID `json:"node_id"`
	// RTTProbeURL GET /rtt 探测地址（无鉴权限速端点，docs 15 BF.5），
	// 由节点自报的 advertise_wss_url 推导。
	RTTProbeURL string `json:"rtt_probe_url"`
	Region      string `json:"region"`
}

// rttProbeURL 由 advertise_wss_url 推导 RTT 探测地址：
// wss://<host>[:port]/<path> → https://<host>[:port]/rtt（ws → http 同理，本地调试用）。
// 无法解析或无 host 时返回空串（调用方跳过该节点）。
func rttProbeURL(wssURL string) string {
	parsed, err := url.Parse(wssURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	scheme := "https"
	if parsed.Scheme == "ws" || parsed.Scheme == "http" {
		scheme = "http"
	}
	return scheme + "://" + parsed.Host + "/rtt"
}

// voiceNodeViews 过滤出 ONLINE 节点并组装客户端视图（纯函数，单测锚点）。
func voiceNodeViews(nodes []sfuctl.NodeInfo) []voiceNodeView {
	views := make([]voiceNodeView, 0, len(nodes))
	for _, info := range nodes {
		if !info.Online || info.Status != model.SfuNodeOnline {
			continue
		}
		probe := rttProbeURL(info.WebRTCEndpoint)
		if probe == "" {
			continue
		}
		views = append(views, voiceNodeView{NodeID: info.ID, RTTProbeURL: probe, Region: info.Region})
	}
	return views
}

// handleListVoiceNodes GET /guilds/:guildID/voice/nodes：成员即可读。
// 返回本服节点池（sfuctl.Dir().PoolNodes 已含「服级池为空回落平台默认池」语义）
// 中 ONLINE 节点的探测视图。非成员一律 404（防扫频）。
func (s *Service) handleListVoiceNodes(c *gin.Context) {
	user := s.currentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	if _, err := perms.LoadGuild(s.db, user, guildID); err != nil {
		fail(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在或不可见")
		return
	}
	nodes, err := sfuctl.Dir().PoolNodes(guildID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "NODE_DIRECTORY_ERROR", "查询节点池失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodes": voiceNodeViews(nodes)})
}
