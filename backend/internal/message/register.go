// Package message 文字频道：消息 CRUD、编辑历史、附件（预签名直传）、全系统搜索、
// 表情反应与入场语音包配置（docs 13、07 专项 5A/5B）。
//
// 挂载分两个认证平面（见 client.go）：
//   - Register：后台前缀 /api/v1，含管理端点，负责后台清理任务；
//   - RegisterClient：用户端前缀 /gapi/v1，仅用户级能力。
package message

import (
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// sharedIDs 进程内唯一的雪花 ID 生成器：后台与用户端两个平面共用，
// 避免两套生成器（机器位相同）在同一毫秒内产生冲突的消息 ID。
var sharedIDs = newSnowflake()

// newService 装配一个平面的 service 实例。
// urlPrefix 为该平面的挂载前缀（如 /api/v1、/gapi/v1），响应中生成的
// upload_url/download_url 均以其开头；currentUser 取自 deps（平面各自的认证语义）。
// 存储目录与索引 DDL 均幂等，两个平面各自构造互不干扰；搜索限流按平面独立计数
//（用户端 token 无法访问后台前缀，不存在叠加绕过）。
func newService(deps appdeps.Deps, urlPrefix string) (*service, error) {
	storage, err := newLocalStorage(filepath.Join(deps.Cfg.DataDir, "attachments"))
	if err != nil {
		return nil, err
	}
	index, err := newPGSearchIndex(deps.DB)
	if err != nil {
		return nil, err
	}
	return &service{
		db:          deps.DB,
		bus:         deps.Bus,
		cfg:         deps.Cfg,
		storage:     storage,
		index:       index,
		searchLimit: newUserLimiter(1, 5), // AU.8：每用户 1 QPS、突发 5
		ids:         sharedIDs,
		currentUser: deps.CurrentUser,
		urlPrefix:   urlPrefix,
	}, nil
}

// mountUserRoutes 挂载用户级端点（后台与用户端两前缀共用同一套 handler）。
// public 为无需登录态的组（签名下载：签名即凭证，签发前已过频道可见性检查），
// authed 为已通过该平面认证中间件的组。
func (s *service) mountUserRoutes(public, authed *gin.RouterGroup) {
	// 附件下载走短时签名 URL，无需登录态（签名在消息响应中签发）。
	public.GET("/attachments/:attachmentID", s.downloadAttachment)

	// 消息收发与编辑（AR/AS）。
	authed.POST("/channels/:channelID/messages", s.createMessage)
	authed.GET("/channels/:channelID/messages", s.listMessages)
	authed.GET("/channels/:channelID/messages/:messageID", s.getMessage)
	authed.PATCH("/channels/:channelID/messages/:messageID", s.editMessage)
	authed.DELETE("/channels/:channelID/messages/:messageID", s.deleteMessage)
	authed.GET("/channels/:channelID/messages/:messageID/edits", s.listEdits)
	// 未读同步（docs 15 §7-1）：已读 ack 推进（路径版 / 体内版）+ 全服已读 + REST 兜底。
	authed.POST("/channels/:channelID/messages/:messageID/ack", s.ackMessage)
	authed.POST("/channels/:channelID/ack", s.ackChannel)
	authed.POST("/guilds/:guildID/ack", s.ackGuild)
	authed.GET("/users/@me/read-states", s.listMyReadStates)
	// 表情反应（AV）；反应者列表（docs 05 FR-26，gin 静态段 @me 优先于本参数路由）。
	authed.PUT("/channels/:channelID/messages/:messageID/reactions/:emoji/@me", s.putReaction)
	authed.DELETE("/channels/:channelID/messages/:messageID/reactions/:emoji/@me", s.deleteReaction)
	authed.GET("/channels/:channelID/messages/:messageID/reactions/:emoji", s.listReactionUsers)
	// 附件二段式上传（AT）；服级上限成员可读（docs 07 FR-06 前置校验数据源）。
	authed.POST("/channels/:channelID/attachments/presign", s.presignAttachment)
	authed.PUT("/attachments/:attachmentID/content", s.uploadAttachmentContent)
	authed.GET("/guilds/:guildID/upload-limit", s.getUploadLimitForMember)
	// 全系统搜索（AU）。
	authed.GET("/search/messages", s.searchMessages)
	// 入场语音包只读（5A）：客户端需要知道是否播放。
	authed.GET("/guilds/:guildID/voice-pack", s.getGuildVoicePack)
	authed.GET("/guilds/:guildID/channels/:channelID/voice-pack", s.getChannelVoicePack)
	// 语音包完整模型（docs 12）：包 CRUD/音频上传（handler 内校验 MANAGE_GUILD）
	// 与用户选包，双平面同挂（服管在桌面客户端管理自己的服务器）。
	// gin 静态段 @me 优先于参数段 :packID，两者可共存。
	authed.GET("/guilds/:guildID/voice-packs", s.listVoicePacks)
	authed.POST("/guilds/:guildID/voice-packs", s.createVoicePack)
	authed.PATCH("/guilds/:guildID/voice-packs/:packID", s.patchVoicePack)
	authed.DELETE("/guilds/:guildID/voice-packs/:packID", s.deleteVoicePack)
	authed.POST("/guilds/:guildID/voice-packs/:packID/audio", s.uploadVoicePackAudio)
	authed.PUT("/guilds/:guildID/voice-packs/:packID/select", s.selectVoicePack)
	authed.GET("/guilds/:guildID/voice-packs/@me", s.getMyVoicePackSelection)
	authed.DELETE("/guilds/:guildID/voice-packs/@me", s.clearMyVoicePackSelection)
	// 入场语音包配置写入口（5A.4/5A.1b）：handler 内校验 MANAGE_GUILD / MANAGE_CHANNELS，
	// 双平面同挂——服管/频道管理员在桌面客户端即可配置（docs 03 FR-35）。
	authed.PATCH("/guilds/:guildID/voice-pack", s.patchGuildVoicePack)
	authed.PUT("/guilds/:guildID/channels/:channelID/voice-pack", s.putChannelVoicePack)
}

// mountBackend 后台平面全部路由：用户级端点 + 管理端点；auth 为后台认证中间件。
func (s *service) mountBackend(v1 *gin.RouterGroup, auth gin.HandlerFunc) {
	authed := v1.Group("", auth)
	s.mountUserRoutes(v1, authed)
	// 服级配置：附件上限（系统管）、保留策略（服管）——仅后台前缀。
	authed.GET("/admin/guilds/:guildID/upload-limit", s.getUploadLimit)
	authed.PATCH("/admin/guilds/:guildID/upload-limit", s.patchUploadLimit)
	authed.GET("/guilds/:guildID/message-retention", s.getRetention)
	authed.PATCH("/guilds/:guildID/message-retention", s.patchRetention)
}

// Register 挂载后台前缀（/api/v1）的消息/附件/搜索 REST API，
// 并启动搜索索引与附件 GC 后台任务。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc, err := newService(deps, v1.BasePath())
	if err != nil {
		return err
	}
	svc.mountBackend(v1, deps.Auth)
	svc.startGC()
	return nil
}
