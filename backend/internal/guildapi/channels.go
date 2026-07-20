package guildapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
	"github.com/owlspeak/owl-server/backend/internal/voice"
	"gorm.io/gorm"
)

type createChannelRequest struct {
	Name     string            `json:"name" binding:"required,min=1,max=100"`
	Type     model.ChannelType `json:"type" binding:"required,oneof=TEXT VOICE CATEGORY"`
	Topic    string            `json:"topic" binding:"omitempty,max=1024"`
	Position int               `json:"position" binding:"omitempty,gte=0"`
	// ParentID 所属分类（Owl-Desktop docs 03 FR-03）：仅 TEXT/VOICE 可设，
	// 必须指向本服 CATEGORY 频道。
	ParentID *uuid.UUID `json:"parent_id"`
	// UserLimit 语音频道人数上限（docs 09 FR-40）：0=不限，1–99。
	UserLimit int `json:"user_limit" binding:"omitempty,gte=0,lte=99"`
	// RateLimitPerUser 文本频道慢速模式秒数（docs 03 §8-9）：0=关闭，≤21600。
	RateLimitPerUser int `json:"rate_limit_per_user" binding:"omitempty,gte=0,lte=21600"`
}

// validateParentCategory 校验 parent_id 指向本服 CATEGORY 频道（分类自身不可再嵌套）。
func (h *api) validateParentCategory(c *gin.Context, guildID uuid.UUID, channelType model.ChannelType, parentID *uuid.UUID) bool {
	if parentID == nil {
		return true
	}
	if channelType == model.ChannelCategory {
		fail(c, http.StatusBadRequest, "CATEGORY_NO_NESTING", "分类频道不能再归属其他分类")
		return false
	}
	var parent model.Channel
	if err := h.deps.DB.First(&parent, "id = ? AND guild_id = ?", *parentID, guildID).Error; err != nil || parent.Type != model.ChannelCategory {
		fail(c, http.StatusBadRequest, "INVALID_PARENT", "parent_id 必须指向本服务器的分类频道")
		return false
	}
	return true
}

// createChannel POST /guilds/{gid}/channels（需 MANAGE_CHANNELS）→ CHANNEL_CREATE。
func (h *api) createChannel(c *gin.Context) {
	var input createChannelRequest
	if !bind(c, &input) {
		return
	}
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageChannels)
	if !ok {
		return
	}
	if !h.validateParentCategory(c, ctx.Guild.ID, input.Type, input.ParentID) {
		return
	}
	if input.UserLimit != 0 && input.Type != model.ChannelVoice {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_limit 仅语音频道可设")
		return
	}
	if input.RateLimitPerUser != 0 && input.Type != model.ChannelText {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "rate_limit_per_user 仅文本频道可设")
		return
	}
	channel := model.Channel{
		ID: uuid.New(), GuildID: ctx.Guild.ID,
		Name: strings.TrimSpace(input.Name), Type: input.Type,
		Topic: strings.TrimSpace(input.Topic), Position: input.Position,
		ParentID: input.ParentID, UserLimit: input.UserLimit,
		RateLimitPerUser: input.RateLimitPerUser,
	}
	if err := h.deps.DB.Create(&channel).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建频道失败")
		return
	}
	h.audit(ctx, user, "rbac.channel_create", "channel", channel.ID.String(), map[string]any{
		"name": channel.Name, "type": channel.Type,
	})
	guildID := ctx.Guild.ID
	// CHANNEL_CREATE 携带 ChannelID → hub 按 VIEW_CHANNEL 可见性过滤，仅推给可见成员。
	h.publish(eventbus.Event{
		Type: eventbus.EventChannelCreate, GuildID: &guildID, ChannelID: &channel.ID,
		Payload: snapshot.NewChannelPayload(h.deps.DB, channel),
	})
	c.JSON(http.StatusCreated, channel)
}

type updateChannelRequest struct {
	Name  *string `json:"name" binding:"omitempty,min=1,max=100"`
	Topic *string `json:"topic" binding:"omitempty,max=1024"`
	// ParentID 移动到分类（docs 03 FR-13 跨分类拖拽）；显式传 null 表示移出分类。
	// 用双指针区分「未携带」与「携带 null」。
	ParentID **uuid.UUID `json:"parent_id"`
	// UserLimit 语音频道人数上限（docs 09 FR-40）。
	UserLimit *int `json:"user_limit" binding:"omitempty,gte=0,lte=99"`
	// RateLimitPerUser 文本频道慢速模式秒数（docs 03 §8-9）。
	RateLimitPerUser *int `json:"rate_limit_per_user" binding:"omitempty,gte=0,lte=21600"`
}

// updateChannel PATCH /channels/{cid}（需 MANAGE_CHANNELS；无 VIEW_CHANNEL 一律 404）
// → 用 snapshot.NewChannelPayload 发 CHANNEL_UPDATE（按可见性过滤）。
// 语音模式/麦位等舞台配置走既有 PATCH /channels/{cid}/voice-stage（stage 模块）。
func (h *api) updateChannel(c *gin.Context) {
	ctx, user, channel, ok := h.channelCtx(c, rbac.ManageChannels)
	if !ok {
		return
	}
	var input updateChannelRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{}
	before := map[string]any{"name": channel.Name, "topic": channel.Topic}
	if input.Name != nil {
		channel.Name = strings.TrimSpace(*input.Name)
		if channel.Name == "" {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "频道名称不能为空")
			return
		}
		updates["name"] = channel.Name
	}
	if input.Topic != nil {
		channel.Topic = strings.TrimSpace(*input.Topic)
		updates["topic"] = channel.Topic
	}
	if input.ParentID != nil {
		if !h.validateParentCategory(c, ctx.Guild.ID, channel.Type, *input.ParentID) {
			return
		}
		channel.ParentID = *input.ParentID
		updates["parent_id"] = channel.ParentID
	}
	if input.UserLimit != nil {
		if channel.Type != model.ChannelVoice {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_limit 仅语音频道可设")
			return
		}
		channel.UserLimit = *input.UserLimit
		updates["user_limit"] = channel.UserLimit
	}
	if input.RateLimitPerUser != nil {
		if channel.Type != model.ChannelText {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "rate_limit_per_user 仅文本频道可设")
			return
		}
		channel.RateLimitPerUser = *input.RateLimitPerUser
		updates["rate_limit_per_user"] = channel.RateLimitPerUser
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "没有可更新的字段")
		return
	}
	if err := h.deps.DB.Model(&model.Channel{}).Where("id = ?", channel.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新频道失败")
		return
	}
	h.audit(ctx, user, "rbac.channel_update", "channel", channel.ID.String(), map[string]any{
		"before": before, "after": map[string]any{"name": channel.Name, "topic": channel.Topic},
	})
	guildID := ctx.Guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventChannelUpdate, GuildID: &guildID, ChannelID: &channel.ID,
		Payload: snapshot.NewChannelPayload(h.deps.DB, channel),
	})
	c.JSON(http.StatusOK, channel)
}

// deleteChannel DELETE /channels/{cid}（需 MANAGE_CHANNELS）→ CHANNEL_DELETE。
// 语音频道内有人时先经 voice 模块断开（复用管理踢出语音的会话终止路径）。
// 事件定向发给删除前可见该频道的成员（频道已删，无法再按 ChannelID 过滤）。
func (h *api) deleteChannel(c *gin.Context) {
	ctx, user, channel, ok := h.channelCtx(c, rbac.ManageChannels)
	if !ok {
		return
	}
	guildID := ctx.Guild.ID
	viewers, _ := snapshot.ChannelViewers(h.deps.DB, guildID, channel.ID)
	// 分类被删除时子频道上浮（docs 03：parent 置空，频道保留）；先收集用于事后广播。
	var children []model.Channel
	if channel.Type == model.ChannelCategory {
		h.deps.DB.Where("guild_id = ? AND parent_id = ?", guildID, channel.ID).Find(&children)
	}
	// 语音频道联动：断开房内全部用户（SFU 踢出 + VOICE_STATE_UPDATE）。
	voice.DisconnectChannelUsers(guildID, channel.ID, "CHANNEL_DELETE")
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Channel{}).Where("guild_id = ? AND parent_id = ?", guildID, channel.ID).
			Update("parent_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.ChannelOverwrite{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.StageChannelConfig{}).Error; err != nil {
			return err
		}
		// 兜底清理残余语音状态行（voice 模块未装配的场景）。
		if err := tx.Model(&model.VoiceState{}).Where("channel_id = ?", channel.ID).
			Updates(map[string]any{"channel_id": nil, "node_id": nil, "room_id": nil, "voice_session_id": nil, "connected": false, "joined_at": nil}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Channel{}, "id = ?", channel.ID).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除频道失败")
		return
	}
	h.audit(ctx, user, "rbac.channel_delete", "channel", channel.ID.String(), map[string]any{
		"name": channel.Name, "type": channel.Type,
	})
	if len(viewers) > 0 {
		h.publish(eventbus.Event{
			Type: eventbus.EventChannelDelete, GuildID: &guildID, UserIDs: viewers,
			Payload: eventbus.NewChannelDeletePayload(guildID, channel.ID),
		})
	}
	// 上浮后的子频道逐个广播 CHANNEL_UPDATE（parent_id 已置空）。
	for _, child := range children {
		child.ParentID = nil
		childID := child.ID
		h.publish(eventbus.Event{
			Type: eventbus.EventChannelUpdate, GuildID: &guildID, ChannelID: &childID,
			Payload: snapshot.NewChannelPayload(h.deps.DB, child),
		})
	}
	c.Status(http.StatusNoContent)
}

type channelPositionEntry struct {
	ID       uuid.UUID `json:"id" binding:"required"`
	Position int       `json:"position" binding:"gte=0"`
}

// reorderChannels PATCH /guilds/{gid}/channels（需 MANAGE_CHANNELS）：批量排序。
// body 为 [{id, position}] 数组；全部频道必须属于本服，事务内整体生效。
// 每个被移动的频道发一条 CHANNEL_UPDATE（按可见性过滤）。
func (h *api) reorderChannels(c *gin.Context) {
	var input []channelPositionEntry
	if err := c.ShouldBindJSON(&input); err != nil || len(input) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "需要非空的 [{id, position}] 数组")
		return
	}
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageChannels)
	if !ok {
		return
	}
	guildID := ctx.Guild.ID
	moved := make([]model.Channel, 0, len(input))
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		for _, entry := range input {
			var channel model.Channel
			if err := tx.First(&channel, "id = ? AND guild_id = ?", entry.ID, guildID).Error; err != nil {
				return err
			}
			if channel.Position == entry.Position {
				continue
			}
			if err := tx.Model(&model.Channel{}).Where("id = ?", channel.ID).Update("position", entry.Position).Error; err != nil {
				return err
			}
			channel.Position = entry.Position
			moved = append(moved, channel)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "NOT_FOUND", "存在不属于本服务器的频道")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道排序失败")
		return
	}
	positions := map[string]any{}
	for _, channel := range moved {
		positions[channel.ID.String()] = channel.Position
	}
	h.audit(ctx, user, "rbac.channel_reorder", "guild", guildID.String(), map[string]any{"positions": positions})
	for _, channel := range moved {
		channelID := channel.ID
		h.publish(eventbus.Event{
			Type: eventbus.EventChannelUpdate, GuildID: &guildID, ChannelID: &channelID,
			Payload: snapshot.NewChannelPayload(h.deps.DB, channel),
		})
	}
	c.Status(http.StatusNoContent)
}

type overwriteRequest struct {
	Type  model.OverwriteType `json:"type" binding:"required,oneof=ROLE MEMBER"`
	Allow int64               `json:"allow"`
	Deny  int64               `json:"deny"`
}

// upsertOverwrite PUT /guilds/{gid}/channels/{cid}/overwrites/{targetID}
//（需 MANAGE_ROLES + 防提权：不能授予超过自身的权限位）。
func (h *api) upsertOverwrite(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	channel, ok := h.overwriteChannel(c, ctx)
	if !ok {
		return
	}
	h.applyOverwrite(c, ctx, user, channel)
}

// upsertOverwriteByChannel PUT /channels/{cid}/overwrites/{targetID}：顶级频道入口，
// 由频道反查 guild；无 VIEW_CHANNEL 一律 404，可见但无 MANAGE_ROLES 返回 403。
func (h *api) upsertOverwriteByChannel(c *gin.Context) {
	ctx, user, channel, ok := h.channelCtx(c, rbac.ManageRoles)
	if !ok {
		return
	}
	h.applyOverwrite(c, ctx, user, channel)
}

// applyOverwrite 覆盖 upsert 的共享核心（guild 前缀与顶级频道入口共用）：
// 请求校验（allow/deny 冲突、防提权）→ 目标归属校验 → 落库 → 审计 + 事件。
func (h *api) applyOverwrite(c *gin.Context, ctx *perms.GuildContext, user model.User, channel model.Channel) {
	var input overwriteRequest
	if !bind(c, &input) {
		return
	}
	requested := permissionMask(input.Allow)
	denied := permissionMask(input.Deny)
	if requested&denied != 0 {
		fail(c, http.StatusBadRequest, "OVERWRITE_CONFLICT", "同一权限位不能同时 allow 和 deny")
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(requested) {
		fail(c, http.StatusForbidden, "CANNOT_GRANT_PERMISSION", "不能授予超过自身的权限")
		return
	}
	targetID, ok := h.overwriteTargetID(c, ctx, input.Type)
	if !ok {
		return
	}
	// 变更前先算一次可见成员集合，事后对比得到「获得/失去可见性」的用户（docs 14 FR-15/FR-17）。
	var viewersBefore []uuid.UUID
	if h.deps.Bus != nil {
		viewersBefore, _ = snapshot.ChannelViewers(h.deps.DB, ctx.Guild.ID, channel.ID)
	}
	overwrite := model.ChannelOverwrite{ID: uuid.New(), ChannelID: channel.ID, Type: input.Type, TargetID: targetID, Allow: input.Allow, Deny: input.Deny}
	err := h.deps.DB.Where(model.ChannelOverwrite{ChannelID: channel.ID, Type: input.Type, TargetID: targetID}).
		Assign(map[string]any{"allow": input.Allow, "deny": input.Deny}).FirstOrCreate(&overwrite).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道覆盖失败")
		return
	}
	h.audit(ctx, user, "rbac.channel_overwrite_update", "channel", channel.ID.String(), map[string]any{
		"target_type": input.Type, "target_id": targetID, "allow": input.Allow, "deny": input.Deny,
	})
	h.publishOverwriteEvents(ctx.Guild.ID, channel, viewersBefore)
	c.JSON(http.StatusOK, overwrite)
}

// deleteOverwrite DELETE /guilds/{gid}/channels/{cid}/overwrites/{targetID}?type=ROLE|MEMBER
//（需 MANAGE_ROLES）→ PERMISSIONS_UPDATE + 可见性增减定向事件（复用 upsert 的 diff 逻辑）。
// type 缺省时删除该目标的全部覆盖记录（ROLE 与 MEMBER 目标 ID 空间不同，实际最多一条）。
func (h *api) deleteOverwrite(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	channel, ok := h.overwriteChannel(c, ctx)
	if !ok {
		return
	}
	h.removeOverwrite(c, ctx, user, channel)
}

// deleteOverwriteByChannel DELETE /channels/{cid}/overwrites/{targetID}：顶级频道入口。
func (h *api) deleteOverwriteByChannel(c *gin.Context) {
	ctx, user, channel, ok := h.channelCtx(c, rbac.ManageRoles)
	if !ok {
		return
	}
	h.removeOverwrite(c, ctx, user, channel)
}

// removeOverwrite 覆盖删除的共享核心（guild 前缀与顶级频道入口共用）。
func (h *api) removeOverwrite(c *gin.Context, ctx *perms.GuildContext, user model.User, channel model.Channel) {
	targetID, err := uuid.Parse(c.Param("targetID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_ID", "目标 ID 无效")
		return
	}
	query := h.deps.DB.Where("channel_id = ? AND target_id = ?", channel.ID, targetID)
	if raw := c.Query("type"); raw != "" {
		if raw != string(model.OverwriteRole) && raw != string(model.OverwriteMember) {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "type 只能是 ROLE 或 MEMBER")
			return
		}
		query = query.Where("type = ?", raw)
	}
	var viewersBefore []uuid.UUID
	if h.deps.Bus != nil {
		viewersBefore, _ = snapshot.ChannelViewers(h.deps.DB, ctx.Guild.ID, channel.ID)
	}
	result := query.Delete(&model.ChannelOverwrite{})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除频道覆盖失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "覆盖记录不存在")
		return
	}
	h.audit(ctx, user, "rbac.channel_overwrite_delete", "channel", channel.ID.String(), map[string]any{
		"target_id": targetID,
	})
	h.publishOverwriteEvents(ctx.Guild.ID, channel, viewersBefore)
	c.Status(http.StatusNoContent)
}

// overwriteChannel 解析 guild 前缀路由的 channelID 并校验归属本服。
func (h *api) overwriteChannel(c *gin.Context, ctx *perms.GuildContext) (model.Channel, bool) {
	var channel model.Channel
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_ID", "频道 ID 无效")
		return channel, false
	}
	if h.deps.DB.First(&channel, "id = ? AND guild_id = ?", channelID, ctx.Guild.ID).Error != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return channel, false
	}
	return channel, true
}

// overwriteTargetID 解析并校验覆盖目标（角色/成员须属于本服）。
func (h *api) overwriteTargetID(c *gin.Context, ctx *perms.GuildContext, targetType model.OverwriteType) (uuid.UUID, bool) {
	targetID, err := uuid.Parse(c.Param("targetID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_ID", "目标 ID 无效")
		return uuid.Nil, false
	}
	var count int64
	if targetType == model.OverwriteRole {
		h.deps.DB.Model(&model.Role{}).Where("id = ? AND guild_id = ?", targetID, ctx.Guild.ID).Count(&count)
	} else {
		h.deps.DB.Model(&model.Member{}).Where("id = ? AND guild_id = ?", targetID, ctx.Guild.ID).Count(&count)
	}
	if count != 1 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "覆盖目标不存在")
		return uuid.Nil, false
	}
	return targetID, true
}

// publishOverwriteEvents 权限覆盖变更后的事件下发：
//  1. PERMISSIONS_UPDATE 按 guild 广播（不带 ChannelID 过滤——失去可见性的用户也必须收到；
//     客户端据此失效权限投影并重拉频道列表）；
//  2. 因本次变更获得 VIEW_CHANNEL 的用户，定向补发 CHANNEL_CREATE 式频道快照；
//     失去 VIEW_CHANNEL 的用户，定向下发 CHANNEL_DELETE 式通知（docs 14 FR-15/FR-17）。
func (h *api) publishOverwriteEvents(guildID uuid.UUID, channel model.Channel, viewersBefore []uuid.UUID) {
	if h.deps.Bus == nil {
		return
	}
	h.publish(eventbus.Event{
		Type: eventbus.EventPermissionsUpdate, GuildID: &guildID,
		Payload: eventbus.NewPermissionsUpdatePayload(guildID, channel.ID),
	})
	viewersAfter, err := snapshot.ChannelViewers(h.deps.DB, guildID, channel.ID)
	if err != nil {
		return
	}
	before := make(map[uuid.UUID]bool, len(viewersBefore))
	for _, id := range viewersBefore {
		before[id] = true
	}
	after := make(map[uuid.UUID]bool, len(viewersAfter))
	var gained []uuid.UUID
	for _, id := range viewersAfter {
		after[id] = true
		if !before[id] {
			gained = append(gained, id)
		}
	}
	var lost []uuid.UUID
	for _, id := range viewersBefore {
		if !after[id] {
			lost = append(lost, id)
		}
	}
	if len(gained) > 0 {
		h.publish(eventbus.Event{
			Type: eventbus.EventChannelCreate, GuildID: &guildID, UserIDs: gained,
			Payload: snapshot.NewChannelPayload(h.deps.DB, channel),
		})
	}
	if len(lost) > 0 {
		h.publish(eventbus.Event{
			Type: eventbus.EventChannelDelete, GuildID: &guildID, UserIDs: lost,
			Payload: eventbus.NewChannelDeletePayload(guildID, channel.ID),
		})
	}
}
