package voice

// 结构删除联动（guildapi 频道/服务器删除时调用）：把频道/整服在房用户按
// 管理断开路径踢出语音（SFU DisconnectUser + VOICE_STATE_UPDATE + 级联收敛），
// 复用 internalLeave 的完整离房编排。
//
// 两个入口都以包级函数导出并对「Service 未装配」保持 no-op：
// 单测或未启用语音底座的进程里，调用方（guildapi）无需感知 voice 是否就绪，
// 残余 voice_states 行由调用方在删除事务里兜底清理。

import (
	"log"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// DisconnectChannelUsers 断开某频道内全部语音用户（频道删除联动）。
func DisconnectChannelUsers(guildID, channelID uuid.UUID, reason string) {
	if sharedService == nil {
		return
	}
	sharedService.disconnectWhere("guild_id = ? AND channel_id = ?", []any{guildID, channelID}, reason)
}

// DisconnectGuildUsers 断开某服务器内全部语音用户（服务器删除联动）。
func DisconnectGuildUsers(guildID uuid.UUID, reason string) {
	if sharedService == nil {
		return
	}
	sharedService.disconnectWhere("guild_id = ? AND channel_id IS NOT NULL", []any{guildID}, reason)
}

// disconnectWhere 按条件批量执行管理断开（逐个 internalLeave，失败仅记日志不中断）。
func (s *Service) disconnectWhere(condition string, args []any, reason string) {
	var states []model.VoiceState
	if err := s.db.Where(condition, args...).Find(&states).Error; err != nil {
		log.Printf("voice: 查询待断开语音状态失败: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range states {
		if states[i].ChannelID == nil {
			continue
		}
		if err := s.internalLeave(&states[i], "ADMIN", reason); err != nil {
			log.Printf("voice: 结构删除联动断开失败 user=%s err=%v", states[i].UserID, err)
		}
	}
}
