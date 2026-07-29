// Package appdeps 定义各领域模块 Register 时注入的公共依赖，避免模块间直接互相 import。
package appdeps

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/mediatoken"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/presence"
	"github.com/newtspeak/newt-server/backend/internal/sfucontrol"
	"gorm.io/gorm"
)

// Deps 由 server.New 装配后传入各模块的 Register(v1, deps)。
type Deps struct {
	DB  *gorm.DB
	Bus *eventbus.Bus
	Cfg config.Config
	// Auth 登录校验中间件（校验 Bearer access token 并注入当前用户）。
	Auth gin.HandlerFunc
	// CurrentUser 从上下文取当前登录用户（必须在 Auth 之后的 handler 中调用）。
	CurrentUser func(*gin.Context) model.User
	// MediaTokens 全局唯一 Media Token 签发器（docs 协议 §1；与 SFU enroll/RegisterAck
	// 下发的验签公钥同源，voice 模块签发 token 必须使用它）。
	MediaTokens *mediatoken.Manager
	// Presence 全局唯一在线状态注册表（内存实现，docs 01 §3.4）：
	// 双认证平面的 Gateway 共享同一实例，同一用户跨平面连接参与多端合并；
	// 为 nil 时 Gateway 不启用 presence（纯单测场景兼容）。
	Presence *presence.Manager
	// SFURegistry gRPC 控制面节点注册表（容量/在线/级联边状态）；管理台拓扑面板读取。
	// 为 nil 时拓扑接口返回空边集（单测或未装配 SFU 时）。
	SFURegistry *sfucontrol.Registry
}
