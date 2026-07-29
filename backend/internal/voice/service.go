package voice

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/mediatoken"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
	"gorm.io/gorm"
)

// 语音频道硬顶 200 人（docs 06；级联 peer 不计入，docs 08 A.3）。
const channelHardCap = 200

// drainHardMigrateAfter Drain 硬迁时限（docs 09 I.6：60s 内尽量迁完，超时硬迁）。
const drainHardMigrateAfter = 60 * time.Second

// Service 语音会话编排核心：进出房、caps、调度、级联与热迁移共享此实例。
// mu 串行化所有「改 VoiceState / 级联图 / 迁移 job」的编排动作（单实例控制面足够）。
type Service struct {
	db  *gorm.DB
	bus *eventbus.Bus
	cfg config.Config
	// tokens 全局 Media Token 签发器（internal/mediatoken，含 sid，docs 协议 §1 + 15 BJ）；
	// 与 SFU enroll/RegisterAck 下发的验签公钥同一密钥，取代 voice 早期自建 signer。
	tokens *mediatoken.Manager
	rtt    *rttStore
	resv   *reservationStore
	sched  schedConfig
	engine *migrationEngine
	// iceFailedAt 客户端 ICE 失败上报节流（user → 最近上报时刻；mu 保护）。
	iceFailedAt map[uuid.UUID]time.Time
	// overload 过载自动迁移检测器（docs 09 I.3–I.5，默认关）；
	// overloadNodes 为指标源（生产 = sfuctl.Dir().AllNodes()，单测可注入假快照）。
	overload      *overloadDetector
	overloadNodes func() ([]sfuctl.NodeInfo, error)
	// edgeFlaps 级联边 EdgeDown 抖动跟踪：短窗反复断边升级分区迁移（docs 09 §3.3）。
	edgeFlaps *edgeFlapTracker
	// iceFailures ICE 失败上报滑动窗口与双信号提前判死记账
	//（icefailure.go；自带锁，零值可用）。
	iceFailures iceFailureStore

	mu sync.Mutex
}

// ---------------------------------------------------------------------------
// Gateway 事件载荷
// ---------------------------------------------------------------------------

// voiceStateEventPayload VOICE_STATE_UPDATE 载荷；Reason 仅踢出等场景携带。
// Username / DisplayName / AvatarURL / Nickname 为展示字段（非 VoiceState 表列），
// 供客户端语音树在成员缓存未就绪时仍能渲染头像与昵称。
type voiceStateEventPayload struct {
	model.VoiceState
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Nickname    string `json:"nickname,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// voiceStateView REST 列表用：VoiceState + 用户展示字段。
type voiceStateView struct {
	model.VoiceState
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	Nickname    string `json:"nickname"`
}

// voiceServerUpdatePayload VOICE_SERVER_UPDATE 载荷（进房辅助路径 / 热迁移，docs 05 §11）。
type voiceServerUpdatePayload struct {
	GuildID     uuid.UUID  `json:"guild_id"`
	ChannelID   uuid.UUID  `json:"channel_id"`
	NodeID      uuid.UUID  `json:"node_id"`
	RoomID      uuid.UUID  `json:"room_id"`
	SFUEndpoint string     `json:"sfu_endpoint"`
	Token       string     `json:"token"`
	Caps        []string   `json:"caps"`
	SessionID   uuid.UUID  `json:"session_id"`
	ExpiresAt   time.Time  `json:"expires_at"`
	MigrationID *uuid.UUID `json:"migration_id,omitempty"`
}

// voiceCapsUpdatePayload VOICE_CAPS_UPDATE 载荷。
type voiceCapsUpdatePayload struct {
	GuildID   uuid.UUID `json:"guild_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
	Caps      []string  `json:"caps"`
}

// voiceMigratingPayload VOICE_MIGRATING 载荷；文案默认模糊（docs 09 O.2）。
type voiceMigratingPayload struct {
	MigrationID uuid.UUID `json:"migration_id"`
	GuildID     uuid.UUID `json:"guild_id"`
	ChannelID   uuid.UUID `json:"channel_id"`
	Message     string    `json:"message"`
}

// voiceMigratedPayload VOICE_MIGRATED 载荷。
type voiceMigratedPayload struct {
	MigrationID uuid.UUID `json:"migration_id"`
	GuildID     uuid.UUID `json:"guild_id"`
	ChannelID   uuid.UUID `json:"channel_id"`
	NodeID      uuid.UUID `json:"node_id"`
}

func (s *Service) enrichVoiceState(vs model.VoiceState, reason string) voiceStateEventPayload {
	payload := voiceStateEventPayload{VoiceState: vs, Reason: reason}
	var user model.User
	if err := s.db.Select("username", "display_name", "avatar_url").
		First(&user, "id = ?", vs.UserID).Error; err == nil {
		payload.Username = user.Username
		payload.DisplayName = user.DisplayName
		payload.AvatarURL = user.AvatarURL
	}
	var member model.Member
	if err := s.db.Select("nickname").
		First(&member, "guild_id = ? AND user_id = ?", vs.GuildID, vs.UserID).Error; err == nil {
		payload.Nickname = member.Nickname
	}
	return payload
}

func (s *Service) publishState(vs model.VoiceState, reason string) {
	payload := s.enrichVoiceState(vs, reason)
	// 隐身临场（adminpresence）：管理员在房时不向全服广播其语音状态，只回给本人，
	// 避免其出现在其他成员的语音列表/事件里（与 SFU hidden claim 抑制广播配合）。
	if vs.ChannelID != nil && StealthPredicate(vs.GuildID, vs.UserID) {
		s.bus.Publish(eventbus.Event{
			Type:    eventbus.EventVoiceStateUpdate,
			GuildID: &vs.GuildID,
			UserIDs: []uuid.UUID{vs.UserID},
			Payload: payload,
		})
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventVoiceStateUpdate,
		GuildID:   &vs.GuildID,
		ChannelID: vs.ChannelID,
		Payload:   payload,
	})
}

// PublishConnectedChange 供 sfucontrol RoomEvent 回调：SFU 上报 PARTICIPANT_JOINED/LEFT
// 更新 VoiceState.connected 后广播 VOICE_STATE_UPDATE，刷新后名单/连接态可实时同步。
// sharedService 未装配时为 no-op。
func PublishConnectedChange(vs model.VoiceState) {
	if sharedService == nil {
		return
	}
	sharedService.publishState(vs, "")
}

// publishChannelStatus 语音频道人数变化时广播 VOICE_CHANNEL_STATUS
//（进出房路径同步调用，端到端 <2s，docs 14 §3.2）。事件带 ChannelID，
// hub 按可见性过滤；mode 取舞台配置（无记录为 FREE_DISCUSSION 默认）。
func (s *Service) publishChannelStatus(guildID, channelID uuid.UUID) {
	var count int64
	if err := s.db.Model(&model.VoiceState{}).Where("channel_id = ?", channelID).Count(&count).Error; err != nil {
		return
	}
	mode := model.StageModeFree
	var cfg model.StageChannelConfig
	if err := s.db.First(&cfg, "channel_id = ?", channelID).Error; err == nil {
		mode = cfg.Mode
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventVoiceChannelStatus,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload:   eventbus.NewVoiceChannelStatusPayload(guildID, channelID, int(count), mode),
	})
}

func (s *Service) publishToUser(eventType string, userID uuid.UUID, guildID uuid.UUID, payload any) {
	s.bus.Publish(eventbus.Event{
		Type:    eventType,
		GuildID: &guildID,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

// ---------------------------------------------------------------------------
// VoiceState 辅助
// ---------------------------------------------------------------------------

// loadVoiceState 取（guild, user）的 VoiceState 行；不存在时返回零值行（未持久化）。
func (s *Service) loadVoiceState(guildID, userID uuid.UUID) (model.VoiceState, bool, error) {
	var vs model.VoiceState
	err := s.db.First(&vs, "guild_id = ? AND user_id = ?", guildID, userID).Error
	if err == gorm.ErrRecordNotFound {
		return model.VoiceState{ID: uuid.New(), GuildID: guildID, UserID: userID}, false, nil
	}
	return vs, err == nil, err
}

// userIsBot 查询用户的 IsBot 标记（Media Token bot claim 用，bot 专项）。
func (s *Service) userIsBot(userID uuid.UUID) bool {
	var isBot bool
	s.db.Model(&model.User{}).Select("is_bot").Where("id = ?", userID).Scan(&isBot)
	return isBot
}

// roomUsersByNode 统计某逻辑房在各节点上的用户数（sticky 打分与 anchor 选举用）。
func (s *Service) roomUsersByNode(roomID uuid.UUID) (map[uuid.UUID]int, error) {
	var rows []struct {
		NodeID uuid.UUID
		Count  int
	}
	err := s.db.Model(&model.VoiceState{}).
		Select("node_id, count(*) as count").
		Where("channel_id = ? AND node_id IS NOT NULL", roomID).
		Group("node_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		counts[row.NodeID] = row.Count
	}
	return counts, nil
}

// buildCandidates 组装某 guild + 逻辑房的调度候选（池内节点 + 房间上下文 + 预留）。
func (s *Service) buildCandidates(guildID, roomID uuid.UUID) ([]nodeCandidate, error) {
	nodes, err := sfuctl.Dir().PoolNodes(guildID)
	if err != nil {
		return nil, err
	}
	roomUsers, err := s.roomUsersByNode(roomID)
	if err != nil {
		return nil, err
	}
	var lease model.VoiceAnchorLease
	hasLease := s.db.First(&lease, "room_id = ?", roomID).Error == nil
	onTree := make(map[uuid.UUID]bool)
	if hasLease {
		onTree[lease.AnchorNodeID] = true
		var edges []model.VoiceCascadeEdge
		if err := s.db.Find(&edges, "room_id = ?", roomID).Error; err == nil {
			for _, e := range edges {
				onTree[e.ChildNodeID] = true
				onTree[e.ParentNodeID] = true
			}
		}
	}
	reserved := s.resv.ActiveByNode()
	candidates := make([]nodeCandidate, 0, len(nodes))
	for _, info := range nodes {
		candidates = append(candidates, nodeCandidate{
			Info:          info,
			SameRoomUsers: roomUsers[info.ID],
			OnTree:        onTree[info.ID],
			IsAnchor:      hasLease && lease.AnchorNodeID == info.ID,
			Reserved:      reserved[info.ID],
		})
	}
	return candidates, nil
}

// nodeInPool 级联/调度不出池校验（docs 08 §6.3）。
func (s *Service) nodeInPool(guildID, nodeID uuid.UUID) bool {
	nodes, err := sfuctl.Dir().PoolNodes(guildID)
	if err != nil {
		return false
	}
	for _, n := range nodes {
		if n.ID == nodeID {
			return true
		}
	}
	return false
}

// nodeIDFromPayload 从 Internal 节点事件载荷中尽力解析 node_id
// （sfunode 专项并行开发中，载荷形态未定，做宽容解析）。
func nodeIDFromPayload(payload any) (uuid.UUID, bool) {
	switch v := payload.(type) {
	case uuid.UUID:
		return v, true
	case string:
		id, err := uuid.Parse(v)
		return id, err == nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, false
	}
	var probe struct {
		NodeID string `json:"node_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return uuid.Nil, false
	}
	if probe.NodeID == "" {
		probe.NodeID = probe.ID
	}
	id, err := uuid.Parse(probe.NodeID)
	return id, err == nil
}

// handleBusEvent 订阅事件总线：caps 脏通知与节点故障/排空触发。
func (s *Service) handleBusEvent(event eventbus.Event) {
	switch event.Type {
	case eventbus.InternalCapsDirty:
		payload, ok := event.Payload.(eventbus.CapsDirtyPayload)
		if !ok {
			// 兼容跨进程序列化后的 map 形态。
			raw, err := json.Marshal(event.Payload)
			if err != nil {
				return
			}
			if json.Unmarshal(raw, &payload) != nil {
				return
			}
		}
		s.recomputeCapsDirty(payload)
	case eventbus.InternalNodeDown:
		if nodeID, ok := nodeIDFromPayload(event.Payload); ok {
			s.engine.migrateNode(nodeID, model.MigrationReasonDeath)
		}
	case eventbus.InternalNodeDraining:
		if nodeID, ok := nodeIDFromPayload(event.Payload); ok {
			s.engine.migrateNode(nodeID, model.MigrationReasonDrain)
			// Drain 60s 硬迁兜底（docs 09 I.6）：时限到仍挂在该节点的会话
			// 重新触发批量迁移（幂等：活跃 job 会被合并，仅补漏网会话）。
			// 节点期间被 undrain / 下线则放弃兜底，避免误迁正常会话。
			time.AfterFunc(drainHardMigrateAfter, func() {
				info, err := sfuctl.Dir().Node(nodeID)
				if err != nil || !info.Draining {
					return
				}
				var remaining int64
				s.db.Model(&model.VoiceState{}).
					Where("node_id = ? AND channel_id IS NOT NULL", nodeID).Count(&remaining)
				if remaining > 0 {
					log.Printf("voice: 节点 %s Drain %s 后仍有 %d 个会话，硬迁兜底重触发",
						nodeID, drainHardMigrateAfter, remaining)
					s.engine.migrateNode(nodeID, model.MigrationReasonDrain)
				}
			})
		}
	case eventbus.InternalEdgeDown:
		payload, ok := event.Payload.(eventbus.EdgeDownPayload)
		if !ok {
			raw, err := json.Marshal(event.Payload)
			if err != nil {
				return
			}
			if json.Unmarshal(raw, &payload) != nil {
				return
			}
		}
		s.handleEdgeDown(payload)
	}
}
