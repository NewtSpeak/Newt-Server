// Package stage 舞台状态机（docs 11）与屏幕共享配额（docs 14）。
// 本文件是供语音编排（caps 投影）消费的稳定钩子；真实实现由「舞台/屏幕共享专项」在 Register 中替换。
package stage

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 舞台角色常量（docs 11 AC.1）。RoleNone 表示频道处于 FREE 模式、无舞台语义。
const (
	RoleNone     = ""
	RoleAudience = "AUDIENCE"
	RoleQueued   = "QUEUED"
	RoleSpeaker  = "SPEAKER"
)

// CanPublishAudio 判断用户当前舞台/容量状态是否允许发布音频。
// FREE 模式且未被容量禁说时为 true；STAGE 模式仅 SPEAKER 为 true（docs 11 AD.4）。
// 注意：RBAC SPEAK、server_mute、Restriction 由语音编排模块另行叠加，这里只回答舞台维度。
var CanPublishAudio = func(db *gorm.DB, userID, channelID uuid.UUID) bool { return true }

// CanPublishScreen 判断舞台维度是否允许发布屏幕轨（STAGE 仅台上，docs 14 AX.1）。
var CanPublishScreen = func(db *gorm.DB, userID, channelID uuid.UUID) bool { return true }

// HasScreenSlot 判断用户在该频道是否持有屏幕坑（RESERVED 或 ACTIVE）。
// docs 14 BC.1：publish_screen 必须在 Server 审批（screen/start 占坑成功）之后才签发，
// RESERVED 阶段即需给 cap 供客户端向 SFU 发布；坑释放后 caps 重算自动收回。
// 未装配（Register 前）恒 true，保持与其他钩子一致的宽松默认。
var HasScreenSlot = func(db *gorm.DB, userID, channelID uuid.UUID) bool { return true }

// RoleOf 返回用户在频道内的舞台角色（FREE 模式返回 RoleNone）。
var RoleOf = func(db *gorm.DB, userID, channelID uuid.UUID) string { return RoleNone }
