// Package clientapi 用户端 API（前缀 /gapi/v1），与后台管理 API（/api/v1）完全隔离：
//   - 独立注册/登录端点，签发 aud=client 的 token；后台 token（aud=admin）不可互通；
//   - 端点命名与后台无关联，避免用户端流量推断后台地址（用户需求）；
//   - 用户端 Gateway WS 挂在本前缀下。
//
// 模块化约定：各频道领域（文本/语音/屏幕共享）在本包内各自的文件中实现
// RegisterText / RegisterVoice / RegisterScreen 并由 Register 统一调用，文件级隔离便于并行开发。
package clientapi

import (
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/activity"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/auditapi"
	"github.com/owlspeak/owl-server/backend/internal/botapi"
	"github.com/owlspeak/owl-server/backend/internal/cosmetics"
	"github.com/owlspeak/owl-server/backend/internal/customization"
	"github.com/owlspeak/owl-server/backend/internal/gateway"
	"github.com/owlspeak/owl-server/backend/internal/guildapi"
	"github.com/owlspeak/owl-server/backend/internal/keysync"
	"github.com/owlspeak/owl-server/backend/internal/moderation"
	"github.com/owlspeak/owl-server/backend/internal/publicinvite"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"github.com/owlspeak/owl-server/backend/internal/sfunode"
	"github.com/owlspeak/owl-server/backend/internal/social"
	"github.com/owlspeak/owl-server/backend/internal/sticker"
	"github.com/owlspeak/owl-server/backend/internal/userapi"
	"github.com/owlspeak/owl-server/backend/internal/voice"
)

// 各领域子模块注册钩子：由对应实现文件在本包内赋值（保持包内无跨文件符号冲突）。
var (
	// RegisterText 文本频道用户端端点（消息/附件/搜索/反应），由 text.go 赋值。
	// 与其他钩子不同：附件签名下载无需登录态，故传入 /gapi/v1 根组，
	// 实现方用 deps.Auth 自建认证子组；装配失败返回 error（与后台模块 Register 同语义）。
	RegisterText = func(root *gin.RouterGroup, deps appdeps.Deps) error { return nil }
	// RegisterVoice 语音频道用户端端点（进出房/状态/舞台/RTT）。
	RegisterVoice = func(authed *gin.RouterGroup, deps appdeps.Deps) {}
	// RegisterVoicePublic 语音相关免认证公开端点（如验签公钥），挂用户端根组，
	// 保证用户端流量完全不触达后台前缀（含公开端点）。
	RegisterVoicePublic = func(root *gin.RouterGroup, deps appdeps.Deps) {}
	// RegisterScreen 屏幕共享用户端端点（start/stop/配额查询）。
	RegisterScreen = func(authed *gin.RouterGroup, deps appdeps.Deps) {}
)

// signupEnabled 读取平台开关 CLIENT_SIGNUP_ENABLED（默认 true=开放注册）。
// 在 clientapi 内自行读取环境变量，避免为单个开关改动 config 包。
func signupEnabled() bool {
	value := os.Getenv("CLIENT_SIGNUP_ENABLED")
	if value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return enabled
}

// Register 挂载用户端 API（认证 + 基础资源 + 领域钩子 + Gateway WS）。
func Register(root *gin.RouterGroup, deps appdeps.Deps) error {
	h := newAPI(deps, signupEnabled())

	// 认证端点（无需登录）。
	auth := root.Group("/auth")
	auth.POST("/signup", h.signup)
	auth.POST("/login", h.login)
	auth.POST("/refresh", h.refresh)
	auth.POST("/logout", h.logout)

	// 需登录的端点：requireAuth 仅接受 aud=client 的 token。
	// GET /users/@me 由 userapi 模块统一提供（连同 PATCH/头像/密码/会话/设置）。
	authed := root.Group("", h.requireAuth())
	authed.GET("/users/@me/guilds", h.myGuilds)
	authed.POST("/guilds", h.createGuild)
	authed.GET("/guilds/:guildID/channels", h.listChannels)
	authed.GET("/guilds/:guildID/members", h.listMembers)
	authed.POST("/invites/:code/join", h.joinInvite)

	// 传给领域钩子的 Deps 换成用户端的认证中间件与用户读取函数：
	// 子模块内部再建路由组或读当前用户时，走的都是用户端（aud=client）认证平面。
	clientDeps := deps
	clientDeps.Auth = h.requireAuth()
	clientDeps.CurrentUser = CurrentUser
	// 文本频道：传根组（签名下载在认证之外），内部用 clientDeps.Auth 自建认证子组。
	if err := RegisterText(root, clientDeps); err != nil {
		return err
	}
	RegisterVoice(authed, clientDeps)
	RegisterVoicePublic(root, clientDeps)
	RegisterScreen(authed, clientDeps)
	// 用户账号自助端点（资料/头像/密码/会话/设置，docs 01/16）：与后台平面共享 handler。
	if err := userapi.Register(root, clientDeps); err != nil {
		return err
	}
	// 社交层（隐私/好友/通知，Server-16）
	if err := social.RegisterClient(root, clientDeps); err != nil {
		return err
	}
	// AI 时代扩展功能的用户端端点（自定义资料、邀请免注册加入、密钥跨端同步）。
	customization.RegisterClient(authed, clientDeps)
	publicinvite.RegisterClient(authed, clientDeps)
	keysync.RegisterClient(authed, clientDeps)
	// 贴图与表情包（docs 17）：双平面共享 handler。
	if err := sticker.RegisterClient(root, clientDeps); err != nil {
		return err
	}
	// 平台装扮商店。
	if err := cosmetics.RegisterClient(root, clientDeps); err != nil {
		return err
	}
	// 平台活跃度与每日积分。
	if err := activity.RegisterClient(root, clientDeps); err != nil {
		return err
	}
	// IDENTIFY 成功即"当日登录"活跃信号（装配层桥接，避免 activity→gateway 依赖成环；
	// RESUME 不触发，bot 由 TrackLogin 内部排除）。
	gateway.OnIdentify = activity.TrackLogin

	// 服务器管理端点投影（角色/频道/覆盖/guild 生命周期/治理/Restriction/节点池/
	// 审计/语音管理）：服主/管理员管理本服；系统所有者（system_admin）保留全服短路
	//（docs 04 FR-32），可打开任意服务器的管理员视图并执行治理操作（审计记 system_admin）。
	mgmtDeps := clientDeps
	// 保留 CurrentUser 中的 SystemAdmin，与 guildCtx / perms.LoadGuild 语义一致。
	if err := guildapi.Register(root, mgmtDeps); err != nil {
		return err
	}
	if err := moderation.RegisterClient(root, mgmtDeps); err != nil {
		return err
	}
	if err := restriction.RegisterClient(root, mgmtDeps); err != nil {
		return err
	}
	if err := auditapi.RegisterClient(root, mgmtDeps); err != nil {
		return err
	}
	sfunode.RegisterClient(root, mgmtDeps)
	voice.RegisterClientModeration(authed, mgmtDeps)
	// 服级机器人：服主/MANAGE_BOTS 在本服创建独属 bot、签发 token、删除。
	if err := botapi.RegisterClient(root, mgmtDeps); err != nil {
		return err
	}

	// 用户端 Gateway：复用 internal/gateway 的 WS 协议实现，IDENTIFY 仅接受 aud=client。
	return gateway.RegisterWithAudience(root, deps, security.AudienceClient)
}
