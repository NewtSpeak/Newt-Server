package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/adminpresence"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/auditapi"
	"github.com/owlspeak/owl-server/backend/internal/botapi"
	"github.com/owlspeak/owl-server/backend/internal/clientapi"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/customization"
	"github.com/owlspeak/owl-server/backend/internal/publicinvite"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/gateway"
	"github.com/owlspeak/owl-server/backend/internal/guildapi"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/message"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/moderation"
	"github.com/owlspeak/owl-server/backend/internal/observability"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/platformadmin"
	"github.com/owlspeak/owl-server/backend/internal/presence"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/registrationinvite"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"github.com/owlspeak/owl-server/backend/internal/secretstore"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"github.com/owlspeak/owl-server/backend/internal/sfubridge"
	"github.com/owlspeak/owl-server/backend/internal/sfunode"
	"github.com/owlspeak/owl-server/backend/internal/social"
	"github.com/owlspeak/owl-server/backend/internal/stage"
	"github.com/owlspeak/owl-server/backend/internal/sticker"
	"github.com/owlspeak/owl-server/backend/internal/userapi"
	"github.com/owlspeak/owl-server/backend/internal/voice"
	"github.com/owlspeak/owl-server/backend/internal/web"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"
)

// New 装配 HTTP 服务。
//
// bus 可为 nil（此时内部新建）；main 传入与 sfucontrol 共享的总线，
// 使节点判死（InternalNodeDown）能到达 voice 迁移引擎。
// sfu 可选：传入时 httpapi 挂载 SFU 节点管理路由的真实依赖（注册表 + Media Token 签发器），
// 并把 sfuctl.Directory/Controller 桥接到 gRPC 控制面（internal/sfubridge）；
// 不传时相关路由返回 503、sfuctl 保持 no-op（纯单测场景）。
func New(cfg config.Config, db *gorm.DB, bus *eventbus.Bus, sfu ...httpapi.SFUOptions) (*gin.Engine, error) {
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	for _, mw := range observability.GinMiddleware() {
		router.Use(mw)
	}
	// CORS 分平面：/gapi、/invite-api 面向桌面客户端（Tauri）跨域直连自部署服务端，
	// 来源不可枚举（tauri://localhost / http://localhost:* / https://tauri.localhost 等），
	// 放开任意 Origin（凭证走 Bearer header 而非 Cookie，无 CSRF 面）；
	// 其余前缀（含 /api/v1 管理后台，生产同源）维持仅本地开发来源的收紧配置。
	adminCORS := cors.New(cors.Config{
		AllowOrigins: []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Authorization", "Content-Type"},
	})
	clientCORS := cors.New(cors.Config{
		AllowAllOrigins: true,
		AllowMethods:    []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:    []string{"Authorization", "Content-Type"},
	})
	router.Use(gin.Logger(), gin.Recovery(), func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/gapi/") || strings.HasPrefix(path, "/invite-api/") {
			clientCORS(c)
			return
		}
		adminCORS(c)
	})
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	// 服务器时间（Owl-Desktop docs 08 §8-9：客户端显示 Restriction 剩余时长时
	// 校准本地时钟偏差）。免认证、双前缀可达（CORS 中间件按前缀已放行）。
	serverTime := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"server_time": time.Now().UTC(), "unix_ms": time.Now().UTC().UnixMilli()})
	}
	router.GET("/api/v1/time", serverTime)
	router.GET("/gapi/v1/time", serverTime)
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)

	// SFU 底座桥接（收敛决议）：sfuctl 稳定接口的真实实现改为 internal/sfubridge
	//（节点目录 = SfuNode 表 + gRPC 注册表快照；指令经 gRPC Command 下发），
	// 取代 internal/sfunode 的 WSS 控制通道实现（与真实 Owl-SFU 协议不兼容，已停止监听）。
	var mediaTokens *mediatoken.Manager
	if len(sfu) > 0 {
		api.AttachSFU(sfu[0])
		mediaTokens = sfu[0].MediaTokens
		if sfu[0].Registry != nil {
			sfubridge.Install(db, sfu[0].Registry)
		}
	}
	// Media Token 签发器全局唯一（密钥存 ClusterSecret 表）；未经 SFUOptions
	// 传入时直接从库加载同一份密钥，保证签发与 SFU 验签公钥恒同源。
	if mediaTokens == nil {
		var err error
		mediaTokens, err = mediatoken.Load(secretstore.GormStore{DB: db}, cfg.MediaTokenTTL)
		if err != nil {
			return nil, err
		}
	}

	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)

	// 领域模块装配：Restriction 需最先注入（perms 依赖其收紧钩子），
	// sfunode 其次（enrollment/节点池 REST），其余模块随后。
	if bus == nil {
		bus = eventbus.New()
	}
	// RBAC / 频道等后台变更端点发布 Gateway 事件（GUILD_ROLE_* / CHANNEL_CREATE /
	// PERMISSIONS_UPDATE / GUILD_MEMBER_UPDATE 等，docs 14 §3.2）。
	api.AttachEventBus(bus)
	// Presence 注册表全局唯一：后台 / 用户端 / bot 三个 Gateway 平面共享，
	// 同一用户跨平面的连接自然参与多端合并（docs 01 §3.4）。
	presenceManager := presence.NewDBManager(db, bus)
	deps := appdeps.Deps{DB: db, Bus: bus, Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser, MediaTokens: mediaTokens, Presence: presenceManager}
	perms.RestrictionMask = func(db *gorm.DB, bits rbac.Permission, userID, guildID uuid.UUID, channel *model.Channel) rbac.Permission {
		return restriction.Mask(bits, userID, guildID, channel)
	}
	modules := []func(*gin.RouterGroup, appdeps.Deps) error{
		restriction.Register,
		sfunode.Register,
		voice.Register,
		stage.Register,
		moderation.Register,
		// 服务器结构管理（角色/频道/覆盖/guild 生命周期）：从 httpapi 抽出的共享
		// handler，后台平面在此挂载，用户端平面由 clientapi.Register 投影。
		guildapi.Register,
		// 用户账号自助端点（资料/头像/密码/会话/设置，docs 01/16）：双平面共享 handler，
		// 后台平面在此挂载，用户端平面由 clientapi.Register 投影。
		userapi.Register,
		// 社交层（隐私/好友/通知收件箱，Server-16）；DM 频道后续补齐。
		social.Register,
		message.Register,
		sticker.Register, // 贴图与表情包（docs 17）
		gateway.Register,
		auditapi.Register,
		// AI 时代扩展功能（后台管理 API 部分）：
		botapi.Register,          // 机器人集成：注册/token/权限/流式/卡片
		customization.Register,   // 角色名样式、徽章、头像横幅（后台管理）
		adminpresence.Register,   // 系统管理员临场（进频道发言/隐身/音频审计）
		publicinvite.RegisterAdmin, // 邀请落地页内容管理（公告/协议）后台
		platformadmin.Register,   // 平台用户治理（禁用/重置密码/系统管理员/注册开关）
		registrationinvite.Register, // 注册邀请链接（凭码绕过注册开关，仅系统管理员）
	}
	for _, register := range modules {
		if err := register(v1, deps); err != nil {
			return nil, err
		}
	}

	// 用户端 API（/gapi/v1）：与后台管理 API 完全隔离的独立前缀与认证体系。
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		return nil, err
	}

	// 公开邀请落地页 API（/invite-api）：无需登录，供未安装客户端的用户查看
	// 服务器信息/公告/协议与下载引导；与 /api、/gapi 前缀均隔离。
	inviteAPI := router.Group("/invite-api")
	if err := publicinvite.RegisterPublic(inviteAPI, deps); err != nil {
		return nil, err
	}
	// 注册邀请公开预检（GET /invite-api/registration/{code}）：桌面客户端注册前免登录校验。
	if err := registrationinvite.RegisterPublic(inviteAPI, deps); err != nil {
		return nil, err
	}
	// 友好分享短链 /invite/{code}：服务端渲染 HTML 落地页（含深链唤起与下载引导）。
	publicinvite.RegisterLanding(router, deps)

	// 头像/横幅公开访问（/public-assets）：无需登录，文件名带版本号可长缓存。
	publicAssets := router.Group("/public-assets")
	if err := customization.RegisterPublic(publicAssets, deps); err != nil {
		return nil, err
	}
	// 入场语音包音频公开访问（/public-assets/voicepacks，docs 12 §5.1 客户端直拉）。
	if err := message.RegisterPublicAssets(publicAssets, deps); err != nil {
		return nil, err
	}
	// 贴图资产公开访问（/public-assets/stickers，docs 17 A1 内容 hash 文件名）。
	if err := sticker.RegisterPublic(publicAssets, deps); err != nil {
		return nil, err
	}

	// 机器人开放 API（/bot-api）：供各语言 SDK 调用，bot token 鉴权，独立前缀。
	if err := botapi.RegisterBotAPI(router.Group("/bot-api"), deps); err != nil {
		return nil, err
	}

	// 审计录音上传 API（/audit-api）：SFU 节点凭共享密钥（AUDIT_INGEST_TOKEN）
	// 把被审计频道的上行音频录制上传到主节点服务器；与其他前缀隔离。
	if err := adminpresence.RegisterIngest(router.Group("/audit-api"), deps); err != nil {
		return nil, err
	}

	if err := web.RegisterFallback(router, cfg.Environment, cfg.FrontendDevURL); err != nil {
		return nil, err
	}
	return router, nil
}
