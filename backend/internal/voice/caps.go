package voice

import (
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
	"github.com/owlspeak/owl-server/backend/internal/stage"
	"gorm.io/gorm"
)

// Media capability 常量（docs 02 §7 能力投影）。
const (
	CapJoin            = "join"
	CapSubscribeAudio  = "subscribe_audio"
	CapPublishAudio    = "publish_audio"
	CapPublishScreen   = "publish_screen"
	CapPrioritySpeaker = "priority_speaker"
)

// capsInput caps 投影的纯逻辑输入（抽离 DB 依赖便于单测）。
type capsInput struct {
	// Bits 该用户在频道内的最终 RBAC 权限（已含 Restriction 收紧）。
	Bits rbac.Permission
	// ServerMute 服务器静音（成员状态，不进权限位，docs 02 结论 5）。
	ServerMute bool
	// StageAudio 舞台维度是否允许发音频（stage.CanPublishAudio）。
	StageAudio bool
	// StageScreen 舞台维度是否允许发屏幕轨（stage.CanPublishScreen）。
	StageScreen bool
	// DenySpeak Restriction 是否禁止「语音说」维度。
	DenySpeak bool
}

// projectCaps 将业务权限投影为 SFU 最小能力集（docs 02 §7）。
// 前提：调用方已确认用户可 join（VIEW+CONNECT 且未被禁听），
// 因此 join / subscribe_audio 恒给（专项要求；server_deaf 的下行限制由 SFU 依控制指令处理）。
func projectCaps(in capsInput) []string {
	caps := []string{CapJoin, CapSubscribeAudio}
	if rbac.Has(in.Bits, rbac.Speak) && !in.ServerMute && in.StageAudio && !in.DenySpeak {
		caps = append(caps, CapPublishAudio)
	}
	if rbac.Has(in.Bits, rbac.Stream) && in.StageScreen && !in.DenySpeak {
		caps = append(caps, CapPublishScreen)
	}
	if rbac.Has(in.Bits, rbac.PrioritySpeaker) {
		caps = append(caps, CapPrioritySpeaker)
	}
	return caps
}

// computeCaps 从 DB 汇总舞台钩子、Restriction 与 server_mute，产出某用户在某语音频道的 caps。
//
// publish_screen 审批门控（docs 14 BC.1，屏幕共享专项补缺口）：舞台维度允许（AX.1）
// 之外，还须该用户已通过 screen/start 占坑（stage.HasScreenSlot，RESERVED/ACTIVE 均算）——
// 即「Server 审批后才 publish_screen」；坑释放（stop/抱下/超时/掐配额）触发 InternalCapsDirty
// 重算时此条件失效，cap 自动收回。两钩子合并进 StageScreen 输入，projectCaps 纯投影不变。
func computeCaps(db *gorm.DB, bits rbac.Permission, guildID, channelID, userID uuid.UUID, serverMute bool) []string {
	denies := restriction.Denies(userID, guildID, &channelID, model.ChannelVoice)
	return projectCaps(capsInput{
		Bits:        bits,
		ServerMute:  serverMute,
		StageAudio:  stage.CanPublishAudio(db, userID, channelID),
		StageScreen: stage.CanPublishScreen(db, userID, channelID) && stage.HasScreenSlot(db, userID, channelID),
		DenySpeak:   denies.SpeakVoice,
	})
}

// hasCap 判断 caps 列表是否包含某能力。
func hasCap(caps []string, cap string) bool {
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// StealthPredicate 由 adminpresence 注入：返回某用户在某 guild 是否处于隐身临场。
// 默认恒 false（未启用临场时无隐身）。隐身会话不进成员列表、不广播状态、
// 其 Media Token 带 hidden claim。
var StealthPredicate = func(guildID, userID uuid.UUID) bool { return false }

// AuditPredicate 由 adminpresence 注入：返回某频道是否开启音频审计录制。
// 默认恒 false。开启时该频道所有会话的 Media Token 带 audit claim（SFU 录制并上传）。
var AuditPredicate = func(guildID, channelID uuid.UUID) bool { return false }

// AuditNotifyPredicate 由 adminpresence 注入：返回某频道审计是否需要提示用户。
// 默认恒 false。进房时若审计开启且需提示，向该用户下发 CHANNEL_AUDIT_NOTICE。
var AuditNotifyPredicate = func(guildID, channelID uuid.UUID) bool { return false }
