package restriction

import (
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// 协管路径（docs 12 AK.2）：舞台专项的协管表（model.StageCoModerator）已落地，
// 协管默认仅可对「本语音频道」施加/解除 speak_voice 限制（快捷禁说，AO.1）。

// isCoModerator 判断用户是否是指定频道的协管。
func isCoModerator(db *gorm.DB, channelID, userID uuid.UUID) bool {
	var count int64
	db.Model(&model.StageCoModerator{}).Where("channel_id = ? AND user_id = ?", channelID, userID).Count(&count)
	return count == 1
}

// coModAllowedDeny 协管可操作的限制形态：仅 speak_voice 单维（不含禁听/文字维度）。
func coModAllowedDeny(deny DenyFlags) bool {
	return deny.SpeakVoice && !deny.ListenVoice && !deny.ViewText && !deny.SendText
}

// coModCanCreate 协管是否可创建该限制：本语音频道 + 仅 speak_voice + 临时制裁。
func coModCanCreate(db *gorm.DB, userID uuid.UUID, scope Scope, channelID *uuid.UUID, deny DenyFlags, kind Kind) bool {
	if scope != ScopeVoiceChannel || channelID == nil || kind != KindSanction {
		return false
	}
	if !coModAllowedDeny(deny) {
		return false
	}
	return isCoModerator(db, *channelID, userID)
}

// coModCanManage 协管是否可管理（改期/解除）既有记录：形态同创建约束。
func coModCanManage(db *gorm.DB, userID uuid.UUID, record model.Restriction) bool {
	if Scope(record.Scope) != ScopeVoiceChannel || record.ChannelID == nil || Kind(record.Kind) != KindSanction {
		return false
	}
	deny := DenyFlags{
		ViewText:    record.DenyViewText,
		SendText:    record.DenySendText,
		ListenVoice: record.DenyListenVoice,
		SpeakVoice:  record.DenySpeakVoice,
	}
	if !coModAllowedDeny(deny) {
		return false
	}
	return isCoModerator(db, *record.ChannelID, userID)
}
