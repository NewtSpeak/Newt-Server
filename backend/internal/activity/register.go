package activity

// 装配：
//   Register        后台平面 /api/v1（admin CRUD + 用户端同源读接口，便于调试）
//   RegisterClient  用户端平面 /gapi/v1
// service 为包级单例（多平面重复 Register 只装配一次），一次性副作用包括
// gateway IDENTIFY 钩子注入与 flush/采样/结算三个后台 goroutine。

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

var sharedService *service

// serviceInitCount 实际构造次数，仅供单测验证单例语义。
var serviceInitCount int

func ensureService(deps appdeps.Deps) *service {
	if sharedService != nil {
		return sharedService
	}
	svc := &service{db: deps.DB, bus: deps.Bus}
	svc.tracker = newTracker(svc)
	svc.loadConfig()

	// 注：gateway.OnIdentify（"当日登录"信号）在装配层（clientapi.Register）桥接，
	// activity 不直接 import gateway——否则经 message→activity→gateway→social→message 成环。

	sharedService = svc
	serviceInitCount++

	go svc.tracker.flushLoop(flushInterval)
	go svc.samplerLoop(time.Minute)
	go svc.settleLoop()
	return svc
}

func mountUser(group *gin.RouterGroup, svc *service, deps appdeps.Deps) {
	h := &userHandlers{svc: svc, currentUser: deps.CurrentUser}
	me := group.Group("/users/@me", deps.Auth)
	me.GET("/activity", h.myActivity)
}

// Register 后台平面：admin 配置/统计/结算 + 用户端同源读接口。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps)
	h := &adminHandlers{svc: svc}
	admin := v1.Group("/admin/activity", deps.Auth, svc.requireSystemAdmin(deps.CurrentUser))
	admin.GET("/config", h.getConfig)
	admin.PUT("/config", h.putConfig)
	admin.GET("/stats", h.getStats)
	admin.GET("/users/:userID", h.getUserDetail)
	admin.POST("/settle", h.triggerSettle)
	mountUser(v1, svc, deps)
	return nil
}

// RegisterClient 用户端平面 /gapi/v1。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps)
	mountUser(root, svc, deps)
	return nil
}
