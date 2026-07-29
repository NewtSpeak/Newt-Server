package activity

// 信号入口薄封装：供 message/gateway 等模块直调（不经 eventbus，避免缓冲满丢事件）。
// 单例未装配（如相关包单测）时静默忽略；bot 统一在此排除。

import (
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// TrackMessage 用户成功发出一条消息（30s 限流内多条只计 1 条）。
func TrackMessage(user model.User) {
	svc := sharedService
	if svc == nil || user.IsBot {
		return
	}
	svc.tracker.trackMessage(user.ID)
}

// TrackReaction 用户首次为某消息添加某表情反应。
func TrackReaction(user model.User) {
	svc := sharedService
	if svc == nil || user.IsBot {
		return
	}
	svc.tracker.track(user.ID, dimReaction, 1)
}

// TrackLogin 用户新建 Gateway 会话（IDENTIFY 成功；RESUME 不触发）。
func TrackLogin(user model.User) {
	svc := sharedService
	if svc == nil || user.IsBot {
		return
	}
	svc.tracker.track(user.ID, dimLogin, 1)
}
