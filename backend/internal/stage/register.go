package stage

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// sharedService 包级单例。service 的装配包含包级权威裁决钩子赋值、事件总线订阅与
// 后台扫描 goroutine（申请过期、断线掉席、预留超时、动态降额），全进程必须只执行一次，
// 否则会出现重复订阅与双跑扫描。
//
// 装配顺序假设：server.New 先调用后台 Register（/api/v1）触发构造，随后 clientapi
// 经 RegisterClient（/gapi/v1）复用同一实例；顺序颠倒时 ensureService 也会在首个
// 调用方处完成构造，单例语义不变。路由注册均发生在装配阶段单 goroutine 内，无需加锁。
var sharedService *service

// serviceInitCount service 实际构造（含钩子赋值/订阅/后台扫描启动）的次数，
// 仅用于单元测试验证单例语义，业务代码不得依赖。
var serviceInitCount int

// ensureService 返回全局唯一的 service：首次调用完成构造并执行全部一次性装配副作用；
// 后续调用（无论来自哪个前缀）直接复用。service 只消费 DB/Bus 这些全局共享依赖，
// 认证平面差异（Auth/CurrentUser）由各前缀的挂载函数在 handlers 层适配。
func ensureService(deps appdeps.Deps) *service {
	if sharedService != nil {
		return sharedService
	}
	svc := &service{db: deps.DB, bus: deps.Bus}

	// ---- 舞台/屏幕共享维度的权威裁决钩子（供语音编排 caps 投影消费）----
	// FREE 且未容量禁说可发音频；STAGE 仅 SPEAKER（docs 11 AD.4）。
	CanPublishAudio = func(db *gorm.DB, userID, channelID uuid.UUID) bool {
		guildID, ok := svc.guildOfChannel(db, channelID)
		if !ok {
			return false
		}
		cfg := svc.channelConfig(db, guildID, channelID)
		if cfg.Mode == model.StageModeStage {
			return svc.isSpeaker(db, channelID, userID)
		}
		return !svc.isCapacityMuted(db, channelID, userID)
	}
	// STAGE 仅 SPEAKER 可发屏幕轨；FREE 恒 true（RBAC STREAM 由语音模块叠加，docs 14 AX）。
	CanPublishScreen = func(db *gorm.DB, userID, channelID uuid.UUID) bool {
		guildID, ok := svc.guildOfChannel(db, channelID)
		if !ok {
			return false
		}
		cfg := svc.channelConfig(db, guildID, channelID)
		if cfg.Mode == model.StageModeStage {
			return svc.isSpeaker(db, channelID, userID)
		}
		return true
	}
	// 审批占坑门控（docs 14 BC.1 步骤 2–3）：screen/start 成功（ScreenSlot 存在，
	// RESERVED/ACTIVE 均算）才允许签发 publish_screen；坑释放后 caps 重算自动收回。
	HasScreenSlot = func(db *gorm.DB, userID, channelID uuid.UUID) bool {
		var count int64
		db.Model(&model.ScreenSlot{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
		return count > 0
	}
	RoleOf = func(db *gorm.DB, userID, channelID uuid.UUID) string {
		guildID, ok := svc.guildOfChannel(db, channelID)
		if !ok {
			return RoleNone
		}
		return svc.roleOf(db, guildID, channelID, userID)
	}

	// ---- 进出房钩子（语音编排模块可直接调用，得到同步处理）----
	OnVoiceJoin = func(_ *gorm.DB, _ *eventbus.Bus, guildID, channelID, _ uuid.UUID) {
		svc.reconcileChannel(guildID, channelID)
	}
	OnVoiceLeave = func(_ *gorm.DB, _ *eventbus.Bus, guildID, channelID, _ uuid.UUID) {
		svc.reconcileChannel(guildID, channelID)
	}
	// ---- SFU 上报屏幕轨生效：RESERVED → ACTIVE（docs 14 BC.1）----
	OnScreenTrackActive = func(_ *gorm.DB, _ *eventbus.Bus, channelID, userID uuid.UUID) {
		svc.confirmScreenActive(channelID, userID)
	}

	// ---- 订阅 VOICE_STATE_UPDATE 兜底处理进出房（连接数以 VoiceState 表计数为准）----
	deps.Bus.Subscribe(func(event eventbus.Event) {
		if event.Type != eventbus.EventVoiceStateUpdate {
			return
		}
		// 跳过本包自己发布的增量事件，避免无意义的自触发（reconcile 本身幂等，此处仅省开销）。
		if _, mine := event.Payload.(stageVoiceStatePayload); mine {
			return
		}
		switch {
		case event.GuildID != nil && event.ChannelID != nil:
			svc.reconcileChannel(*event.GuildID, *event.ChannelID)
		case event.GuildID != nil:
			// 离房事件可能不带频道：兜底重算该服所有有舞台/语音痕迹的频道。
			for _, channelID := range svc.channelsWithState(*event.GuildID) {
				svc.reconcileChannel(*event.GuildID, channelID)
			}
		}
	})

	// ---- 后台扫描 ----
	go svc.backgroundLoop(10 * time.Second)

	sharedService = svc
	serviceInitCount++
	return svc
}

// Register 挂载舞台/屏幕共享 REST API（后台管理平面，aud=admin），并确保单例构造。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps)
	h := &handlers{svc: svc, currentUser: deps.CurrentUser}

	channels := v1.Group("/channels/:channelID", deps.Auth)
	channels.GET("/voice-stage", h.getVoiceStage)
	channels.PATCH("/voice-stage", h.patchVoiceStage)
	channels.GET("/stage/queue", h.getQueue)
	channels.DELETE("/stage/queue/:userID", h.removeFromQueue)
	channels.POST("/stage/apply", h.apply)
	channels.DELETE("/stage/apply", h.cancelApply)
	channels.POST("/stage/bring-up", h.bringUp)
	channels.POST("/stage/bring-down", h.bringDown)
	channels.POST("/stage/self-leave", h.selfLeave)
	channels.POST("/voice/screen/start", h.screenStart)
	channels.POST("/voice/screen/stop", h.screenStop)
	channels.POST("/voice/screen/stop-user", h.screenStopUser)
	v1.GET("/guilds/:guildID/screen-quota", deps.Auth, h.guildScreenQuota)
	admin := v1.Group("/admin", deps.Auth, h.requireSystemAdmin)
	admin.PATCH("/guilds/:guildID/screen-quota", h.adminGuildQuota)
	admin.PATCH("/screen-quota/settings", h.adminSettings)
	return nil
}

// channelsWithState 该服有语音/舞台/屏幕痕迹的频道集合（兜底重算范围）。
func (s *service) channelsWithState(guildID uuid.UUID) []uuid.UUID {
	set := map[uuid.UUID]bool{}
	collect := func(table any, column string) {
		var ids []uuid.UUID
		s.db.Model(table).Distinct(column).Where("guild_id = ? AND "+column+" IS NOT NULL", guildID).Pluck(column, &ids)
		for _, id := range ids {
			if id != uuid.Nil {
				set[id] = true
			}
		}
	}
	collect(&model.VoiceState{}, "channel_id")
	collect(&model.StageSpeaker{}, "channel_id")
	collect(&model.StageQueueEntry{}, "channel_id")
	collect(&model.StageCapacityMute{}, "channel_id")
	collect(&model.ScreenSlot{}, "channel_id")
	result := make([]uuid.UUID, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	return result
}
