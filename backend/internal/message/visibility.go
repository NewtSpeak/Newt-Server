package message

import (
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"github.com/newtspeak/newt-server/backend/internal/snapshot"
)

// isMessageRestricted 消息是否启用了限定可见范围（角色和/或用户名单）。
func isMessageRestricted(msg model.Message) bool {
	return len(msg.VisibleRoleIDs) > 0 || len(msg.VisibleUserIDs) > 0
}

// roleIDsFromCtx 从权限上下文提取角色 ID（含 @everyone）。
func roleIDsFromCtx(ctx *perms.GuildContext) []uuid.UUID {
	if ctx == nil {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(ctx.Roles))
	for _, role := range ctx.Roles {
		id, err := uuid.Parse(role.ID)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// canViewMessage 判定 viewer 是否可见该消息。
// 公开消息恒 true；限定消息：作者 / 用户名单 / 角色交集 / MANAGE_MESSAGES / 服主·系统管。
// bits 为观众在该频道的最终权限位；viewerRoleIDs 为其当前角色集合。
func canViewMessage(
	viewerID uuid.UUID,
	bits rbac.Permission,
	viewerRoleIDs []uuid.UUID,
	ownerOrSysAdmin bool,
	msg model.Message,
) bool {
	if !isMessageRestricted(msg) {
		return true
	}
	if msg.AuthorID == viewerID {
		return true
	}
	if ownerOrSysAdmin || rbac.Has(bits, rbac.ManageMessages) {
		return true
	}
	for _, id := range msg.VisibleUserIDs {
		if id == viewerID {
			return true
		}
	}
	if len(msg.VisibleRoleIDs) == 0 || len(viewerRoleIDs) == 0 {
		return false
	}
	have := make(map[uuid.UUID]struct{}, len(viewerRoleIDs))
	for _, id := range viewerRoleIDs {
		have[id] = struct{}{}
	}
	for _, id := range msg.VisibleRoleIDs {
		if _, ok := have[id]; ok {
			return true
		}
	}
	return false
}

// filterVisibleMessages 过滤出观众可见的消息（保持原序）。
func filterVisibleMessages(
	viewerID uuid.UUID,
	bits rbac.Permission,
	viewerRoleIDs []uuid.UUID,
	ownerOrSysAdmin bool,
	messages []model.Message,
) []model.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]model.Message, 0, len(messages))
	for _, msg := range messages {
		if canViewMessage(viewerID, bits, viewerRoleIDs, ownerOrSysAdmin, msg) {
			out = append(out, msg)
		}
	}
	return out
}

// resolveEffectiveVisibleRoles 按频道策略与客户端请求合成最终可见范围。
//  - ForceDefaultVisibility：强制使用频道 DefaultVisibleRoleIDs；
//  - 否则若客户端显式指定：校验 AllowRestrictedVisibility；
//  - 客户端未指定且频道有 Default：套用默认；
//  - 客户端未指定且无默认：公开。
// clientSpecified 表示请求体显式携带了 visible_role_ids 字段（含空数组「强制公开」）。
func (s *service) resolveEffectiveVisibleRoles(
	channel model.Channel,
	clientRoles []uuid.UUID,
	clientSpecified bool,
) (model.UUIDList, error) {
	if channel.Type.IsPrivate() || channel.Type != model.ChannelText {
		if clientSpecified && len(clientRoles) > 0 {
			return nil, errVisibleRolesTextOnly
		}
		return model.UUIDList{}, nil
	}
	if channel.ForceDefaultVisibility {
		return s.validateVisibleRoleIDs(channel.GuildID, channel.Type, []uuid.UUID(channel.DefaultVisibleRoleIDs))
	}
	if clientSpecified {
		if len(clientRoles) > 0 && !channel.AllowRestrictedVisibility {
			return nil, errVisibleRolesDisabled
		}
		return s.validateVisibleRoleIDs(channel.GuildID, channel.Type, clientRoles)
	}
	// 未指定：套用频道默认
	if len(channel.DefaultVisibleRoleIDs) > 0 {
		if !channel.AllowRestrictedVisibility {
			return model.UUIDList{}, nil
		}
		return s.validateVisibleRoleIDs(channel.GuildID, channel.Type, []uuid.UUID(channel.DefaultVisibleRoleIDs))
	}
	return model.UUIDList{}, nil
}

// validateVisibleRoleIDs 校验并归一化发送时的可见身份组：
//   - 仅服内 TEXT 可设；私信忽略（返回空）；
//   - 角色必须属于本服；去重；
//   - 若仅含 @everyone 角色，归一为公开（空切片）。
func (s *service) validateVisibleRoleIDs(guildID uuid.UUID, channelType model.ChannelType, raw []uuid.UUID) (model.UUIDList, error) {
	if channelType.IsPrivate() || len(raw) == 0 {
		return model.UUIDList{}, nil
	}
	if channelType != model.ChannelText {
		return model.UUIDList{}, errVisibleRolesTextOnly
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	ids := make([]uuid.UUID, 0, len(raw))
	for _, id := range raw {
		if id == uuid.Nil {
			return nil, errVisibleRoleInvalid
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return model.UUIDList{}, nil
	}
	var roles []model.Role
	if err := s.db.Where("guild_id = ? AND id IN ?", guildID, ids).Find(&roles).Error; err != nil {
		return nil, err
	}
	if len(roles) != len(ids) {
		return nil, errVisibleRoleInvalid
	}
	// 仅 @everyone → 等价公开
	if len(roles) == 1 && roles[0].IsEveryone {
		return model.UUIDList{}, nil
	}
	// 含 @everyone 与其它角色时去掉 everyone（其它角色已足够表达子集）
	out := make([]uuid.UUID, 0, len(roles))
	for _, role := range roles {
		if role.IsEveryone {
			continue
		}
		out = append(out, role.ID)
	}
	return model.UUIDList(out), nil
}

// userCanViewMessage 按 user_id 加载权限后判定（提及扇出 / 推送受众用）。
func (s *service) userCanViewMessage(userID uuid.UUID, msg model.Message) bool {
	if !isMessageRestricted(msg) {
		return true
	}
	if msg.AuthorID == userID {
		return true
	}
	if msg.GuildID == uuid.Nil {
		return true
	}
	// 快速路径：用户在可见名单
	for _, id := range msg.VisibleUserIDs {
		if id == userID {
			return true
		}
	}
	// 快速路径：持有任一可见角色
	if len(msg.VisibleRoleIDs) > 0 {
		var n int64
		err := s.db.Raw(`SELECT COUNT(*) FROM members m
			JOIN member_roles mr ON mr.member_id = m.id
			WHERE m.guild_id = ? AND m.user_id = ? AND mr.role_id IN ?`,
			msg.GuildID, userID, []uuid.UUID(msg.VisibleRoleIDs)).Scan(&n).Error
		if err == nil && n > 0 {
			return true
		}
	}
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return false
	}
	ctx, err := perms.LoadGuild(s.db, user, msg.GuildID)
	if err != nil {
		return false
	}
	if ctx.Owner || ctx.SystemAdmin {
		return true
	}
	_, bits, err := ctx.ChannelPerms(s.db, msg.ChannelID)
	if err != nil {
		return false
	}
	return rbac.Has(bits, rbac.ManageMessages)
}

// validateVisibleUserIDs 校验并归一化发送时的可见用户名单（须为本服成员）。
func (s *service) validateVisibleUserIDs(guildID uuid.UUID, channelType model.ChannelType, raw []uuid.UUID) (model.UUIDList, error) {
	if channelType.IsPrivate() || len(raw) == 0 {
		return model.UUIDList{}, nil
	}
	if channelType != model.ChannelText {
		return model.UUIDList{}, errVisibleRolesTextOnly
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	ids := make([]uuid.UUID, 0, len(raw))
	for _, id := range raw {
		if id == uuid.Nil {
			return nil, errVisibleUserInvalid
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return model.UUIDList{}, nil
	}
	if len(ids) > maxVisibleUsers {
		return nil, errVisibleUsersTooMany
	}
	var count int64
	if err := s.db.Model(&model.Member{}).
		Where("guild_id = ? AND user_id IN ?", guildID, ids).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if int(count) != len(ids) {
		return nil, errVisibleUserInvalid
	}
	return model.UUIDList(ids), nil
}

// resolveEffectiveVisibleUsers 合成客户端可见用户名单（频道 ForceDefault 时忽略客户端用户）。
func (s *service) resolveEffectiveVisibleUsers(
	channel model.Channel,
	clientUsers []uuid.UUID,
	clientSpecified bool,
) (model.UUIDList, error) {
	if channel.Type.IsPrivate() || channel.Type != model.ChannelText {
		if clientSpecified && len(clientUsers) > 0 {
			return nil, errVisibleRolesTextOnly
		}
		return model.UUIDList{}, nil
	}
	// 强制默认可见范围时不允许额外指定用户（与角色策略一致）
	if channel.ForceDefaultVisibility {
		return model.UUIDList{}, nil
	}
	if !clientSpecified {
		return model.UUIDList{}, nil
	}
	if len(clientUsers) > 0 && !channel.AllowRestrictedVisibility {
		return nil, errVisibleRolesDisabled
	}
	return s.validateVisibleUserIDs(channel.GuildID, channel.Type, clientUsers)
}

// resolveAudienceUserIDs 限定可见消息的推送受众：
// 频道可见成员中能 canView 该消息的用户（含作者、角色成员、MANAGE_MESSAGES、服主/系统管成员）。
// 公开消息返回 nil，表示走全频道广播。
func (s *service) resolveAudienceUserIDs(msg model.Message) []uuid.UUID {
	if !isMessageRestricted(msg) || msg.GuildID == uuid.Nil {
		return nil
	}
	viewers, err := snapshot.ChannelViewers(s.db, msg.GuildID, msg.ChannelID)
	if err != nil {
		// 降级：至少推给作者，避免作者自己收不到确认
		return []uuid.UUID{msg.AuthorID}
	}
	out := make([]uuid.UUID, 0, len(viewers))
	seen := make(map[uuid.UUID]struct{}, len(viewers))
	for _, uid := range viewers {
		if !s.userCanViewMessage(uid, msg) {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, uid)
	}
	// 作者应始终收到（即使极端情况下未出现在 viewers）
	if _, ok := seen[msg.AuthorID]; !ok {
		out = append(out, msg.AuthorID)
	}
	return out
}

// filterUsersWhoCanView 在候选用户中保留可看该消息者（提及计数用）。
func (s *service) filterUsersWhoCanView(userIDs []uuid.UUID, msg model.Message) []uuid.UUID {
	if !isMessageRestricted(msg) || len(userIDs) == 0 {
		return userIDs
	}
	out := make([]uuid.UUID, 0, len(userIDs))
	for _, id := range userIDs {
		if s.userCanViewMessage(id, msg) {
			out = append(out, id)
		}
	}
	return out
}
