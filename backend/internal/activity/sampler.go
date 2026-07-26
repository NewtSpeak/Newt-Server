package activity

// 语音活跃采样：每分钟扫描"真在麦上"（connected=true）的语音状态，
// 每人每 tick 记 1 分钟。采样式天然抗客户端崩溃（不依赖 /voice/leave 结算），
// 多计残留受结算日上限封顶。

import (
	"log"
	"time"

	"github.com/google/uuid"
)

// StealthCheck 隐身临场判定钩子：由装配层（server.New）注入 voice.StealthPredicate
// 的包装，避免 activity→voice 直接依赖形成 voice→message→activity→voice 环。
// 默认恒 false（未注入时不过滤，仅影响隐身管理员被计入语音活跃，无功能危害）。
var StealthCheck = func(guildID, userID uuid.UUID) bool { return false }

func (s *service) samplerLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.sampleOnce()
	}
}

// sampleOnce 采样一轮：排除 bot、隐身管理员，按用户去重后各记 1 分钟。
func (s *service) sampleOnce() {
	cfg := s.config()
	if !cfg.Enabled {
		return
	}
	type sampleRow struct {
		UserID  uuid.UUID
		GuildID uuid.UUID
		IsBot   bool
	}
	var rows []sampleRow
	err := s.db.Table("voice_states").
		Select("voice_states.user_id, voice_states.guild_id, users.is_bot").
		Joins("JOIN users ON users.id = voice_states.user_id").
		Where("voice_states.channel_id IS NOT NULL AND voice_states.connected = true").
		Scan(&rows).Error
	if err != nil {
		log.Printf("activity: 语音采样查询失败: %v", err)
		return
	}
	seen := make(map[uuid.UUID]struct{}, len(rows))
	for _, row := range rows {
		if row.IsBot {
			continue
		}
		if StealthCheck(row.GuildID, row.UserID) {
			continue
		}
		if _, dup := seen[row.UserID]; dup {
			continue
		}
		seen[row.UserID] = struct{}{}
		s.tracker.track(row.UserID, dimVoiceMinute, 1)
	}
}
