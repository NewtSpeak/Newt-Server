package voice

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

// TestICEFailureStoreDedupAndWindow 滑动窗口纯逻辑：10s 用户去重、60s 窗口过期、
// 独立用户计数与宣告抑制。
func TestICEFailureStoreDedupAndWindow(t *testing.T) {
	store := &iceFailureStore{}
	node := uuid.New()
	userA, userB := uuid.New(), uuid.New()
	base := time.Now()

	counted, reporters := store.report(node, userA, base)
	if !counted || reporters != 1 {
		t.Fatalf("首次上报应计数: counted=%v reporters=%d", counted, reporters)
	}
	// 10s 去重窗口内重复上报不计数。
	counted, reporters = store.report(node, userA, base.Add(5*time.Second))
	if counted || reporters != 1 {
		t.Fatalf("去重窗口内重复上报不应计数: counted=%v reporters=%d", counted, reporters)
	}
	// 去重窗口过后可再次计数（仍是同一独立用户，人数不变）。
	counted, reporters = store.report(node, userA, base.Add(iceFailureUserDedup+time.Second))
	if !counted || reporters != 1 {
		t.Fatalf("去重窗口过后应重新计数: counted=%v reporters=%d", counted, reporters)
	}
	// 第二个用户：独立上报人数 2。
	counted, reporters = store.report(node, userB, base.Add(12*time.Second))
	if !counted || reporters != 2 {
		t.Fatalf("第二个用户应独立计数: counted=%v reporters=%d", counted, reporters)
	}
	// 60s 窗口过期：旧样本淘汰，只剩新上报者。
	_, reporters = store.report(node, uuid.New(), base.Add(iceFailureWindow+13*time.Second))
	if reporters != 1 {
		t.Fatalf("窗口过期后应只剩新上报者: reporters=%d", reporters)
	}

	// 宣告抑制：首次放行，抑制窗口内拒绝。
	if !store.markDeclared(node, base) {
		t.Fatal("首次宣告应放行")
	}
	if store.markDeclared(node, base.Add(30*time.Second)) {
		t.Fatal("抑制窗口内不应重复宣告")
	}
	if !store.markDeclared(node, base.Add(iceFailureDeclareSuppress+time.Second)) {
		t.Fatal("抑制窗口过后应可再次宣告")
	}
}

// iceFailureTestEnv 组装 handler 级测试环境：真实 DB + 假节点目录 + 可切换上报用户。
type iceFailureTestEnv struct {
	svc     *Service
	router  *gin.Engine
	current model.User
}

func newICEFailureEnv(t *testing.T, nodes []sfuctl.NodeInfo) *iceFailureTestEnv {
	t.Helper()
	env := &iceFailureTestEnv{svc: newTestService(t, nodes)}
	gin.SetMode(gin.TestMode)
	env.router = gin.New()
	env.router.POST("/voice/ice-failure", func(c *gin.Context) {
		c.Set(currentUserContextKey, env.current)
		env.svc.handleICEFailure(c)
	})
	return env
}

// report 以 user 身份上报，返回响应 JSON。
func (env *iceFailureTestEnv) report(t *testing.T, user uuid.UUID, body map[string]any) map[string]any {
	t.Helper()
	env.current = model.User{ID: user}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/voice/ice-failure", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ice-failure 返回 %d: %s", rec.Code, rec.Body.String())
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return parsed
}

// putVoiceState 直接落一行「user 在 node 上的语音会话」。
func putVoiceState(t *testing.T, svc *Service, guildID, channelID, nodeID, userID uuid.UUID) uuid.UUID {
	t.Helper()
	sessionID := uuid.New()
	now := time.Now().UTC()
	vs := model.VoiceState{
		ID: uuid.New(), GuildID: guildID, UserID: userID,
		ChannelID: &channelID, NodeID: &nodeID, RoomID: &channelID,
		VoiceSessionID: &sessionID, JoinedAt: &now,
	}
	if err := svc.db.Create(&vs).Error; err != nil {
		t.Fatalf("插入 VoiceState 失败: %v", err)
	}
	return sessionID
}

func deathJobCount(t *testing.T, svc *Service, nodeID uuid.UUID) int64 {
	t.Helper()
	var count int64
	if err := svc.db.Model(&model.VoiceMigrationJob{}).
		Where("from_node_id = ? AND reason = ?", nodeID, model.MigrationReasonDeath).
		Count(&count).Error; err != nil {
		t.Fatalf("查迁移 job 失败: %v", err)
	}
	return count
}

// TestICEFailureDoubleSignalDeath 双信号判死门槛（docs 15 BI.2/BI.3）：
//   - 上报者不在该节点上 → 忽略；
//   - 同一用户 10s 去重；
//   - ≥2 个用户上报但心跳新鲜 → 绝不判死（只记录）；
//   - ≥2 个用户上报 + 心跳超 1 个周期未到 → 提前判死，走 migrateNode 批量迁移；
//   - 宣告抑制窗口内不重复触发。
func TestICEFailureDoubleSignalDeath(t *testing.T) {
	nodeID, spareNodeID := uuid.New(), uuid.New()
	freshNode := testNode(nodeID)
	freshNode.LastSeenAt = time.Now() // 心跳新鲜（1 周期内）
	spare := testNode(spareNodeID)
	spare.LastSeenAt = time.Now()
	env := newICEFailureEnv(t, []sfuctl.NodeInfo{freshNode, spare})
	svc := env.svc

	guildID, channelID := uuid.New(), uuid.New()
	userA, userB, userC, stranger := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	putVoiceState(t, svc, guildID, channelID, nodeID, userA)
	putVoiceState(t, svc, guildID, channelID, nodeID, userB)
	sessionC := putVoiceState(t, svc, guildID, channelID, nodeID, userC)

	body := map[string]any{"node_id": nodeID}

	// 1. 不在该节点上的用户：忽略。
	resp := env.report(t, stranger, body)
	if resp["recorded"] != false || resp["reason"] != "NOT_ON_NODE" {
		t.Fatalf("非节点会话上报应被忽略: %v", resp)
	}
	// 2. userA 首报计数；10s 内重复去重。
	resp = env.report(t, userA, body)
	if resp["recorded"] != true || resp["reporters"].(float64) != 1 {
		t.Fatalf("userA 首报异常: %v", resp)
	}
	resp = env.report(t, userA, body)
	if resp["recorded"] != false || resp["reason"] != "DUPLICATE" {
		t.Fatalf("10s 去重未生效: %v", resp)
	}
	// 3. session_id 与当前会话不符：忽略（迟到的旧会话上报）。
	resp = env.report(t, userC, map[string]any{"node_id": nodeID, "session_id": uuid.New()})
	if resp["recorded"] != false || resp["reason"] != "STALE_SESSION" {
		t.Fatalf("旧会话上报应被忽略: %v", resp)
	}
	// 4. userB 上报 → 2 个独立用户，但心跳新鲜：绝不判死。
	resp = env.report(t, userB, body)
	if resp["recorded"] != true || resp["reporters"].(float64) != 2 || resp["node_down_declared"] != false {
		t.Fatalf("心跳新鲜时不应判死: %v", resp)
	}
	if count := deathJobCount(t, svc, nodeID); count != 0 {
		t.Fatalf("心跳新鲜时不应创建迁移 job，got %d", count)
	}

	// 5. 心跳变陈旧（超 1 个周期 = 5s）：下一条上报触发提前判死。
	staleNode := freshNode
	staleNode.LastSeenAt = time.Now().Add(-10 * time.Second)
	sfuctl.SetDirectory(fakeDirectory{nodes: []sfuctl.NodeInfo{staleNode, spare}})
	resp = env.report(t, userC, map[string]any{"node_id": nodeID, "session_id": sessionC})
	if resp["recorded"] != true || resp["node_down_declared"] != true {
		t.Fatalf("双信号达标应提前判死: %v", resp)
	}
	// 节点上 3 个会话全部进入 DEATH 批量迁移。
	if count := deathJobCount(t, svc, nodeID); count != 3 {
		t.Fatalf("应为节点上 3 个会话建 DEATH 迁移 job，got %d", count)
	}

	// 6. 抑制窗口内再有新用户上报：不重复宣告，job 数不变（活跃 job 合并幂等）。
	userD := uuid.New()
	putVoiceState(t, svc, guildID, channelID, nodeID, userD)
	resp = env.report(t, userD, body)
	if resp["recorded"] != true || resp["node_down_declared"] != false {
		t.Fatalf("抑制窗口内不应重复宣告: %v", resp)
	}
	if count := deathJobCount(t, svc, nodeID); count != 3 {
		t.Fatalf("抑制窗口内 job 数不应变化，got %d", count)
	}
}

// TestICEFailureNoHeartbeatEvidence 无心跳记录（LastSeenAt 零值）时证据不足不判死。
func TestICEFailureNoHeartbeatEvidence(t *testing.T) {
	nodeID := uuid.New()
	node := testNode(nodeID) // LastSeenAt 零值
	env := newICEFailureEnv(t, []sfuctl.NodeInfo{node})
	svc := env.svc

	guildID, channelID := uuid.New(), uuid.New()
	userA, userB := uuid.New(), uuid.New()
	putVoiceState(t, svc, guildID, channelID, nodeID, userA)
	putVoiceState(t, svc, guildID, channelID, nodeID, userB)

	body := map[string]any{"node_id": nodeID}
	env.report(t, userA, body)
	resp := env.report(t, userB, body)
	if resp["node_down_declared"] != false {
		t.Fatalf("无心跳基线时不应判死: %v", resp)
	}
	if count := deathJobCount(t, svc, nodeID); count != 0 {
		t.Fatalf("无心跳基线时不应创建迁移 job，got %d", count)
	}
}
