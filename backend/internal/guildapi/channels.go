package guildapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"github.com/newtspeak/newt-server/backend/internal/snapshot"
	"github.com/newtspeak/newt-server/backend/internal/voice"
	"gorm.io/gorm"
)

// errInvalidParent 批量排序时 parent_id 非法（事务内哨兵，映射为 400）。
var errInvalidParent = errors.New("invalid parent category")

type createChannelRequest struct {
	Name     string            `json:"name" binding:"required,min=1,max=100"`
	Type     model.ChannelType `json:"type" binding:"required,oneof=TEXT VOICE CATEGORY"`
	Topic    string            `json:"topic" binding:"omitempty,max=1024"`
	Position int               `json:"position" binding:"omitempty,gte=0"`
	// ParentID 所属分类（Newt-Desktop docs 03 FR-03）：仅 TEXT/VOICE 可设，
	// 必须指向本服 CATEGORY 频道。
	ParentID *uuid.UUID `json:"parent_id"`
	// UserLimit 语音频道人数上限（docs 09 FR-40）：0=不限，1–99。
	UserLimit int `json:"user_limit" binding:"omitempty,gte=0,lte=99"`
	// RateLimitPerUser 文本频道慢速模式秒数（docs 03 §8-9）：0=关闭，≤21600。
	RateLimitPerUser int `json:"rate_limit_per_user" binding:"omitempty,gte=0,lte=21600"`
	// RateLimitExemptRoleIDs 慢速模式豁免角色（为空表示不豁免任何角色）。
	RateLimitExemptRoleIDs []uuid.UUID `json:"rate_limit_exempt_role_ids"`
	// Password 频道访问密码（TEXT/VOICE）；设置后频道上锁，访问内容需先解锁。
	Password string `json:"password" binding:"omitempty,min=1,max=64"`
	// VoiceNote 语音频道活动注释（≤200）。
	VoiceNote string `json:"voice_note" binding:"omitempty,max=200"`
	// Private 私密频道：对 @everyone deny VIEW_CHANNEL，并对 VisibleRoleIDs allow。
	Private bool `json:"private"`
	// VisibleRoleIDs 私密频道可见角色（本服 role id，不含 @everyone）。
	VisibleRoleIDs []uuid.UUID `json:"visible_role_ids"`
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

// validateRateLimitExemptRoles 校验慢速模式豁免角色均属于当前服务器，并去重。
func (h *api) validateRateLimitExemptRoles(c *gin.Context, guildID uuid.UUID, channelType model.ChannelType, roleIDs []uuid.UUID) ([]uuid.UUID, bool) {
	if len(roleIDs) == 0 {
		return []uuid.UUID{}, true
	}
	if channelType != model.ChannelText {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "rate_limit_exempt_role_ids 仅文本频道可设")
		return nil, false
	}
	seen := make(map[uuid.UUID]struct{}, len(roleIDs))
	unique := make([]uuid.UUID, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		if roleID == uuid.Nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "rate_limit_exempt_role_ids 含无效角色")
			return nil, false
		}
		if _, exists := seen[roleID]; exists {
			continue
		}
		seen[roleID] = struct{}{}
		unique = append(unique, roleID)
	}
	var count int64
	if err := h.deps.DB.Model(&model.Role{}).Where("guild_id = ? AND id IN ?", guildID, unique).Count(&count).Error; err != nil || count != int64(len(unique)) {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "rate_limit_exempt_role_ids 含无效角色")
		return nil, false
	}
	return unique, true
}

// validateVisibleRoleIDsForChannel 校验频道默认可见身份组（须为本服角色，可含 @everyone）。
func (h *api) validateVisibleRoleIDsForChannel(c *gin.Context, guildID uuid.UUID, roleIDs []uuid.UUID) ([]uuid.UUID, bool) {
	if len(roleIDs) == 0 {
		return []uuid.UUID{}, true
	}
	seen := make(map[uuid.UUID]struct{}, len(roleIDs))
	unique := make([]uuid.UUID, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		if roleID == uuid.Nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "default_visible_role_ids 含无效角色")
			return nil, false
		}
		if _, exists := seen[roleID]; exists {
			continue
		}
		seen[roleID] = struct{}{}
		unique = append(unique, roleID)
	}
	var count int64
	if err := h.deps.DB.Model(&model.Role{}).Where("guild_id = ? AND id IN ?", guildID, unique).Count(&count).Error; err != nil || count != int64(len(unique)) {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "default_visible_role_ids 含无效角色")
		return nil, false
	}
	return unique, true
}

// getChannel GET /channels/{cid}：单频道详情；无 VIEW_CHANNEL 一律 404（防扫频）。
// 对齐 Discord Get Channel。
func (h *api) getChannel(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, channel.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	channel, bits, err := ctx.ChannelPerms(h.deps.DB, channel.ID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	unlocked := perms.IsChannelUnlocked(h.deps.DB, user.ID, channel, ctx, bits)
	c.JSON(http.StatusOK, gin.H{
		"channel":     channel,
		"permissions": maskString(bits),
		"locked":      channel.PasswordHash != "",
		"unlocked":    unlocked,
	})
}

// getChannelByGuild GET /guilds/{gid}/channels/{cid}：guild 前缀下的单频道详情。
func (h *api) getChannelByGuild(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	channel, bits, err := ctx.ChannelPerms(h.deps.DB, channelID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	unlocked := perms.IsChannelUnlocked(h.deps.DB, user.ID, channel, ctx, bits)
	c.JSON(http.StatusOK, gin.H{
		"channel":     channel,
		"permissions": maskString(bits),
		"locked":      channel.PasswordHash != "",
		"unlocked":    unlocked,
	})
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
	exemptRoleIDs, ok := h.validateRateLimitExemptRoles(c, ctx.Guild.ID, input.Type, input.RateLimitExemptRoleIDs)
	if !ok {
		return
	}
	if input.Password != "" && input.Type == model.ChannelCategory {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "分类不能设置访问密码")
		return
	}
	if strings.TrimSpace(input.VoiceNote) != "" && input.Type != model.ChannelVoice {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "voice_note 仅语音频道可设")
		return
	}
	if input.Private && input.Type == model.ChannelCategory {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "分类不支持私密频道开关")
		return
	}
	// 未显式指定 position 时，追加到本服末尾（避免新建频道全为 0 导致排序不稳定）。
	position := input.Position
	if position == 0 {
		var maxPosition int
		_ = h.deps.DB.Model(&model.Channel{}).Where("guild_id = ?", ctx.Guild.ID).
			Select("COALESCE(MAX(position), -1)").Scan(&maxPosition)
		position = maxPosition + 1
	}
	passwordHash := ""
	if input.Password != "" {
		hash, err := security.HashChannelPassword(input.Password)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		passwordHash = hash
	}
	voiceNote := ""
	if input.Type == model.ChannelVoice {
		voiceNote = strings.TrimSpace(input.VoiceNote)
	}
	channel := model.Channel{
		ID: uuid.New(), GuildID: ctx.Guild.ID,
		Name: strings.TrimSpace(input.Name), Type: input.Type,
		Topic: strings.TrimSpace(input.Topic), Position: position,
		ParentID: input.ParentID, UserLimit: input.UserLimit,
		RateLimitPerUser:       input.RateLimitPerUser,
		RateLimitExemptRoleIDs: model.UUIDList(exemptRoleIDs),
		// 文本频道默认允许限定可见消息（bool 零值为 false，须显式 true）。
		AllowRestrictedVisibility: input.Type == model.ChannelText,
		DefaultVisibleRoleIDs:     model.UUIDList{},
		PasswordHash:              passwordHash,
		VoiceNote:                 voiceNote,
	}
	channel.SyncLocked()

	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		if input.Private && (input.Type == model.ChannelText || input.Type == model.ChannelVoice) {
			if err := applyPrivateVisibility(tx, ctx.Guild.ID, channel.ID, input.VisibleRoleIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errInvalidVisibleRole) {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "visible_role_ids 含无效角色")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建频道失败")
		return
	}
	h.audit(ctx, user, "rbac.channel_create", "channel", channel.ID.String(), map[string]any{
		"name": channel.Name, "type": channel.Type, "locked": channel.Locked, "private": input.Private,
	})
	guildID := ctx.Guild.ID
	// CHANNEL_CREATE 携带 ChannelID → hub 按 VIEW_CHANNEL 可见性过滤，仅推给可见成员。
	h.publish(eventbus.Event{
		Type: eventbus.EventChannelCreate, GuildID: &guildID, ChannelID: &channel.ID,
		Payload: snapshot.NewChannelPayload(h.deps.DB, channel),
	})
	c.JSON(http.StatusCreated, channel)
}

var errInvalidVisibleRole = errors.New("invalid visible role")

// applyPrivateVisibility 私密频道初始 overwrite：@everyone deny VIEW；指定角色 allow VIEW。
func applyPrivateVisibility(tx *gorm.DB, guildID, channelID uuid.UUID, roleIDs []uuid.UUID) error {
	var everyone model.Role
	if err := tx.First(&everyone, "guild_id = ? AND is_everyone = true", guildID).Error; err != nil {
		return err
	}
	view := int64(rbac.ViewChannel)
	if err := tx.Create(&model.ChannelOverwrite{
		ID: uuid.New(), ChannelID: channelID, Type: model.OverwriteRole,
		TargetID: everyone.ID, Allow: 0, Deny: view,
	}).Error; err != nil {
		return err
	}
	seen := map[uuid.UUID]struct{}{}
	for _, roleID := range roleIDs {
		if roleID == everyone.ID {
			continue
		}
		if _, ok := seen[roleID]; ok {
			continue
		}
		seen[roleID] = struct{}{}
		var role model.Role
		if err := tx.First(&role, "id = ? AND guild_id = ?", roleID, guildID).Error; err != nil {
			return errInvalidVisibleRole
		}
		if err := tx.Create(&model.ChannelOverwrite{
			ID: uuid.New(), ChannelID: channelID, Type: model.OverwriteRole,
			TargetID: role.ID, Allow: view, Deny: 0,
		}).Error; err != nil {
			return err
		}
	}
	return nil
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
	// RateLimitExemptRoleIDs 显式传空数组可清空豁免角色。
	RateLimitExemptRoleIDs *[]uuid.UUID `json:"rate_limit_exempt_role_ids"`
	// AllowRestrictedVisibility 是否允许限定可见消息（仅 TEXT）。
	AllowRestrictedVisibility *bool `json:"allow_restricted_visibility"`
	// DefaultVisibleRoleIDs 默认可见身份组；显式 [] 清空。
	DefaultVisibleRoleIDs *[]uuid.UUID `json:"default_visible_role_ids"`
	// ForceDefaultVisibility 强制使用默认可见范围。
	ForceDefaultVisibility *bool `json:"force_default_visibility"`
	// Password 设置/更换访问密码；与 Locked=false 同时时以关锁为准。
	Password *string `json:"password" binding:"omitempty,min=1,max=64"`
	// Locked 显式关锁（false 清空密码）；true  alone 表示保持/需要密码字段新建锁。
	Locked *bool `json:"locked"`
	// VoiceNote 语音频道活动注释；传空串清空。
	VoiceNote *string `json:"voice_note" binding:"omitempty,max=200"`
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
	before := map[string]any{
		"name": channel.Name, "topic": channel.Topic,
		"locked": channel.Locked, "voice_note": channel.VoiceNote,
	}
	revokeUnlocks := false
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
	if input.RateLimitExemptRoleIDs != nil {
		exemptRoleIDs, valid := h.validateRateLimitExemptRoles(c, ctx.Guild.ID, channel.Type, *input.RateLimitExemptRoleIDs)
		if !valid {
			return
		}
		channel.RateLimitExemptRoleIDs = model.UUIDList(exemptRoleIDs)
		updates["rate_limit_exempt_role_ids"] = channel.RateLimitExemptRoleIDs
	}
	if input.AllowRestrictedVisibility != nil {
		if channel.Type != model.ChannelText {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "allow_restricted_visibility 仅文本频道可设")
			return
		}
		channel.AllowRestrictedVisibility = *input.AllowRestrictedVisibility
		updates["allow_restricted_visibility"] = channel.AllowRestrictedVisibility
	}
	if input.DefaultVisibleRoleIDs != nil {
		if channel.Type != model.ChannelText {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "default_visible_role_ids 仅文本频道可设")
			return
		}
		roleIDs, valid := h.validateVisibleRoleIDsForChannel(c, ctx.Guild.ID, *input.DefaultVisibleRoleIDs)
		if !valid {
			return
		}
		channel.DefaultVisibleRoleIDs = model.UUIDList(roleIDs)
		updates["default_visible_role_ids"] = channel.DefaultVisibleRoleIDs
	}
	if input.ForceDefaultVisibility != nil {
		if channel.Type != model.ChannelText {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "force_default_visibility 仅文本频道可设")
			return
		}
		channel.ForceDefaultVisibility = *input.ForceDefaultVisibility
		updates["force_default_visibility"] = channel.ForceDefaultVisibility
	}
	if input.VoiceNote != nil {
		if channel.Type != model.ChannelVoice {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "voice_note 仅语音频道可设")
			return
		}
		channel.VoiceNote = strings.TrimSpace(*input.VoiceNote)
		updates["voice_note"] = channel.VoiceNote
	}
	// 密码锁：Locked=false 关锁；Password 设/改密；Locked=true 且当前未锁时必须带 Password。
	if input.Locked != nil && !*input.Locked {
		if channel.PasswordHash != "" {
			channel.PasswordHash = ""
			updates["password_hash"] = ""
			revokeUnlocks = true
		}
	} else if input.Password != nil {
		if channel.Type == model.ChannelCategory {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "分类不能设置访问密码")
			return
		}
		hash, err := security.HashChannelPassword(*input.Password)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		channel.PasswordHash = hash
		updates["password_hash"] = hash
		revokeUnlocks = true
	} else if input.Locked != nil && *input.Locked && channel.PasswordHash == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "上锁时必须提供 password")
		return
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "没有可更新的字段")
		return
	}
	if err := h.deps.DB.Model(&model.Channel{}).Where("id = ?", channel.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新频道失败")
		return
	}
	if revokeUnlocks {
		_ = perms.RevokeChannelUnlocks(h.deps.DB, channel.ID)
	}
	channel.SyncLocked()
	h.audit(ctx, user, "rbac.channel_update", "channel", channel.ID.String(), map[string]any{
		"before": before,
		"after": map[string]any{
			"name": channel.Name, "topic": channel.Topic,
			"locked": channel.Locked, "voice_note": channel.VoiceNote,
		},
	})
	guildID := ctx.Guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventChannelUpdate, GuildID: &guildID, ChannelID: &channel.ID,
		Payload: snapshot.NewChannelPayload(h.deps.DB, channel),
	})
	c.JSON(http.StatusOK, channel)
}

type unlockChannelRequest struct {
	Password string `json:"password" binding:"required,min=1,max=64"`
}

// unlockChannel POST /channels/{cid}/unlock：输入正确密码后写入解锁记录。
// 无 VIEW_CHANNEL → 404；未上锁 → 200 幂等；密码错误 → 403。
func (h *api) unlockChannel(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if channel.Type.IsPrivate() || channel.Type == model.ChannelCategory {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, channel.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	channel, bits, err := ctx.ChannelPerms(h.deps.DB, channel.ID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if channel.PasswordHash == "" {
		c.JSON(http.StatusOK, gin.H{"channel_id": channel.ID, "unlocked": true, "already": true})
		return
	}
	if perms.IsChannelUnlocked(h.deps.DB, user.ID, channel, ctx, bits) {
		c.JSON(http.StatusOK, gin.H{"channel_id": channel.ID, "unlocked": true, "already": true})
		return
	}
	var input unlockChannelRequest
	if !bind(c, &input) {
		return
	}
	if !security.VerifyPassword(channel.PasswordHash, input.Password) {
		fail(c, http.StatusForbidden, "CHANNEL_PASSWORD_INCORRECT", "频道密码错误")
		return
	}
	rec := model.ChannelUnlock{
		ID: uuid.New(), ChannelID: channel.ID, UserID: user.ID, UnlockedAt: time.Now().UTC(),
	}
	// 唯一索引冲突时视为已解锁（并发双点）。
	if err := h.deps.DB.Where("channel_id = ? AND user_id = ?", channel.ID, user.ID).
		Assign(model.ChannelUnlock{UnlockedAt: rec.UnlockedAt}).
		FirstOrCreate(&rec).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解锁失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel_id": channel.ID, "unlocked": true})
}

// unlockStatus GET /channels/{cid}/unlock-status：当前用户是否已解锁（可见频道）。
func (h *api) unlockStatus(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, channel.GuildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	channel, bits, err := ctx.ChannelPerms(h.deps.DB, channel.ID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	unlocked := perms.IsChannelUnlocked(h.deps.DB, user.ID, channel, ctx, bits)
	c.JSON(http.StatusOK, gin.H{
		"channel_id": channel.ID,
		"locked":     channel.PasswordHash != "",
		"unlocked":   unlocked,
	})
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
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.ChannelUnlock{}).Error; err != nil {
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
		// 若本频道是服务器默认着陆频道，一并置空（避免悬空引用）。
		if ctx.Guild.DefaultChannelID != nil && *ctx.Guild.DefaultChannelID == channel.ID {
			if err := tx.Model(&model.Guild{}).Where("id = ?", guildID).
				Update("default_channel_id", nil).Error; err != nil {
				return err
			}
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

// channelPositionEntry 批量排序条目（Newt-Desktop docs 03 FR-12）。
// ParentID：JSON null / 省略均表示根级（无分类）；分类频道强制 parent_id=null。
// 客户端提交「受影响项」或「全量可见频道」均可；事务内原子写入。
type channelPositionEntry struct {
	ID       uuid.UUID `json:"id" binding:"required"`
	Position int       `json:"position" binding:"gte=0"`
	// ParentID 所属分类；nil = 根级。跨分类拖拽时一并更新。
	ParentID *uuid.UUID `json:"parent_id"`
}

// parentIDEqual 比较两个 *uuid.UUID（含双方 nil）。
func parentIDEqual(a, b *uuid.UUID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// reorderChannels PATCH /guilds/{gid}/channels（需 MANAGE_CHANNELS）：批量排序 + 跨分类移动。
// body 为 [{id, position, parent_id?}] 数组；全部频道必须属于本服，事务内整体生效。
// 每个实际变更的频道发一条 CHANNEL_UPDATE（含最新 position / parent_id，按可见性过滤），
// 供所有在线客户端实时同步列表顺序。
func (h *api) reorderChannels(c *gin.Context) {
	var input []channelPositionEntry
	if err := c.ShouldBindJSON(&input); err != nil || len(input) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "需要非空的 [{id, position, parent_id?}] 数组")
		return
	}
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageChannels)
	if !ok {
		return
	}
	guildID := ctx.Guild.ID

	// 预校验 parent_id：指向本服 CATEGORY，且分类自身不可嵌套。
	for _, entry := range input {
		if entry.ParentID == nil {
			continue
		}
		if *entry.ParentID == entry.ID {
			fail(c, http.StatusBadRequest, "INVALID_PARENT", "频道不能以自身为分类")
			return
		}
	}

	moved := make([]model.Channel, 0, len(input))
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		// 缓存本服 CATEGORY id，避免循环内 N+1。
		var categories []model.Channel
		if err := tx.Select("id").Where("guild_id = ? AND type = ?", guildID, model.ChannelCategory).
			Find(&categories).Error; err != nil {
			return err
		}
		categorySet := make(map[uuid.UUID]struct{}, len(categories))
		for _, cat := range categories {
			categorySet[cat.ID] = struct{}{}
		}

		for _, entry := range input {
			var channel model.Channel
			if err := tx.First(&channel, "id = ? AND guild_id = ?", entry.ID, guildID).Error; err != nil {
				return err
			}
			// 分类不可再挂 parent。
			parentID := entry.ParentID
			if channel.Type == model.ChannelCategory {
				parentID = nil
			} else if parentID != nil {
				if _, ok := categorySet[*parentID]; !ok {
					return errInvalidParent
				}
			}

			changed := channel.Position != entry.Position || !parentIDEqual(channel.ParentID, parentID)
			if !changed {
				continue
			}
			// Select 强制写入 parent_id=NULL（GORM Updates 默认跳过 nil 字段）。
			if err := tx.Model(&model.Channel{}).Where("id = ?", channel.ID).
				Select("position", "parent_id").
				Updates(map[string]any{
					"position":  entry.Position,
					"parent_id": parentID,
				}).Error; err != nil {
				return err
			}
			channel.Position = entry.Position
			channel.ParentID = parentID
			moved = append(moved, channel)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "NOT_FOUND", "存在不属于本服务器的频道")
			return
		}
		if errors.Is(err, errInvalidParent) {
			fail(c, http.StatusBadRequest, "INVALID_PARENT", "parent_id 必须指向本服务器的分类频道")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道排序失败")
		return
	}
	detail := make([]map[string]any, 0, len(moved))
	for _, channel := range moved {
		item := map[string]any{"id": channel.ID.String(), "position": channel.Position}
		if channel.ParentID != nil {
			item["parent_id"] = channel.ParentID.String()
		} else {
			item["parent_id"] = nil
		}
		detail = append(detail, item)
	}
	h.audit(ctx, user, "rbac.channel_reorder", "guild", guildID.String(), map[string]any{"items": detail})
	// 逐频道 CHANNEL_UPDATE：客户端 upsert 后按 position/parent_id 重排。
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
// （需 MANAGE_ROLES + 防提权：不能授予超过自身的权限位）。
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
	var prev model.ChannelOverwrite
	hadPrev := h.deps.DB.Where("channel_id = ? AND type = ? AND target_id = ?", channel.ID, input.Type, targetID).First(&prev).Error == nil
	before := map[string]any{"target_id": targetID, "type": input.Type, "target_type": input.Type}
	if hadPrev {
		before["id"] = prev.ID
		before["allow"] = prev.Allow
		before["deny"] = prev.Deny
		before["created"] = false
	} else {
		before["created"] = true
		before["allow"] = int64(0)
		before["deny"] = int64(0)
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
		"before": before,
		"after":  map[string]any{"allow": input.Allow, "deny": input.Deny, "target_id": targetID, "type": input.Type},
	})
	h.publishOverwriteEvents(ctx.Guild.ID, channel, viewersBefore)
	c.JSON(http.StatusOK, overwrite)
}

// deleteOverwrite DELETE /guilds/{gid}/channels/{cid}/overwrites/{targetID}?type=ROLE|MEMBER
// （需 MANAGE_ROLES）→ PERMISSIONS_UPDATE + 可见性增减定向事件（复用 upsert 的 diff 逻辑）。
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
	var existing []model.ChannelOverwrite
	_ = query.Session(&gorm.Session{}).Find(&existing).Error
	// 重新构造删除 query（Find 可能消耗）
	delQuery := h.deps.DB.Where("channel_id = ? AND target_id = ?", channel.ID, targetID)
	if raw := c.Query("type"); raw != "" {
		delQuery = delQuery.Where("type = ?", raw)
	}
	result := delQuery.Delete(&model.ChannelOverwrite{})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除频道覆盖失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "覆盖记录不存在")
		return
	}
	before := map[string]any{"target_id": targetID}
	if len(existing) > 0 {
		ow := existing[0]
		before["id"] = ow.ID
		before["type"] = ow.Type
		before["target_type"] = ow.Type
		before["allow"] = ow.Allow
		before["deny"] = ow.Deny
	}
	h.audit(ctx, user, "rbac.channel_overwrite_delete", "channel", channel.ID.String(), map[string]any{
		"target_id": targetID,
		"before":    before,
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
