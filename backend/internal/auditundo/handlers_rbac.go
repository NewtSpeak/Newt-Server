package auditundo

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

func init() {
	Register("rbac.role_create", Spec{Perm: rbac.ManageRoles, Handler: undoRoleCreate})
	Register("rbac.role_update", Spec{Perm: rbac.ManageRoles, Handler: undoRoleUpdate})
	Register("rbac.role_delete", Spec{Perm: rbac.ManageRoles, Handler: undoRoleDelete})
	Register("rbac.member_role_assign", Spec{Perm: rbac.ManageRoles, Handler: undoMemberRoleAssign})
	Register("rbac.member_role_remove", Spec{Perm: rbac.ManageRoles, Handler: undoMemberRoleRemove})
	Register("rbac.channel_update", Spec{Perm: rbac.ManageChannels, Handler: undoChannelUpdate})
	Register("rbac.channel_overwrite_update", Spec{Perm: rbac.ManageRoles, Handler: undoOverwriteUpdate})
	Register("rbac.channel_overwrite_delete", Spec{Perm: rbac.ManageRoles, Handler: undoOverwriteDelete})
	Register("guild.update", Spec{Perm: rbac.ManageGuild, Handler: undoGuildUpdate})
}

func undoRoleCreate(ctx Context, log model.AuditLog) (Result, error) {
	roleID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("角色 ID 无效")
	}
	var role model.Role
	if err := ctx.Deps.DB.First(&role, "id = ?", roleID).Error; err != nil {
		return Result{}, targetGone("角色已不存在")
	}
	if role.IsEveryone || role.Managed {
		return Result{}, errf(http.StatusConflict, "NOT_REVERSIBLE", "内置角色不可删除")
	}
	guildID := role.GuildID
	_ = ctx.Deps.DB.Where("role_id = ?", role.ID).Delete(&model.MemberRole{})
	_ = ctx.Deps.DB.Where("type = ? AND target_id = ?", model.OverwriteRole, role.ID).Delete(&model.ChannelOverwrite{})
	if err := ctx.Deps.DB.Delete(&role).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "删除角色失败")
	}
	ev := eventbus.Event{
		Type: eventbus.EventGuildRoleDelete, GuildID: &guildID,
		Payload: eventbus.NewGuildRoleDeletePayload(guildID, roleID),
	}
	return Result{
		TargetType: "role", TargetID: roleID.String(),
		Detail: map[string]any{"undid": "rbac.role_create", "name": role.Name},
		Events: []eventbus.Event{ev},
	}, nil
}

func undoRoleUpdate(ctx Context, log model.AuditLog) (Result, error) {
	before, _, _ := stateOf(log)
	if len(before) == 0 {
		return Result{}, badState("缺少角色变更前快照")
	}
	roleID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("角色 ID 无效")
	}
	var role model.Role
	if err := ctx.Deps.DB.First(&role, "id = ?", roleID).Error; err != nil {
		return Result{}, targetGone("角色已不存在")
	}
	if name, ok := before["name"].(string); ok && name != "" {
		role.Name = name
	}
	if perms, ok := int64Field(before, "permissions"); ok {
		role.Permissions = perms
	}
	if pos, ok := int64Field(before, "position"); ok {
		role.Position = int(pos)
	}
	if color, ok := before["color"].(string); ok {
		role.Color = color
	}
	if style, ok := before["style"].(string); ok {
		role.Style = style
	}
	if hoist, ok := boolField(before, "hoist"); ok {
		role.Hoist = hoist
	}
	if mentionable, ok := boolField(before, "mentionable"); ok {
		role.Mentionable = mentionable
	}
	if err := ctx.Deps.DB.Save(&role).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复角色失败")
	}
	guildID := role.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventGuildRoleUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildRolePayload(role),
	}
	return Result{
		TargetType: "role", TargetID: roleID.String(),
		Detail: map[string]any{"undid": "rbac.role_update"},
		After:  before,
		Events: []eventbus.Event{ev},
	}, nil
}

func undoRoleDelete(ctx Context, log model.AuditLog) (Result, error) {
	before, _, detail := stateOf(log)
	snap := before
	if len(snap) == 0 {
		snap = detail
	}
	roleID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("角色 ID 无效")
	}
	if log.GuildID == nil {
		return Result{}, badState("缺少服务器 ID")
	}
	// 若已存在则跳过创建
	var existing model.Role
	if err := ctx.Deps.DB.First(&existing, "id = ?", roleID).Error; err == nil {
		return Result{}, errf(http.StatusConflict, "ALREADY_RESTORED", "角色已存在")
	}
	name := strField(snap, "name")
	if name == "" {
		return Result{}, badState("快照缺少角色名称")
	}
	role := model.Role{
		ID: roleID, GuildID: *log.GuildID, Name: name,
	}
	if perms, ok := int64Field(snap, "permissions"); ok {
		role.Permissions = perms
	}
	if pos, ok := int64Field(snap, "position"); ok {
		role.Position = int(pos)
	}
	if color, ok := snap["color"].(string); ok {
		role.Color = color
	}
	if style, ok := snap["style"].(string); ok {
		role.Style = style
	}
	if err := ctx.Deps.DB.Create(&role).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "重建角色失败")
	}
	// 恢复成员绑定
	if raw, ok := snap["member_ids"].([]any); ok {
		for _, item := range raw {
			var mid uuid.UUID
			switch t := item.(type) {
			case string:
				mid, _ = uuid.Parse(t)
			}
			if mid != uuid.Nil {
				_ = ctx.Deps.DB.Create(&model.MemberRole{MemberID: mid, RoleID: roleID}).Error
			}
		}
	}
	guildID := role.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventGuildRoleCreate, GuildID: &guildID,
		Payload: eventbus.NewGuildRolePayload(role),
	}
	return Result{
		TargetType: "role", TargetID: roleID.String(),
		Detail: map[string]any{"undid": "rbac.role_delete", "name": role.Name},
		Events: []eventbus.Event{ev},
	}, nil
}

func undoMemberRoleAssign(ctx Context, log model.AuditLog) (Result, error) {
	return flipMemberRole(ctx, log, false)
}

func undoMemberRoleRemove(ctx Context, log model.AuditLog) (Result, error) {
	return flipMemberRole(ctx, log, true)
}

func flipMemberRole(ctx Context, log model.AuditLog, assign bool) (Result, error) {
	_, _, detail := stateOf(log)
	memberID, err := uuid.Parse(log.TargetID)
	if err != nil {
		if id, ok := uuidField(detail, "member_id"); ok {
			memberID = id
		} else {
			return Result{}, badState("成员 ID 无效")
		}
	}
	roleID, ok := uuidField(detail, "role_id")
	if !ok {
		before, _, _ := stateOf(log)
		roleID, ok = uuidField(before, "role_id")
	}
	if !ok {
		return Result{}, badState("缺少 role_id")
	}
	var member model.Member
	if err := ctx.Deps.DB.First(&member, "id = ?", memberID).Error; err != nil {
		return Result{}, targetGone("成员不存在")
	}
	var role model.Role
	if err := ctx.Deps.DB.First(&role, "id = ?", roleID).Error; err != nil {
		return Result{}, targetGone("角色不存在")
	}
	if assign {
		_ = ctx.Deps.DB.Where(model.MemberRole{MemberID: memberID, RoleID: roleID}).
			FirstOrCreate(&model.MemberRole{MemberID: memberID, RoleID: roleID})
	} else {
		_ = ctx.Deps.DB.Delete(&model.MemberRole{}, "member_id = ? AND role_id = ?", memberID, roleID)
	}
	guildID := member.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventGuildMemberUpdate, GuildID: &guildID,
		Payload: map[string]any{"guild_id": guildID, "user_id": member.UserID, "member_id": member.ID},
	}
	action := "rbac.member_role_assign"
	if assign {
		action = "rbac.member_role_remove"
	}
	return Result{
		TargetType: "member", TargetID: memberID.String(),
		Detail: map[string]any{"undid": action, "role_id": roleID, "assign": assign},
		Events: []eventbus.Event{ev},
	}, nil
}

func undoChannelUpdate(ctx Context, log model.AuditLog) (Result, error) {
	before, _, _ := stateOf(log)
	if len(before) == 0 {
		return Result{}, badState("缺少频道变更前快照")
	}
	channelID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("频道 ID 无效")
	}
	var channel model.Channel
	if err := ctx.Deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return Result{}, targetGone("频道已不存在")
	}
	if name, ok := before["name"].(string); ok {
		channel.Name = name
	}
	if topic, ok := before["topic"].(string); ok {
		channel.Topic = topic
	}
	if locked, ok := boolField(before, "locked"); ok {
		channel.Locked = locked
	}
	if err := ctx.Deps.DB.Save(&channel).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复频道失败")
	}
	guildID := channel.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventChannelUpdate, GuildID: &guildID,
		Payload: map[string]any{
			"id": channel.ID, "guild_id": guildID, "name": channel.Name,
			"topic": channel.Topic, "locked": channel.Locked, "type": channel.Type,
		},
	}
	return Result{
		TargetType: "channel", TargetID: channelID.String(),
		Detail: map[string]any{"undid": "rbac.channel_update"},
		Events: []eventbus.Event{ev},
	}, nil
}

func undoOverwriteUpdate(ctx Context, log model.AuditLog) (Result, error) {
	before, _, detail := stateOf(log)
	channelID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("频道 ID 无效")
	}
	targetID, ok := uuidField(detail, "target_id")
	if !ok {
		targetID, ok = uuidField(before, "target_id")
	}
	if !ok {
		return Result{}, badState("缺少覆盖目标")
	}
	owType := strField(detail, "target_type", "type")
	if owType == "" {
		owType = strField(before, "target_type", "type")
	}
	if owType == "" {
		owType = string(model.OverwriteRole)
	}
	// before.created=true 表示原操作新建了覆盖 → 撤销=删除
	if created, ok := boolField(before, "created"); ok && created {
		_ = ctx.Deps.DB.Delete(&model.ChannelOverwrite{}, "channel_id = ? AND target_id = ? AND type = ?", channelID, targetID, owType)
	} else if len(before) > 0 {
		allow, _ := int64Field(before, "allow")
		deny, _ := int64Field(before, "deny")
		var ow model.ChannelOverwrite
		err := ctx.Deps.DB.Where("channel_id = ? AND target_id = ? AND type = ?", channelID, targetID, owType).First(&ow).Error
		if err != nil {
			ow = model.ChannelOverwrite{
				ID: uuid.New(), ChannelID: channelID, Type: model.OverwriteType(owType),
				TargetID: targetID, Allow: allow, Deny: deny,
			}
			if err := ctx.Deps.DB.Create(&ow).Error; err != nil {
				return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复覆盖失败")
			}
		} else {
			if err := ctx.Deps.DB.Model(&ow).Updates(map[string]any{"allow": allow, "deny": deny}).Error; err != nil {
				return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复覆盖失败")
			}
		}
	} else {
		return Result{}, badState("缺少覆盖变更前快照")
	}
	var channel model.Channel
	_ = ctx.Deps.DB.First(&channel, "id = ?", channelID)
	guildID := channel.GuildID
	ev := eventbus.Event{
		Type: eventbus.EventChannelUpdate, GuildID: &guildID,
		Payload: map[string]any{"id": channelID, "guild_id": guildID, "overwrites_changed": true},
	}
	return Result{
		TargetType: "channel", TargetID: channelID.String(),
		Detail: map[string]any{"undid": "rbac.channel_overwrite_update", "target_id": targetID},
		Events: []eventbus.Event{ev},
	}, nil
}

func undoOverwriteDelete(ctx Context, log model.AuditLog) (Result, error) {
	before, _, detail := stateOf(log)
	channelID, err := uuid.Parse(log.TargetID)
	if err != nil {
		return Result{}, badState("频道 ID 无效")
	}
	snap := before
	if len(snap) == 0 {
		snap = detail
	}
	targetID, ok := uuidField(snap, "target_id")
	if !ok {
		return Result{}, badState("缺少覆盖快照")
	}
	owType := strField(snap, "type", "target_type")
	if owType == "" {
		owType = string(model.OverwriteRole)
	}
	allow, _ := int64Field(snap, "allow")
	deny, _ := int64Field(snap, "deny")
	ow := model.ChannelOverwrite{
		ID: uuid.New(), ChannelID: channelID, Type: model.OverwriteType(owType),
		TargetID: targetID, Allow: allow, Deny: deny,
	}
	if id, ok := uuidField(snap, "id"); ok {
		ow.ID = id
	}
	err = ctx.Deps.DB.Where(model.ChannelOverwrite{ChannelID: channelID, Type: model.OverwriteType(owType), TargetID: targetID}).
		Assign(map[string]any{"allow": allow, "deny": deny}).
		FirstOrCreate(&ow).Error
	if err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "重建覆盖失败")
	}
	return Result{
		TargetType: "channel", TargetID: channelID.String(),
		Detail: map[string]any{"undid": "rbac.channel_overwrite_delete", "target_id": targetID},
	}, nil
}

func undoGuildUpdate(ctx Context, log model.AuditLog) (Result, error) {
	before, _, _ := stateOf(log)
	if len(before) == 0 {
		return Result{}, badState("缺少服务器变更前快照")
	}
	if log.GuildID == nil {
		return Result{}, badState("缺少服务器 ID")
	}
	var guild model.Guild
	if err := ctx.Deps.DB.First(&guild, "id = ?", *log.GuildID).Error; err != nil {
		return Result{}, targetGone("服务器不存在")
	}
	if name, ok := before["name"].(string); ok && name != "" {
		guild.Name = name
	}
	if desc, ok := before["description"].(string); ok {
		guild.Description = desc
	}
	if err := ctx.Deps.DB.Save(&guild).Error; err != nil {
		return Result{}, errf(http.StatusInternalServerError, "UNDO_FAILED", "恢复服务器失败")
	}
	guildID := guild.ID
	ev := eventbus.Event{
		Type: eventbus.EventGuildUpdate, GuildID: &guildID,
		Payload: map[string]any{"id": guild.ID, "name": guild.Name, "description": guild.Description},
	}
	return Result{
		TargetType: "guild", TargetID: guild.ID.String(),
		Detail: map[string]any{"undid": "guild.update"},
		Events: []eventbus.Event{ev},
	}, nil
}
