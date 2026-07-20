package sfunode

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"gorm.io/gorm"
)

// directory sfuctl.Directory 实现：DB 提供节点归属与静态属性，Hub 内存快照覆盖实时容量。
type directory struct {
	db  *gorm.DB
	hub *Hub
}

var _ sfuctl.Directory = (*directory)(nil)

// PoolNodes 返回 Guild 节点池候选（docs 07 专项 2）：
//  1. 有勾选节点 → 返回勾选集合；
//  2. 池为空且回落开关打开（默认打开）→ 平台默认池；
//  3. 池为空且回落关闭 → 空列表（调用方应明确失败）。
func (d *directory) PoolNodes(guildID uuid.UUID) ([]sfuctl.NodeInfo, error) {
	var selections []model.SfuGuildNodeSelection
	if err := d.db.Where("guild_id = ?", guildID).Find(&selections).Error; err != nil {
		return nil, fmt.Errorf("查询服级节点池失败: %w", err)
	}
	var nodes []model.SfuNode
	if len(selections) > 0 {
		ids := make([]uuid.UUID, 0, len(selections))
		for _, sel := range selections {
			ids = append(ids, sel.NodeID)
		}
		if err := d.db.Where("id IN ?", ids).Find(&nodes).Error; err != nil {
			return nil, fmt.Errorf("查询池内节点失败: %w", err)
		}
	} else {
		// 空池：按回落开关决定是否使用平台默认池。
		fallback := true
		var pool model.SfuGuildNodePool
		if err := d.db.First(&pool, "guild_id = ?", guildID).Error; err == nil {
			fallback = pool.FallbackToDefault
		}
		if !fallback {
			return nil, nil
		}
		if err := d.db.Where("platform_default = ?", true).Find(&nodes).Error; err != nil {
			return nil, fmt.Errorf("查询平台默认池失败: %w", err)
		}
	}
	return d.toInfos(nodes), nil
}

func (d *directory) Node(nodeID uuid.UUID) (sfuctl.NodeInfo, error) {
	var node model.SfuNode
	if err := d.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return sfuctl.NodeInfo{}, sfuctl.ErrNodeNotFound
	}
	return d.toInfo(node), nil
}

func (d *directory) AllNodes() ([]sfuctl.NodeInfo, error) {
	var nodes []model.SfuNode
	if err := d.db.Order("created_at ASC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询全部节点失败: %w", err)
	}
	return d.toInfos(nodes), nil
}

func (d *directory) toInfos(nodes []model.SfuNode) []sfuctl.NodeInfo {
	infos := make([]sfuctl.NodeInfo, 0, len(nodes))
	for _, node := range nodes {
		infos = append(infos, d.toInfo(node))
	}
	return infos
}

// toInfo DB 记录 + 内存实时快照 → 调度用 NodeInfo。
func (d *directory) toInfo(node model.SfuNode) sfuctl.NodeInfo {
	info := NodeInfoFromModel(node)
	if d.hub != nil {
		if live, ok := d.hub.Live(node.ID); ok {
			info.Online = true
			info.CurrentUsers = live.CurrentUsers
			info.CPUPercent = live.CPUPct
			info.MemPercent = live.MemPct
			info.BandwidthOutMbps = live.BandwidthOutMbps
			info.ScreenTracks = live.ScreenTracks
			if live.NodeRTTMs != nil {
				info.NodeRTTMs = live.NodeRTTMs
			}
		}
	}
	return info
}

// NodeInfoFromModel 纯转换：DB 记录 → NodeInfo（不含实时覆盖，便于单测）。
func NodeInfoFromModel(node model.SfuNode) sfuctl.NodeInfo {
	endpoint := ""
	if len(node.WebRTCHosts) > 0 {
		endpoint = node.WebRTCHosts[0]
	}
	return sfuctl.NodeInfo{
		ID:                   node.ID,
		DisplayName:          node.DisplayName,
		Region:               node.Labels["region"],
		Labels:               node.Labels,
		Status:               node.Status,
		Online:               node.Status == model.SfuNodeOnline,
		Draining:             node.Status == model.SfuNodeDraining,
		EnabledForScheduling: node.EnabledForScheduling,
		MaxUsers:             node.MaxUsers,
		CurrentUsers:         node.CurrentUsers,
		CPUPercent:           node.CPUPct,
		MemPercent:           node.MemPct,
		BandwidthOutMbps:     node.BandwidthOutMbps,
		ScreenTracks:         node.ScreenTracks,
		WebRTCEndpoint:       endpoint,
		NodeRTTMs:            node.NodeRTTMs,
	}
}

// ResolvePool 纯逻辑的节点池解析（供单测）：
// selected 非空返回 selected；否则按 fallback 开关返回平台默认池或空。
func ResolvePool(selected, platformDefault []model.SfuNode, fallbackEnabled bool) []model.SfuNode {
	if len(selected) > 0 {
		return selected
	}
	if !fallbackEnabled {
		return nil
	}
	return platformDefault
}
