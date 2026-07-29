package voice

// ICE 失败上报与双信号提前判死（docs 13 FR-16、docs 15 BI.2/BI.3）：
// 客户端 ICE disconnected/failed 时上报当前节点，作为独立于心跳的节点死亡信号源。
// 组合规则：≥2 个不同用户在 60s 窗口内上报同一节点 + 该节点最近一次心跳超过
// 1 个周期未到 → 提前判死（走既有 InternalNodeDown → migrateNode 的迁移路径），
// 不必等满 3 个心跳周期（15s）的硬判死。
//
// 防误杀铁律：节点心跳新鲜（≤1 周期）时无论多少用户上报都绝不判死，只记录；
// 心跳新鲜度以内存注册表实时值为准（sfuctl.NodeInfo.LastSeenAt），
// 无心跳记录（零值）视为证据不足同样不判死。判死权威仅 Newt-Server（BI.4）。

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

const (
	// iceFailureWindow 独立上报聚合窗口（BI.2 定稿 60s）。
	iceFailureWindow = 60 * time.Second
	// iceFailureUserDedup 同一用户对同一节点的去重窗口（docs 13 FR-16 节流对齐）。
	iceFailureUserDedup = 10 * time.Second
	// iceFailureMinReporters 提前判死所需的独立上报用户数下限（BI.3 定稿 ≥2）。
	iceFailureMinReporters = 2
	// iceFailureDeclareSuppress 同一节点两次提前判死宣告的最小间隔
	//（migrateNode 本身幂等，抑制仅为防日志/事件风暴）。
	iceFailureDeclareSuppress = 60 * time.Second
)

// iceFailureStore 内存滑动窗口：node → user → 最近上报时刻。零值可用（惰性建表）。
type iceFailureStore struct {
	mu         sync.Mutex
	reports    map[uuid.UUID]map[uuid.UUID]time.Time
	declaredAt map[uuid.UUID]time.Time
}

// report 记录一条上报。同一用户 10s 去重窗口内的重复上报不计数（counted=false）；
// 返回该节点 60s 窗口内的独立上报用户数（顺带清理过期项）。
func (s *iceFailureStore) report(nodeID, userID uuid.UUID, now time.Time) (counted bool, reporters int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reports == nil {
		s.reports = map[uuid.UUID]map[uuid.UUID]time.Time{}
	}
	byUser := s.reports[nodeID]
	if byUser == nil {
		byUser = map[uuid.UUID]time.Time{}
		s.reports[nodeID] = byUser
	}
	counted = true
	if last, ok := byUser[userID]; ok && now.Sub(last) < iceFailureUserDedup {
		counted = false
	} else {
		byUser[userID] = now
	}
	for user, at := range byUser {
		if now.Sub(at) > iceFailureWindow {
			delete(byUser, user)
		}
	}
	return counted, len(byUser)
}

// markDeclared 抑制窗口内已宣告过返回 false，否则记账并返回 true。
func (s *iceFailureStore) markDeclared(nodeID uuid.UUID, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.declaredAt[nodeID]; ok && now.Sub(at) < iceFailureDeclareSuppress {
		return false
	}
	if s.declaredAt == nil {
		s.declaredAt = map[uuid.UUID]time.Time{}
	}
	s.declaredAt[nodeID] = now
	return true
}

// heartbeatInterval 心跳周期（RegisterAck 下发口径，定稿 5000ms）；
// 未配置（单测最小构造）时取定稿默认。
func (s *Service) heartbeatInterval() time.Duration {
	if s.cfg.SFUHeartbeatIntervalMS > 0 {
		return time.Duration(s.cfg.SFUHeartbeatIntervalMS) * time.Millisecond
	}
	return 5 * time.Second
}

// maybeDeclareNodeDown 双信号组合裁决：独立上报用户数达标且节点心跳超过 1 个周期
// 未到时提前判死，复用 InternalNodeDown 的处理路径（migrateNode 批量迁移，幂等）。
// 返回是否实际宣告（单测锚点）。
func (s *Service) maybeDeclareNodeDown(nodeID uuid.UUID, reporters int, now time.Time) bool {
	if reporters < iceFailureMinReporters {
		return false
	}
	info, err := sfuctl.Dir().Node(nodeID)
	if err != nil {
		return false
	}
	// 心跳正常绝不判死；无心跳记录（零值）证据不足同样不判死（防误杀）。
	if info.LastSeenAt.IsZero() || now.Sub(info.LastSeenAt) <= s.heartbeatInterval() {
		return false
	}
	if !s.iceFailures.markDeclared(nodeID, now) {
		return false
	}
	log.Printf("voice: 节点 %s 提前判死（%d 个用户 60s 内上报 ICE 失败 + 心跳超 1 周期未到），触发批量迁移",
		nodeID, reporters)
	// 与 InternalNodeDown 订阅处理同一路径（service.go handleBusEvent）：
	// 直接进迁移引擎，活跃 job 合并保证幂等。
	s.engine.migrateNode(nodeID, model.MigrationReasonDeath)
	return true
}

// ---------------------------------------------------------------------------
// POST /voice/ice-failure（docs 13 FR-16 / 15 BI.2 客户端侧 ICE 失败上报）
// ---------------------------------------------------------------------------

type iceFailureRequest struct {
	NodeID    uuid.UUID  `json:"node_id" binding:"required"`
	SessionID *uuid.UUID `json:"session_id"`
	Detail    string     `json:"detail" binding:"omitempty,max=512"`
}

// handleICEFailure 登录即可上报；服务端校验上报者当前 VoiceState 确实挂在该节点上，
// 否则忽略（防伪造指控诱发对无关节点的误迁移）。响应恒 200：上报是尽力而为的
// 诊断信号，被忽略/去重不构成客户端错误。
func (s *Service) handleICEFailure(c *gin.Context) {
	user := s.currentUser(c)
	var input iceFailureRequest
	if !bind(c, &input) {
		return
	}
	var vs model.VoiceState
	err := s.db.
		Where("user_id = ? AND node_id = ? AND channel_id IS NOT NULL", user.ID, input.NodeID).
		First(&vs).Error
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"recorded": false, "reason": "NOT_ON_NODE"})
		return
	}
	// 携带 session_id 时须与当前会话一致：不一致视为迟到的旧会话上报，忽略。
	if input.SessionID != nil && (vs.VoiceSessionID == nil || *vs.VoiceSessionID != *input.SessionID) {
		c.JSON(http.StatusOK, gin.H{"recorded": false, "reason": "STALE_SESSION"})
		return
	}
	now := time.Now()
	counted, reporters := s.iceFailures.report(input.NodeID, user.ID, now)
	if !counted {
		c.JSON(http.StatusOK, gin.H{"recorded": false, "reason": "DUPLICATE"})
		return
	}
	declared := s.maybeDeclareNodeDown(input.NodeID, reporters, now)
	c.JSON(http.StatusOK, gin.H{"recorded": true, "reporters": reporters, "node_down_declared": declared})
}
