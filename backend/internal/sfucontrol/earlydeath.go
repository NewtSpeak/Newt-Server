// earlydeath.go BI.3 提前判死（docs 15 §5）：
// 除心跳外聚合两类独立提前信号源——级联邻居 EdgeDown 指控（BI.2 ①）与
// 客户端/节点侧 ICE 失败上报（BI.2 ②）。组合规则（BI.3，阈值可配）：
// ≥ MinSources 个独立信号源 + ≥ MinHeartbeatMisses 次心跳丢失 → 提前判死，
// 无需等满 3 次心跳失败。判死权威仍仅 Owl-Server（BI.4），信号只是证据来源。
package sfucontrol

import (
	"time"

	"github.com/google/uuid"
)

// EarlyDeathConfig 提前判死组合规则参数（docs 15 BI.3「阈值实现期可调」）。
type EarlyDeathConfig struct {
	// Enabled 关闭时仅保留 5s×3 硬判死。
	Enabled bool
	// MinSources 触发提前判死所需的独立信号源下限（BI.3 定稿 ≥2）。
	MinSources int
	// MinHeartbeatMisses 触发提前判死所需的心跳丢失次数下限（BI.3 定稿 ≥1）。
	MinHeartbeatMisses int
	// SignalTTL 单条信号的有效窗口，超时的信号不再计入（防陈旧证据误判）。
	SignalTTL time.Duration
}

// DefaultEarlyDeathConfig BI.3 定稿默认值。
func DefaultEarlyDeathConfig() EarlyDeathConfig {
	return EarlyDeathConfig{
		Enabled:            true,
		MinSources:         2,
		MinHeartbeatMisses: 1,
		SignalTTL:          30 * time.Second,
	}
}

// normalize 补齐零值字段为定稿默认（Enabled 保持调用方语义）。
func (c EarlyDeathConfig) normalize() EarlyDeathConfig {
	def := DefaultEarlyDeathConfig()
	if c.MinSources <= 0 {
		c.MinSources = def.MinSources
	}
	if c.MinHeartbeatMisses <= 0 {
		c.MinHeartbeatMisses = def.MinHeartbeatMisses
	}
	if c.SignalTTL <= 0 {
		c.SignalTTL = def.SignalTTL
	}
	return c
}

// heartbeatMisses 心跳丢失次数 = 距上次上报的整心跳周期数。
func heartbeatMisses(sinceLastSeen, interval time.Duration) int {
	if interval <= 0 {
		return 0
	}
	return int(sinceLastSeen / interval)
}

// earlyDeathEligible BI.3 组合规则纯函数（单测锚点）：
// 独立信号源 ≥ MinSources 且 心跳丢失 ≥ MinHeartbeatMisses → 可提前判死。
func earlyDeathEligible(sinceLastSeen, interval time.Duration, sources int, cfg EarlyDeathConfig) bool {
	if !cfg.Enabled {
		return false
	}
	cfg = cfg.normalize()
	return sources >= cfg.MinSources &&
		heartbeatMisses(sinceLastSeen, interval) >= cfg.MinHeartbeatMisses
}

// ReportSuspect 记录一条针对 nodeID 的提前信号。origin 标识独立信号来源
// （如 "edge_down:<邻居节点>"、"client_ice:<用户>"、"self_ice"），
// 同一 origin 重复上报只刷新时间，不叠加计数——独立性以 origin 去重保证。
// 未知节点（无注册表状态）忽略：无心跳基线可组合，判死无从谈起。
func (r *Registry) ReportSuspect(nodeID uuid.UUID, origin string) {
	if origin == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[nodeID]
	if !ok {
		return
	}
	if state.suspicions == nil {
		state.suspicions = map[string]time.Time{}
	}
	state.suspicions[origin] = time.Now()
}

// SuspicionSources 返回某节点在 ttl 窗口内的独立信号源数（顺带清理过期信号）。
func (r *Registry) SuspicionSources(nodeID uuid.UUID, ttl time.Duration) int {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[nodeID]
	if !ok {
		return 0
	}
	for origin, at := range state.suspicions {
		if now.Sub(at) > ttl {
			delete(state.suspicions, origin)
		}
	}
	return len(state.suspicions)
}

// MarkEarlyDead 扫描满足 BI.3 组合规则的在线节点，标记离线并返回本次新标记的节点。
// interval 为心跳周期（丢失次数基准）。与 MarkStale（硬判死）互斥推进：
// 任一路径标记后 online=false，另一路径不再重复触发。
func (r *Registry) MarkEarlyDead(interval time.Duration, cfg EarlyDeathConfig) []uuid.UUID {
	if !cfg.Enabled {
		return nil
	}
	cfg = cfg.normalize()
	now := time.Now()
	var dead []uuid.UUID
	r.mu.Lock()
	defer r.mu.Unlock()
	for nodeID, state := range r.states {
		if !state.online {
			continue
		}
		for origin, at := range state.suspicions {
			if now.Sub(at) > cfg.SignalTTL {
				delete(state.suspicions, origin)
			}
		}
		if earlyDeathEligible(now.Sub(state.lastSeen), interval, len(state.suspicions), cfg) {
			state.online = false
			dead = append(dead, nodeID)
		}
	}
	return dead
}
