// Package voice 语音会话编排：进房/离房/切换、Media Token（Ed25519）、调度器（docs 10）、
// 级联编排（docs 08）与热迁移状态机（docs 09）。
package voice

import (
	"fmt"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

// tryRegister 容错注册路由：并行开发期间其他模块可能已抢注同一路径（gin 对重复注册会 panic），
// 此时记录警告并跳过，保证服务可启动；集成阶段应收敛为单一归属。
func tryRegister(register func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("voice: 路由注册冲突，已跳过（其他模块已注册同一路径）: %v", r)
		}
	}()
	register()
}

// ---------------------------------------------------------------------------
// Service 单例（双前缀共用）
// ---------------------------------------------------------------------------

// sharedService 包级单例。Service 含迁移引擎后台 goroutine 与事件总线订阅，
// 全进程必须只初始化一次，否则会出现重复迁移调度与重复事件消费。
//
// 装配顺序假设：server.New 先调用后台 Register（/api/v1）触发构造，
// 随后 clientapi.Register 经 RegisterClient（/gapi/v1）复用同一实例。
// 即便顺序颠倒，ensureService 也会在首个调用方处完成构造，单例语义不变。
// 路由注册均发生在 server 装配阶段的单 goroutine 内，无需加锁。
var sharedService *Service

// serviceInitCount Service 实际构造（含总线订阅与迁移引擎启动）的次数，
// 仅用于单元测试验证单例语义，业务代码不得依赖。
var serviceInitCount int

// ensureService 返回全局唯一的 Service：首次调用完成构造、订阅事件总线并启动
// 迁移引擎；后续调用（无论来自哪个前缀）直接复用，忽略 deps 中的认证平面差异
// （Service 只消费 DB/Bus/Cfg/MediaTokens 这些全局共享依赖）。
func ensureService(deps appdeps.Deps) (*Service, error) {
	if sharedService != nil {
		return sharedService, nil
	}
	// Media Token 统一由 internal/mediatoken 签发（密钥存 ClusterSecret 表，
	// 与 SFU enroll/RegisterAck 下发的验签公钥同源）；不再使用 voice 自建 signer。
	if deps.MediaTokens == nil {
		return nil, fmt.Errorf("voice 模块需要 Media Token 签发器（deps.MediaTokens 未装配）")
	}
	sched := defaultSchedConfig()
	// 过载自动迁移（docs 09 I.3：默认关，部署配置可开）；开关经既有配置口
	// OverloadAutoMigrate 生效，阈值/批量/冷却见 overloadConfigFromEnv。
	overloadCfg := overloadConfigFromEnv(deps.Cfg)
	sched.OverloadAutoMigrate = overloadCfg.Enabled
	svc := &Service{
		db:            deps.DB,
		bus:           deps.Bus,
		cfg:           deps.Cfg,
		tokens:        deps.MediaTokens,
		rtt:           newRTTStore(sched),
		resv:          newReservationStore(),
		sched:         sched,
		overload:      newOverloadDetector(overloadCfg),
		overloadNodes: func() ([]sfuctl.NodeInfo, error) { return sfuctl.Dir().AllNodes() },
		edgeFlaps:     newEdgeFlapTracker(),
	}
	svc.engine = newMigrationEngine(svc)

	// 订阅 caps 脏通知与节点故障/排空/级联边断事件（docs 09 §3、专项约定）、
	// 启动迁移引擎与租约续约循环：属于「全局一次」的副作用，
	// 必须与构造绑定在同一处，防止双前缀装配时重复执行。
	deps.Bus.Subscribe(svc.handleBusEvent)
	svc.engine.start()
	go svc.leaseMaintenanceLoop()
	if overloadCfg.Enabled {
		go svc.overloadLoop()
	}

	sharedService = svc
	serviceInitCount++
	return svc, nil
}

// ---------------------------------------------------------------------------
// 当前用户注入（认证平面适配）
// ---------------------------------------------------------------------------

// currentUserContextKey voice 私有上下文键：由 injectCurrentUser 写入，
// 使同一套 handler 无差别服务后台（/api/v1，aud=admin）与用户端（/gapi/v1，aud=client）。
const currentUserContextKey = "voice.current_user"

// injectCurrentUser 把某认证平面的「读当前用户」函数适配为中间件：在认证中间件之后
// 提前读取用户并写入 voice 私有键。Service 为单例、不能按平面各持一份 CurrentUser，
// 平面差异全部收敛到这里。
func injectCurrentUser(read func(*gin.Context) model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(currentUserContextKey, read(c))
		c.Next()
	}
}

// currentUser 读取 injectCurrentUser 注入的当前登录用户（handler 内调用）。
func (s *Service) currentUser(c *gin.Context) model.User {
	return c.MustGet(currentUserContextKey).(model.User)
}

// ---------------------------------------------------------------------------
// 后台前缀挂载（/api/v1）
// ---------------------------------------------------------------------------

// Register 挂载语音相关 REST API（后台管理平面，aud=admin），并确保单例构造。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc, err := ensureService(deps)
	if err != nil {
		return err
	}

	// 公钥端点无需登录（SFU / 调试用）。
	tryRegister(func() { v1.GET("/voice/public-key", svc.handlePublicKey) })

	authed := v1.Group("", deps.Auth, injectCurrentUser(deps.CurrentUser))
	tryRegister(func() { authed.POST("/voice/join", svc.handleJoin) })
	tryRegister(func() { authed.POST("/voice/leave", svc.handleLeave) })
	tryRegister(func() { authed.POST("/voice/refresh-token", svc.handleRefreshToken) })
	tryRegister(func() { authed.PATCH("/voice/state", svc.handleSelfState) })
	tryRegister(func() { authed.POST("/voice/rtt", svc.handleRTTReport) })
	tryRegister(func() { authed.POST("/voice/ice-failed", svc.handleIceFailed) })
	// ICE 失败上报（docs 13 FR-16 / 15 BI.2）：双信号提前判死的独立信号源。
	tryRegister(func() { authed.POST("/voice/ice-failure", svc.handleICEFailure) })
	tryRegister(func() { authed.POST("/voice/migrations/:migrationID/ack", svc.handleMigrationAck) })
	// 候选节点池下发（docs 13 §7.1）：客户端后台 RTT 探测用，成员即可读。
	tryRegister(func() { authed.GET("/guilds/:guildID/voice/nodes", svc.handleListVoiceNodes) })
	tryRegister(func() { authed.POST("/guilds/:guildID/voice/disconnect", svc.handleAdminDisconnect) })
	// 管理员移动成员到另一语音频道（docs 09 FR-29：MOVE_MEMBERS + 层级）。
	tryRegister(func() { authed.POST("/guilds/:guildID/voice/move", svc.handleAdminMove) })
	tryRegister(func() { authed.PATCH("/guilds/:guildID/voice/states/:userID", svc.handleServerState) })
	tryRegister(func() { authed.GET("/guilds/:guildID/channels/:channelID/voice-states", svc.handleListVoiceStates) })
	tryRegister(func() { authed.POST("/admin/voice/migrations", svc.handleManualMigration) })
	return nil
}
