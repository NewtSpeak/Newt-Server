// Package adminpresence 系统管理员临场：从后台随时加入任意频道的文本/语音子频道，
// 文本频道以系统管理员身份发言，语音频道可选择是否显示自己到语音成员列表（隐身）；
// 语音频道支持全局或独立配置是否将音频内容录制到主节点服务器（审计），
// 并可配置是否向用户提示该频道正在被审计（可隐藏提示）。
package adminpresence

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/voice"
)

// Register 挂载系统管理员临场 API（/api/v1，仅 SystemAdmin），并把「隐身/审计」
// 判定钩子注入 voice 模块（voice 据此设置 Media Token 的 hidden/audit claim、
// 抑制隐身会话广播、下发审计提示）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}

	// 注入 voice 判定钩子（进程内共享单例，多前缀装配会重复调用 Register，赋值幂等）。
	voice.StealthPredicate = func(guildID, userID uuid.UUID) bool {
		return stealth(deps.DB, guildID, userID)
	}
	voice.AuditPredicate = func(guildID, channelID uuid.UUID) bool {
		record, _ := channelAuditEffective(deps.DB, guildID, channelID)
		return record
	}
	voice.AuditNotifyPredicate = func(guildID, channelID uuid.UUID) bool {
		_, notify := channelAuditEffective(deps.DB, guildID, channelID)
		return notify
	}

	admin := v1.Group("", deps.Auth)
	admin.GET("/admin/audit-config", h.getPlatformAudit)
	admin.PUT("/admin/audit-config", h.putPlatformAudit)
	admin.GET("/admin/channels/:channelID/audit-config", h.getChannelAudit)
	admin.PUT("/admin/channels/:channelID/audit-config", h.putChannelAudit)
	admin.POST("/admin/channels/:channelID/presence/message", h.postPresenceMessage)
	admin.GET("/admin/voice/stealth", h.getStealth)
	admin.PUT("/admin/voice/stealth", h.putStealth)
	admin.GET("/admin/audit-records", h.listAuditRecords)
	admin.GET("/admin/audit-records/:recordID/audio", h.downloadAuditRecord)
	return nil
}

// RegisterIngest 挂载审计录音上传端点（/audit-api，Bearer 共享密钥，供 SFU 节点调用）。
func RegisterIngest(pub *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	pub.POST("/records", h.ingestRecord)
	return nil
}
