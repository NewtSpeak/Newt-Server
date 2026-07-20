package voice

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

// maxQuickRetries 换目标快速重试上限（docs 09 K.3），之后转入排队重试。
const maxQuickRetries = 3

// migrationTimeouts 各阶段超时（docs 09 §5.2 K.2，可配；单测可注入缩短值）。
type migrationTimeouts struct {
	Prepare time.Duration
	Connect time.Duration
	Cutover time.Duration
	Cleanup time.Duration
}

func defaultMigrationTimeouts() migrationTimeouts {
	return migrationTimeouts{
		Prepare: 2 * time.Second,
		Connect: 8 * time.Second,
		Cutover: 2 * time.Second,
		Cleanup: 3 * time.Second,
	}
}

// migrationPriority 死亡 > 分区 > Drain > 过载（docs 09 K.4）；手动介于 Drain 与过载之上。
func migrationPriority(reason string) int {
	switch reason {
	case model.MigrationReasonDeath:
		return 40
	case model.MigrationReasonPartition:
		return 30
	case model.MigrationReasonDrain:
		return 20
	case model.MigrationReasonManual:
		return 15
	default: // OVERLOAD
		return 10
	}
}

// migrationEvent 状态机事件。
type migrationEvent string

const (
	evStart          migrationEvent = "start"
	evPrepared       migrationEvent = "prepared"
	evConnectAck     migrationEvent = "connect_ack"
	evConnectTimeout migrationEvent = "connect_timeout"
	evCutoverDone    migrationEvent = "cutover_done"
	evCleanupDone    migrationEvent = "cleanup_done"
	evFail           migrationEvent = "fail"
	evCancel         migrationEvent = "cancel"
	evRetry          migrationEvent = "retry"
)

// nextMigrationState 五段状态机转换表（docs 09 §5.1）。纯函数，供引擎与单测共用。
// 返回 (新状态, 是否合法转换)。
func nextMigrationState(state string, ev migrationEvent) (string, bool) {
	if ev == evCancel {
		switch state {
		case model.MigrationStateQueued, model.MigrationStatePrepare,
			model.MigrationStateConnect, model.MigrationStateCutover, model.MigrationStateFailed:
			return model.MigrationStateCanceled, true
		}
		return state, false
	}
	switch state {
	case model.MigrationStateQueued:
		if ev == evStart {
			return model.MigrationStatePrepare, true
		}
	case model.MigrationStatePrepare:
		switch ev {
		case evPrepared:
			return model.MigrationStateConnect, true
		case evFail:
			return model.MigrationStateFailed, true
		}
	case model.MigrationStateConnect:
		switch ev {
		case evConnectAck, evConnectTimeout:
			// 客户端确认或超时自动推进（docs 09 M.4：客户端无响应也不阻塞）。
			return model.MigrationStateCutover, true
		case evFail:
			return model.MigrationStateFailed, true
		}
	case model.MigrationStateCutover:
		switch ev {
		case evCutoverDone:
			return model.MigrationStateCleanup, true
		case evFail:
			return model.MigrationStateFailed, true
		}
	case model.MigrationStateCleanup:
		switch ev {
		case evCleanupDone:
			return model.MigrationStateDone, true
		case evFail:
			return model.MigrationStateFailed, true
		}
	case model.MigrationStateFailed:
		if ev == evRetry {
			return model.MigrationStateQueued, true
		}
	}
	return state, false
}

// isTerminalMigrationState 终态判断。
func isTerminalMigrationState(state string) bool {
	return state == model.MigrationStateDone || state == model.MigrationStateCanceled
}

// retryBackoff 快速重试次数耗尽后的排队重试退避（docs 09 §10：排队再试）。
func retryBackoff(attempt int) time.Duration {
	if attempt < maxQuickRetries {
		return 0
	}
	backoff := 5 * time.Second << uint(min(attempt-maxQuickRetries, 5))
	if backoff > 2*time.Minute {
		backoff = 2 * time.Minute
	}
	return backoff
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// 引擎
// ---------------------------------------------------------------------------

type migrationEngine struct {
	svc      *Service
	wake     chan struct{}
	timeouts migrationTimeouts
}

func newMigrationEngine(svc *Service) *migrationEngine {
	return &migrationEngine{svc: svc, wake: make(chan struct{}, 1), timeouts: defaultMigrationTimeouts()}
}

// start 启动后台循环；重启后把上一进程遗留的进行中 job 归位重跑（幂等指令保证安全）。
func (e *migrationEngine) start() {
	e.svc.db.Model(&model.VoiceMigrationJob{}).
		Where("state IN ?", []string{model.MigrationStatePrepare, model.MigrationStateConnect, model.MigrationStateCutover, model.MigrationStateCleanup}).
		Updates(map[string]any{"state": model.MigrationStateQueued, "state_deadline": nil})
	go e.loop()
}

func (e *migrationEngine) kick() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *migrationEngine) loop() {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-e.wake:
		}
		e.tick()
	}
}

// tick 推进所有活跃 job：高优先级先行（docs 09 K.4）。
func (e *migrationEngine) tick() {
	var jobs []model.VoiceMigrationJob
	err := e.svc.db.
		Where("state IN ?", []string{
			model.MigrationStateQueued, model.MigrationStatePrepare, model.MigrationStateConnect,
			model.MigrationStateCutover, model.MigrationStateCleanup, model.MigrationStateFailed,
		}).
		Order("priority DESC, created_at ASC").Find(&jobs).Error
	if err != nil {
		return
	}
	for _, job := range jobs {
		e.step(job)
	}
}

func (e *migrationEngine) step(job model.VoiceMigrationJob) {
	now := time.Now()
	switch job.State {
	case model.MigrationStateQueued:
		if job.RetryAt != nil && now.Before(*job.RetryAt) {
			return
		}
		e.runPrepare(job)
	case model.MigrationStatePrepare:
		// PREPARE 为同步执行，出现该状态说明上次进程中断，超时归位。
		if job.StateDeadline != nil && now.After(*job.StateDeadline) {
			e.fail(job, "PREPARE 阶段超时")
		}
	case model.MigrationStateConnect:
		if job.StateDeadline != nil && now.After(*job.StateDeadline) {
			// 超时自动推进：目标节点已就绪，客户端稍后经 Gateway 对齐（docs 09 M.4）。
			e.advance(job, evConnectTimeout)
		}
	case model.MigrationStateCutover:
		e.runCutover(job)
	case model.MigrationStateCleanup:
		e.runCleanup(job)
	case model.MigrationStateFailed:
		if job.RetryAt == nil || !now.Before(*job.RetryAt) {
			e.advance(job, evRetry)
		}
	}
}

// advance 应用一次状态机转换并落库。
func (e *migrationEngine) advance(job model.VoiceMigrationJob, ev migrationEvent) {
	next, ok := nextMigrationState(job.State, ev)
	if !ok {
		return
	}
	updates := map[string]any{"state": next, "state_deadline": nil}
	now := time.Now()
	switch next {
	case model.MigrationStateCutover:
		deadline := now.Add(e.timeouts.Cutover)
		updates["state_deadline"] = &deadline
		updates["cutover_at"] = &now
		if ev == evConnectAck {
			// 客户端确认已连上新节点（审计 O.1：CONNECT 完成时间戳）。
			updates["connected_at"] = &now
		}
	case model.MigrationStateCleanup:
		deadline := now.Add(e.timeouts.Cleanup)
		updates["state_deadline"] = &deadline
	case model.MigrationStateDone, model.MigrationStateCanceled:
		updates["completed_at"] = &now
	}
	e.svc.db.Model(&model.VoiceMigrationJob{}).Where("id = ?", job.ID).Updates(updates)
	e.kick()
}

// runPrepare PREPARE：校验会话仍有效 → 选目标（换目标重试）→ 占容量 → EnsureRoom → 挂边
// → 签发新 token → 发 VOICE_MIGRATING + VOICE_SERVER_UPDATE → 进入 CONNECT（docs 09 §6.1）。
func (e *migrationEngine) runPrepare(job model.VoiceMigrationJob) {
	s := e.svc
	s.mu.Lock()
	defer s.mu.Unlock()

	// 会话有效性：用户已离开 / 已切频道 / 已不在源节点 → 取消 job（docs 09 §10）。
	vs, exists, err := s.loadVoiceState(job.GuildID, job.UserID)
	if err != nil || !exists || vs.ChannelID == nil || *vs.ChannelID != job.ChannelID ||
		vs.NodeID == nil || *vs.NodeID != job.FromNodeID {
		e.applyEvent(job.ID, evCancel)
		return
	}

	now := time.Now()
	deadline := now.Add(e.timeouts.Prepare)
	// 快照迁出侧旧会话 sid：CLEANUP 阶段按 sid 精确摘除（15 BJ.2/BJ.3）。
	// 仅首次尝试快照——重试时 VoiceState.voice_session_id 已被上次尝试的新 sid
	// 覆盖，源节点上的旧会话仍持有首次快照的 sid。
	fromSessionID := job.FromSessionID
	if fromSessionID == nil {
		fromSessionID = vs.VoiceSessionID
	}
	s.db.Model(&model.VoiceMigrationJob{}).Where("id = ?", job.ID).Updates(map[string]any{
		"state": model.MigrationStatePrepare, "state_deadline": &deadline, "attempt": job.Attempt + 1,
		"from_session_id": fromSessionID,
	})
	job.Attempt++

	target, err := e.pickTarget(job)
	if err != nil {
		e.fail(job, err.Error())
		return
	}
	resvID := s.resv.Reserve(target, s.sched.ReservationTTL)
	if err := sfuctl.Ctl().EnsureRoom(target, job.ChannelID); err != nil {
		s.resv.Release(resvID)
		e.fail(job, "EnsureRoom 失败: "+err.Error())
		return
	}
	if err := s.ensureCascade(job.GuildID, job.ChannelID, target); err != nil {
		s.resv.Release(resvID)
		e.fail(job, "级联挂边失败: "+err.Error())
		return
	}

	// 重算 caps 并签发绑定新节点的 token。
	caps, err := s.capsFor(job.GuildID, job.ChannelID, job.UserID, vs.ServerMute)
	if err != nil {
		s.resv.Release(resvID)
		e.fail(job, "重算 caps 失败: "+err.Error())
		return
	}
	// 热迁移 CONNECT 阶段同一用户在新旧节点各持一会话：新 token 必须带新 sid
	//（15 BJ.2；SFU 会话键 = sid，互不冲突）。CLEANUP 阶段把新 sid 落回 VoiceState。
	sessionID := uuid.New()
	token, expiresAt, err := s.tokens.Sign(mediatoken.Claims{
		UID: job.UserID.String(), GID: job.GuildID.String(), CID: job.ChannelID.String(),
		NID: target.String(), RID: job.ChannelID.String(), SID: sessionID.String(),
		Caps: caps, Bot: s.userIsBot(job.UserID),
		Hidden: StealthPredicate(job.GuildID, job.UserID),
		Audit:  AuditPredicate(job.GuildID, job.ChannelID),
	})
	if err != nil {
		s.resv.Release(resvID)
		e.fail(job, "签发 Media Token 失败: "+err.Error())
		return
	}
	info, err := sfuctl.Dir().Node(target)
	if err != nil {
		s.resv.Release(resvID)
		e.fail(job, "目标节点不可用: "+err.Error())
		return
	}

	tried := job.TriedNodes
	if tried != "" {
		tried += ","
	}
	tried += target.String()
	connectDeadline := time.Now().Add(e.timeouts.Connect)
	preparedAt := time.Now()
	s.db.Model(&model.VoiceMigrationJob{}).Where("id = ?", job.ID).Updates(map[string]any{
		"to_node_id": target, "tried_nodes": tried, "prepared_at": &preparedAt,
		"state": model.MigrationStateConnect, "state_deadline": &connectDeadline,
	})
	// 新会话 sid 记入 VoiceState（CLEANUP 幂等再确认）；旧节点会话仍以旧 sid 存活到 CLEANUP。
	s.db.Model(&model.VoiceState{}).Where("id = ?", vs.ID).Update("voice_session_id", sessionID)

	// MARK：告知源节点该 sid 处于迁出中（CUTOVER 前继续服务，协议 MigrateParticipants.Phase）。
	// 源节点死亡（DEATH 触发）时必然失败，忽略——EXECUTE 同样兜底。
	if fromSessionID != nil {
		_ = sfuctl.Ctl().MigrateParticipants(job.FromNodeID, job.ChannelID, job.ID,
			[]string{fromSessionID.String()}, target, sfuctl.MigratePhaseMark)
	}

	migrationID := job.ID
	// 客户端协议：先 VOICE_MIGRATING（进入切换态、驱动双 PC），再推新 token（docs 09 §7）。
	s.publishToUser(eventbus.EventVoiceMigrating, job.UserID, job.GuildID, voiceMigratingPayload{
		MigrationID: migrationID, GuildID: job.GuildID, ChannelID: job.ChannelID,
		Message: "线路优化中，语音将自动切换", // 默认模糊文案（docs 09 O.2）
	})
	s.publishToUser(eventbus.EventVoiceServerUpdate, job.UserID, job.GuildID, voiceServerUpdatePayload{
		GuildID: job.GuildID, ChannelID: job.ChannelID, NodeID: target, RoomID: job.ChannelID,
		SFUEndpoint: info.WebRTCEndpoint, Token: token, Caps: caps,
		SessionID: sessionID, ExpiresAt: expiresAt, MigrationID: &migrationID,
	})
}

// pickTarget 选目标节点：预置目标（BATCH/手动）优先，否则调度器打分；排除已尝试节点。
func (e *migrationEngine) pickTarget(job model.VoiceMigrationJob) (uuid.UUID, error) {
	s := e.svc
	tried := map[uuid.UUID]bool{}
	for _, part := range strings.Split(job.TriedNodes, ",") {
		if id, err := uuid.Parse(strings.TrimSpace(part)); err == nil {
			tried[id] = true
		}
	}
	mode := ModeMigrateLeaf
	if job.BatchKey != "" {
		mode = ModeMigrateBatch
	}
	// 预置目标（批量收敛 / 手动指定）：未尝试过且过硬过滤即用。
	if job.ToNodeID != nil && !tried[*job.ToNodeID] {
		candidates, err := s.buildCandidates(job.GuildID, job.ChannelID)
		if err == nil {
			from := job.FromNodeID
			for _, c := range candidates {
				if c.Info.ID == *job.ToNodeID && passHardFilter(c, scheduleParams{Mode: mode, FromNodeID: &from, Config: s.sched}) {
					return *job.ToNodeID, nil
				}
			}
		}
	}
	candidates, err := s.buildCandidates(job.GuildID, job.ChannelID)
	if err != nil {
		return uuid.Nil, err
	}
	filtered := candidates[:0]
	for _, c := range candidates {
		if !tried[c.Info.ID] {
			filtered = append(filtered, c)
		}
	}
	from := job.FromNodeID
	result, ok := schedule(filtered, scheduleParams{
		Mode: mode, UserID: job.UserID,
		RTTMs:      s.rtt.Samples(job.UserID, time.Now()),
		FromNodeID: &from, Config: s.sched,
	})
	if !ok {
		return uuid.Nil, fmt.Errorf("迁移无可用目标节点（池满，排队重试）")
	}
	return result.Primary, nil
}

// runCutover CUTOVER：发送侧由客户端切到新 PC，Server 侧仅推进状态（docs 09 §7.2）。
func (e *migrationEngine) runCutover(job model.VoiceMigrationJob) {
	e.advance(job, evCutoverDone)
}

// runCleanup CLEANUP：源节点摘人 → VoiceState 落新节点 → 级联收尾 → VOICE_MIGRATED
// → 发布 InternalCapsDirty 让舞台等模块重放 caps → 审计（docs 09 §6.1 步骤 6–7）。
func (e *migrationEngine) runCleanup(job model.VoiceMigrationJob) {
	s := e.svc
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.ToNodeID == nil {
		e.fail(job, "CLEANUP 缺少目标节点")
		return
	}
	vs, exists, err := s.loadVoiceState(job.GuildID, job.UserID)
	if err != nil || !exists || vs.ChannelID == nil || *vs.ChannelID != job.ChannelID {
		e.applyEvent(job.ID, evCancel)
		return
	}
	// 幂等：源侧按旧 sid 精确摘参与者（MigrateParticipants EXECUTE，closed 码
	// MIGRATED，绝不误伤新节点上的新会话；15 BJ.2/BJ.3）。源节点死亡时失败忽略。
	// 无旧 sid 快照（历史 job）时降级为按 uid 断开——新会话在目标节点，同样安全。
	if job.FromSessionID != nil {
		_ = sfuctl.Ctl().MigrateParticipants(job.FromNodeID, job.ChannelID, job.ID,
			[]string{job.FromSessionID.String()}, *job.ToNodeID, sfuctl.MigratePhaseExecute)
	} else {
		_ = sfuctl.Ctl().DisconnectUser(job.FromNodeID, job.ChannelID, job.UserID, "MIGRATED")
	}
	if err := s.db.Model(&model.VoiceState{}).Where("id = ?", vs.ID).
		Update("node_id", *job.ToNodeID).Error; err != nil {
		e.fail(job, "更新 VoiceState 失败: "+err.Error())
		return
	}
	// 源节点若因此空掉，摘边/换根/回收房间。
	s.cascadeAfterLeave(job.GuildID, job.ChannelID, job.FromNodeID)

	e.advance(job, evCleanupDone)

	s.publishToUser(eventbus.EventVoiceMigrated, job.UserID, job.GuildID, voiceMigratedPayload{
		MigrationID: job.ID, GuildID: job.GuildID, ChannelID: job.ChannelID, NodeID: *job.ToNodeID,
	})
	vs.NodeID = job.ToNodeID
	s.publishState(vs, "")
	// 迁移保留舞台状态由舞台模块负责；此处触发 caps 重放（专项约定）。
	s.bus.Publish(eventbus.Event{
		Type:    eventbus.InternalCapsDirty,
		GuildID: &job.GuildID,
		Payload: eventbus.CapsDirtyPayload{
			GuildID: job.GuildID.String(), ChannelID: job.ChannelID.String(),
			UserID: job.UserID.String(), Reason: "migration",
		},
	})
	audit.Log(s.db, audit.Entry{
		ActorID: job.ActorID, ActorType: nonEmpty(job.ActorType, "auto"), GuildID: &job.GuildID,
		Action: "voice.migration.completed", TargetType: "voice_migration", TargetID: job.ID.String(),
		Detail: map[string]any{
			"migration_id": job.ID.String(), "reason": job.Reason, "priority": job.Priority,
			"user_id": job.UserID.String(), "channel_id": job.ChannelID.String(),
			"from_node_id": job.FromNodeID.String(), "to_node_id": job.ToNodeID.String(),
			"attempt": job.Attempt,
		},
	})
}

// fail 记录失败并进入 FAILED；快速重试换目标，超过 3 次转排队重试（docs 09 K.3）。
func (e *migrationEngine) fail(job model.VoiceMigrationJob, reason string) {
	log.Printf("voice: 迁移 %s 失败（attempt=%d）: %s", job.ID, job.Attempt, reason)
	updates := map[string]any{
		"state": model.MigrationStateFailed, "state_deadline": nil,
		"last_error": reason, "to_node_id": nil, // 下次换目标
	}
	if backoff := retryBackoff(job.Attempt); backoff > 0 {
		retryAt := time.Now().Add(backoff)
		updates["retry_at"] = &retryAt
	} else {
		updates["retry_at"] = nil
	}
	e.svc.db.Model(&model.VoiceMigrationJob{}).Where("id = ?", job.ID).Updates(updates)
	audit.Log(e.svc.db, audit.Entry{
		ActorType: "auto", GuildID: &job.GuildID,
		Action: "voice.migration.failed", TargetType: "voice_migration", TargetID: job.ID.String(),
		Detail: map[string]any{"reason": job.Reason, "attempt": job.Attempt, "error": reason},
	})
}

// applyEvent 按状态机应用事件（用于 cancel 等来自外部的转换）。
func (e *migrationEngine) applyEvent(jobID uuid.UUID, ev migrationEvent) {
	var job model.VoiceMigrationJob
	if e.svc.db.First(&job, "id = ?", jobID).Error != nil {
		return
	}
	e.advance(job, ev)
}

// ---------------------------------------------------------------------------
// job 创建入口
// ---------------------------------------------------------------------------

// activeJob 返回某 user+guild 当前未终态 job。
func (e *migrationEngine) activeJob(guildID, userID uuid.UUID) (model.VoiceMigrationJob, bool) {
	var job model.VoiceMigrationJob
	err := e.svc.db.
		Where("guild_id = ? AND user_id = ? AND state NOT IN ?", guildID, userID,
			[]string{model.MigrationStateDone, model.MigrationStateCanceled}).
		First(&job).Error
	return job, err == nil
}

// createJob 创建迁移 job；同 user+guild 仅一个活跃 job，高优先级可抢占低优先级（docs 09 K.4/K.5）。
// 调用方需持有 svc.mu。
func (e *migrationEngine) createJob(job model.VoiceMigrationJob) (model.VoiceMigrationJob, error) {
	// 优先级必须在抢占比较之前确定（按 reason 派生，docs 09 K.4）。
	job.Priority = migrationPriority(job.Reason)
	if existing, ok := e.activeJob(job.GuildID, job.UserID); ok {
		if existing.Priority >= job.Priority {
			return existing, nil // 已有同级或更高优先级迁移在途，合并。
		}
		// 抢占：取消低优先级 job。
		e.applyEvent(existing.ID, evCancel)
	}
	job.ID = uuid.New()
	job.State = model.MigrationStateQueued
	if job.ActorType == "" {
		job.ActorType = "auto"
	}
	if err := e.svc.db.Create(&job).Error; err != nil {
		return job, err
	}
	audit.Log(e.svc.db, audit.Entry{
		ActorID: job.ActorID, ActorType: job.ActorType, GuildID: &job.GuildID,
		Action: "voice.migration.created", TargetType: "voice_migration", TargetID: job.ID.String(),
		Detail: map[string]any{
			"reason": job.Reason, "user_id": job.UserID.String(),
			"channel_id": job.ChannelID.String(), "from_node_id": job.FromNodeID.String(),
		},
	})
	e.kick()
	return job, nil
}

// cancelActive 用户主动 leave / 切频道时取消在途迁移（docs 09 §10）。调用方需持有 svc.mu。
func (e *migrationEngine) cancelActive(guildID, userID uuid.UUID) {
	if job, ok := e.activeJob(guildID, userID); ok {
		e.applyEvent(job.ID, evCancel)
	}
}

// ack 客户端确认已连上新节点，驱动 CONNECT → CUTOVER（docs 09 §7）。
func (e *migrationEngine) ack(migrationID, userID uuid.UUID) error {
	var job model.VoiceMigrationJob
	if e.svc.db.First(&job, "id = ?", migrationID).Error != nil || job.UserID != userID {
		return fmt.Errorf("迁移任务不存在")
	}
	if job.State != model.MigrationStateConnect {
		return fmt.Errorf("迁移任务当前不可确认（state=%s）", job.State)
	}
	e.advance(job, evConnectAck)
	return nil
}

// migrateNode 节点级触发（死亡 / Drain）：该节点全部会话批量迁出（docs 09 H.2、I.6）。
// mode=MIGRATE_BATCH：每（源节点, 房间）批先定 batch_target 再塞人（docs 10 U.2）。
func (e *migrationEngine) migrateNode(nodeID uuid.UUID, reason string) {
	s := e.svc
	s.mu.Lock()
	defer s.mu.Unlock()

	var states []model.VoiceState
	if err := s.db.Find(&states, "node_id = ? AND channel_id IS NOT NULL", nodeID).Error; err != nil {
		return
	}
	byRoom := make(map[uuid.UUID][]model.VoiceState)
	for _, vs := range states {
		byRoom[*vs.ChannelID] = append(byRoom[*vs.ChannelID], vs)
	}
	for roomID, members := range byRoom {
		guildID := members[0].GuildID
		// 源即 anchor：先走 08 §7.1 换根（冻图→新根→升 epoch→挂边→等关键
		// EdgeUp），再迁用户（docs 09 L.2）。
		var lease model.VoiceAnchorLease
		if s.db.First(&lease, "room_id = ?", roomID).Error == nil && lease.AnchorNodeID == nodeID {
			if err := s.reRoot(guildID, roomID, map[uuid.UUID]bool{nodeID: true}); err != nil {
				// 无存活候选（全房用户都在死节点上）：清掉指向死根的租约与边，
				// 让迁移 PREPARE 的 ensureCascade 以「首人进房」语义在目标节点
				// 重建 anchor——否则挂边会一直等死根的 EdgeUp 而空转。
				// 此时旧图不存在任何存活涉边节点，epoch 从 1 重新计数无冲突。
				log.Printf("voice: 房间 %s 换根失败: %v（清空旧图，迁移目标将成为新 anchor）", roomID, err)
				s.db.Delete(&model.VoiceCascadeEdge{}, "room_id = ?", roomID)
				s.db.Delete(&model.VoiceAnchorLease{}, "room_id = ?", roomID)
			}
		}
		// 批量先定 batch_target：以首个用户为代表打分（docs 10 §5.1）。
		batchKey := nodeID.String() + "@" + roomID.String()
		var batchTarget *uuid.UUID
		if candidates, err := s.buildCandidates(guildID, roomID); err == nil {
			from := nodeID
			if result, ok := schedule(candidates, scheduleParams{
				Mode: ModeMigrateBatch, UserID: members[0].UserID,
				FromNodeID: &from, Config: s.sched,
			}); ok {
				batchTarget = &result.Primary
			}
		}
		for _, vs := range members {
			_, err := e.createJob(model.VoiceMigrationJob{
				Reason: reason, UserID: vs.UserID, GuildID: vs.GuildID, ChannelID: roomID,
				FromNodeID: nodeID, ToNodeID: batchTarget, BatchKey: batchKey,
			})
			if err != nil {
				log.Printf("voice: 创建批量迁移失败 user=%s: %v", vs.UserID, err)
			}
		}
	}
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
