package auditundo

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

func init() {
	Register("restriction.create", Spec{Perm: rbac.ModerateMembers, Handler: undoRestrictionCreate})
	Register("restriction.lift", Spec{Perm: rbac.ModerateMembers, Handler: undoRestrictionLift})
	Register("restriction.update", Spec{Perm: rbac.ModerateMembers, Handler: undoRestrictionUpdate})
}

func undoRestrictionCreate(ctx Context, log model.AuditLog) (Result, error) {
	rid, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("限制 ID 无效")
	}
	var rec model.Restriction
	if err := ctx.Deps.DB.First(&rec, "id = ?", rid).Error; err != nil {
		return Result{}, targetGone("限制记录不存在")
	}
	if !rec.ActiveAt(time.Now().UTC()) {
		return Result{}, targetGone("该限制已失效")
	}
	now := time.Now().UTC()
	actorID := ctx.Actor.ID
	if err := ctx.Deps.DB.Model(&rec).Updates(map[string]any{
		"lifted_at": now, "lifted_by": actorID,
	}).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "解除限制失败")
	}
	rec.LiftedAt = &now
	rec.LiftedBy = &actorID
	guildID := rec.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventRestrictionLift, GuildID: &guildID,
		Payload: restrictionPayload(rec),
	}
	return Result{
		TargetType: "restriction",
		TargetID:   rid.String(),
		Detail:     map[string]any{"undid": "restriction.create", "target_user_id": rec.TargetUserID},
		Events:     []eventbus.Event{ev},
	}, nil
}

func undoRestrictionLift(ctx Context, log model.AuditLog) (Result, error) {
	before, _, detail := stateOf(log)
	snap := before
	if len(snap) == 0 {
		// lift 时若把完整记录放在 detail
		snap = detail
	}
	rec, ok := restrictionFromSnapshot(snap, log)
	if !ok {
		return Result{}, badState("缺少限制快照，无法重新施加")
	}
	// 清理解除标记，或新建
	var existing model.Restriction
	if err := ctx.Deps.DB.First(&existing, "id = ?", rec.ID).Error; err == nil {
		updates := map[string]any{
			"lifted_at": nil, "lifted_by": nil,
			"expired_notified_at": nil,
			"reason":              rec.Reason,
			"expires_at":          rec.ExpiresAt,
			"deny_view_text":      rec.DenyViewText,
			"deny_send_text":      rec.DenySendText,
			"deny_listen_voice":   rec.DenyListenVoice,
			"deny_speak_voice":    rec.DenySpeakVoice,
		}
		if err := ctx.Deps.DB.Model(&existing).Updates(updates).Error; err != nil {
			return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复限制失败")
		}
		_ = ctx.Deps.DB.First(&rec, "id = ?", rec.ID)
	} else {
		rec.LiftedAt = nil
		rec.LiftedBy = nil
		rec.ExpiredNotifiedAt = nil
		if rec.ID == uuid.Nil {
			rec.ID = uuid.New()
		}
		if rec.CreatedBy == uuid.Nil {
			rec.CreatedBy = ctx.Actor.ID
		}
		if err := ctx.Deps.DB.Create(&rec).Error; err != nil {
			return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "重建限制失败")
		}
	}
	guildID := rec.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventRestrictionCreate, GuildID: &guildID,
		Payload: restrictionPayload(rec),
	}
	return Result{
		TargetType: "restriction",
		TargetID:   rec.ID.String(),
		Detail:     map[string]any{"undid": "restriction.lift", "target_user_id": rec.TargetUserID},
		After:      restrictionSnapshot(rec),
		Events:     []eventbus.Event{ev},
	}, nil
}

func undoRestrictionUpdate(ctx Context, log model.AuditLog) (Result, error) {
	before, _, _ := stateOf(log)
	if len(before) == 0 {
		return Result{}, badState("缺少变更前快照")
	}
	rid, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("限制 ID 无效")
	}
	var rec model.Restriction
	if err := ctx.Deps.DB.First(&rec, "id = ?", rid).Error; err != nil {
		return Result{}, targetGone("限制记录不存在")
	}
	if !rec.ActiveAt(time.Now().UTC()) {
		return Result{}, targetGone("该限制已失效")
	}
	updates := map[string]any{}
	if s, ok := before["reason"].(string); ok {
		updates["reason"] = s
	}
	if v, ok := before["expires_at"]; ok {
		updates["expires_at"] = v
	}
	if len(updates) == 0 {
		return Result{}, badState("快照无可恢复字段")
	}
	if err := ctx.Deps.DB.Model(&rec).Updates(updates).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复限制字段失败")
	}
	_ = ctx.Deps.DB.First(&rec, "id = ?", rid)
	guildID := rec.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventRestrictionUpdate, GuildID: &guildID,
		Payload: restrictionPayload(rec),
	}
	return Result{
		TargetType: "restriction",
		TargetID:   rid.String(),
		Detail:     map[string]any{"undid": "restriction.update"},
		Events:     []eventbus.Event{ev},
	}, nil
}

func restrictionFromSnapshot(snap map[string]any, log model.AuditLog) (model.Restriction, bool) {
	id, ok := uuidField(snap, "id")
	if !ok {
		if parsed, err := uuid.Parse(log.TargetID); err == nil {
			id = parsed
			ok = true
		}
	}
	if !ok {
		return model.Restriction{}, false
	}
	guildID, okG := uuidField(snap, "guild_id")
	if !okG && log.GuildID != nil {
		guildID = *log.GuildID
		okG = true
	}
	target, okT := uuidField(snap, "target_user_id")
	if !okG || !okT {
		return model.Restriction{}, false
	}
	rec := model.Restriction{
		ID:           id,
		GuildID:      guildID,
		TargetUserID: target,
		Scope:        strField(snap, "scope"),
		Kind:         strField(snap, "kind"),
		Reason:       strField(snap, "reason"),
	}
	if rec.Scope == "" {
		rec.Scope = "GUILD_ALL_TEXT"
	}
	if rec.Kind == "" {
		rec.Kind = "SANCTION"
	}
	if cid, ok := uuidField(snap, "channel_id"); ok {
		rec.ChannelID = &cid
	}
	if v, ok := boolField(snap, "deny_view_text"); ok {
		rec.DenyViewText = v
	}
	if v, ok := boolField(snap, "deny_send_text"); ok {
		rec.DenySendText = v
	}
	if v, ok := boolField(snap, "deny_listen_voice"); ok {
		rec.DenyListenVoice = v
	}
	if v, ok := boolField(snap, "deny_speak_voice"); ok {
		rec.DenySpeakVoice = v
	}
	if createdBy, ok := uuidField(snap, "created_by"); ok {
		rec.CreatedBy = createdBy
	}
	if raw, ok := snap["expires_at"].(string); ok && raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			rec.ExpiresAt = &t
		} else if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			rec.ExpiresAt = &t
		}
	}
	return rec, true
}

func restrictionSnapshot(r model.Restriction) map[string]any {
	m := map[string]any{
		"id": r.ID.String(), "guild_id": r.GuildID.String(),
		"target_user_id": r.TargetUserID.String(),
		"scope": r.Scope, "kind": r.Kind, "reason": r.Reason,
		"deny_view_text": r.DenyViewText, "deny_send_text": r.DenySendText,
		"deny_listen_voice": r.DenyListenVoice, "deny_speak_voice": r.DenySpeakVoice,
		"created_by": r.CreatedBy.String(),
	}
	if r.ChannelID != nil {
		m["channel_id"] = r.ChannelID.String()
	}
	if r.ExpiresAt != nil {
		m["expires_at"] = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return m
}

func restrictionPayload(r model.Restriction) map[string]any {
	return map[string]any{
		"id": r.ID, "guild_id": r.GuildID, "target_user_id": r.TargetUserID,
		"scope": r.Scope, "channel_id": r.ChannelID, "kind": r.Kind,
		"reason": r.Reason, "expires_at": r.ExpiresAt,
		"deny_view_text": r.DenyViewText, "deny_send_text": r.DenySendText,
		"deny_listen_voice": r.DenyListenVoice, "deny_speak_voice": r.DenySpeakVoice,
		"lifted_at": r.LiftedAt, "created_by": r.CreatedBy,
	}
}
