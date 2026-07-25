package auditundo

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

func init() {
	Register("moderation.ban", Spec{Perm: rbac.BanMembers, Handler: undoBan})
	Register("moderation.unban", Spec{Perm: rbac.BanMembers, Handler: undoUnban})
	Register("moderation.nickname_update", Spec{Perm: rbac.ManageNicknames, Handler: undoNickname})
}

// undoBan：删除封禁记录（不自动拉回成员）。
func undoBan(ctx Context, log model.AuditLog) (Result, error) {
	if log.GuildID == nil {
		return Result{}, badState("缺少服务器 ID")
	}
	userID, err := uuid.Parse(log.TargetID)
	if err != nil {
		// 兼容 detail
		before, _, detail := stateOf(log)
		if id, ok := uuidField(before, "user_id", "target_user_id"); ok {
			userID = id
		} else if id, ok := uuidField(detail, "target_user_id", "user_id"); ok {
			userID = id
		} else {
			return Result{}, badState("无法解析被封禁用户")
		}
	}
	guildID := *log.GuildID
	result := ctx.Deps.DB.Delete(&model.GuildBan{}, "guild_id = ? AND user_id = ?", guildID, userID)
	if result.Error != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "解除封禁失败")
	}
	if result.RowsAffected == 0 {
		return Result{}, targetGone("封禁记录已不存在（可能已解封）")
	}
	ev := eventbus.Event{
		Type: eventbus.EventGuildBanRemove, GuildID: &guildID,
		Payload: map[string]any{"guild_id": guildID, "user_id": userID, "event_at": time.Now().UTC()},
	}
	// 定向通知被解封者
	ev2 := eventbus.Event{
		Type: eventbus.EventGuildBanRemove, GuildID: &guildID, UserIDs: []uuid.UUID{userID},
		Payload: map[string]any{"guild_id": guildID, "user_id": userID, "event_at": time.Now().UTC()},
	}
	return Result{
		TargetType: "user",
		TargetID:   userID.String(),
		Detail:     map[string]any{"undid": "moderation.ban", "user_id": userID},
		Events:     []eventbus.Event{ev, ev2},
	}, nil
}

// undoUnban：按快照重新封禁。
func undoUnban(ctx Context, log model.AuditLog) (Result, error) {
	if log.GuildID == nil {
		return Result{}, badState("缺少服务器 ID")
	}
	before, _, detail := stateOf(log)
	userID, err := uuid.Parse(log.TargetID)
	if err != nil {
		if id, ok := uuidField(before, "user_id", "target_user_id"); ok {
			userID = id
		} else if id, ok := uuidField(detail, "user_id", "target_user_id"); ok {
			userID = id
		} else {
			return Result{}, badState("无法解析用户")
		}
	}
	reason := strField(before, "reason")
	if reason == "" {
		reason = strField(detail, "reason")
	}
	guildID := *log.GuildID
	ban := model.GuildBan{
		ID:        uuid.New(),
		GuildID:   guildID,
		UserID:    userID,
		Reason:    reason,
		CreatedBy: ctx.Actor.ID,
	}
	err = ctx.Deps.DB.Where(model.GuildBan{GuildID: guildID, UserID: userID}).
		Assign(map[string]any{"reason": ban.Reason, "created_by": ban.CreatedBy}).
		FirstOrCreate(&ban).Error
	if err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "重新封禁失败")
	}
	ev := eventbus.Event{
		Type: eventbus.EventGuildBanAdd, GuildID: &guildID,
		Payload: map[string]any{"guild_id": guildID, "user_id": userID, "reason": reason, "event_at": time.Now().UTC()},
	}
	return Result{
		TargetType: "user",
		TargetID:   userID.String(),
		Detail:     map[string]any{"undid": "moderation.unban", "reason": reason},
		Events:     []eventbus.Event{ev},
	}, nil
}

// undoNickname：恢复 before 昵称。
func undoNickname(ctx Context, log model.AuditLog) (Result, error) {
	if log.GuildID == nil {
		return Result{}, badState("缺少服务器 ID")
	}
	before, _, detail := stateOf(log)
	memberID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("成员 ID 无效")
	}
	var member model.Member
	if err := ctx.Deps.DB.First(&member, "id = ? AND guild_id = ?", memberID, *log.GuildID).Error; err != nil {
		// 尝试 user_id
		if uid, ok := uuidField(detail, "target_user_id"); ok {
			if err := ctx.Deps.DB.First(&member, "user_id = ? AND guild_id = ?", uid, *log.GuildID).Error; err != nil {
				return Result{}, targetGone("成员已不在服务器")
			}
		} else {
			return Result{}, targetGone("成员已不在服务器")
		}
	}
	// before 可能是字符串昵称或 map
	nick := ""
	if s, ok := before["nickname"].(string); ok {
		nick = s
	} else if s, ok := before["after"].(string); ok {
		// 不应发生
		_ = s
	} else if len(before) == 0 {
		// detail.before 可能直接是 string（旧格式）
		if s, ok := detail["before"].(string); ok {
			nick = s
		}
	} else {
		// before 整段是旧昵称？有的实现 before 是标量存在 detail
		if s, ok := detail["before"].(string); ok {
			nick = s
		}
	}
	prev := member.Nickname
	if err := ctx.Deps.DB.Model(&member).Update("nickname", nick).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复昵称失败")
	}
	member.Nickname = nick
	guildID := member.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventGuildMemberUpdate, GuildID: &guildID,
		Payload: map[string]any{
			"guild_id": guildID, "user_id": member.UserID, "nick": nick,
			"member_id": member.ID,
		},
	}
	return Result{
		TargetType: "member",
		TargetID:   member.ID.String(),
		Before:     map[string]any{"nickname": prev},
		After:      map[string]any{"nickname": nick},
		Detail:     map[string]any{"undid": "moderation.nickname_update", "nickname": nick},
		Events:     []eventbus.Event{ev},
	}, nil
}
