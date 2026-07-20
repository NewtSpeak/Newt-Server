package guildapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/voice"
	"gorm.io/gorm"
)

type updateGuildRequest struct {
	Name        *string `json:"name" binding:"omitempty,min=2,max=100"`
	Description *string `json:"description" binding:"omitempty,max=1024"`
}

// updateGuild PATCH /guilds/{gid}（需 MANAGE_GUILD）→ GUILD_UPDATE 全服广播。
func (h *api) updateGuild(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageGuild)
	if !ok {
		return
	}
	var input updateGuildRequest
	if !bind(c, &input) {
		return
	}
	guild := ctx.Guild
	updates := map[string]any{}
	before := map[string]any{"name": guild.Name, "description": guild.Description}
	if input.Name != nil {
		guild.Name = strings.TrimSpace(*input.Name)
		if len(guild.Name) < 2 {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "服务器名称至少 2 个字符")
			return
		}
		updates["name"] = guild.Name
	}
	if input.Description != nil {
		guild.Description = strings.TrimSpace(*input.Description)
		updates["description"] = guild.Description
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "没有可更新的字段")
		return
	}
	if err := h.deps.DB.Model(&model.Guild{}).Where("id = ?", guild.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新服务器失败")
		return
	}
	h.audit(ctx, user, "guild.update", "guild", guild.ID.String(), map[string]any{
		"before": before, "after": map[string]any{"name": guild.Name, "description": guild.Description},
	})
	guildID := guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildUpdatePayload(guild),
	})
	c.JSON(http.StatusOK, guild)
}

type deleteGuildRequest struct {
	// ConfirmName 防误删确认：必须与服务器当前名称完全一致（Owl-Desktop docs 02
	// FR-27：删除确认弹窗要求手动输入服务器名称，对标 Discord）。
	ConfirmName string `json:"confirm_name" binding:"required"`
}

// deleteGuild DELETE /guilds/{gid}（仅所有者，请求体须携带 confirm_name=服务器名）
// → GUILD_DELETE 定向发给全部成员。
// 联动：语音频道内的人先经 voice 模块断开；事务内清理成员/角色/频道/覆盖/
// 语音状态/邀请/封禁/Restriction（消息等大体量领域数据留待离线清理任务）。
func (h *api) deleteGuild(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.Owner {
		fail(c, http.StatusForbidden, "OWNER_ONLY", "仅服务器所有者可删除服务器")
		return
	}
	var input deleteGuildRequest
	if !bind(c, &input) {
		return
	}
	if strings.TrimSpace(input.ConfirmName) != ctx.Guild.Name {
		fail(c, http.StatusBadRequest, "CONFIRM_NAME_MISMATCH", "confirm_name 与服务器名称不一致")
		return
	}
	guildID := ctx.Guild.ID
	// 删除前收集成员，用于事后定向下发 GUILD_DELETE（成员表删除后无法再广播）。
	var memberUserIDs []uuid.UUID
	if err := h.deps.DB.Model(&model.Member{}).Where("guild_id = ?", guildID).Pluck("user_id", &memberUserIDs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取成员失败")
		return
	}
	// 语音联动：断开本服全部语音会话（SFU 踢出 + VOICE_STATE_UPDATE）。
	voice.DisconnectGuildUsers(guildID, "GUILD_DELETE")
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("member_id IN (SELECT id FROM members WHERE guild_id = ?)", guildID).Delete(&model.MemberRole{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id IN (SELECT id FROM channels WHERE guild_id = ?)", guildID).Delete(&model.ChannelOverwrite{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id IN (SELECT id FROM channels WHERE guild_id = ?)", guildID).Delete(&model.StageChannelConfig{}).Error; err != nil {
			return err
		}
		for _, target := range []any{
			&model.VoiceState{}, &model.Channel{}, &model.Member{}, &model.Role{},
			&model.Invite{}, &model.GuildBan{}, &model.Restriction{},
		} {
			if err := tx.Where("guild_id = ?", guildID).Delete(target).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&model.Guild{}, "id = ?", guildID).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除服务器失败")
		return
	}
	h.audit(ctx, user, "guild.delete", "guild", guildID.String(), map[string]any{
		"name": ctx.Guild.Name, "member_count": len(memberUserIDs),
	})
	if len(memberUserIDs) > 0 {
		h.publish(eventbus.Event{
			Type: eventbus.EventGuildDelete, GuildID: &guildID, UserIDs: memberUserIDs,
			Payload: eventbus.NewGuildDeletePayload(guildID),
		})
	}
	c.Status(http.StatusNoContent)
}

type transferOwnershipRequest struct {
	NewOwnerUserID uuid.UUID `json:"new_owner_user_id" binding:"required"`
}

// transferOwnership POST /guilds/{gid}/transfer-ownership（仅所有者）→ GUILD_UPDATE。
// 新所有者必须是本服成员；转让后原所有者保留成员与既有角色。
func (h *api) transferOwnership(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.Owner {
		fail(c, http.StatusForbidden, "OWNER_ONLY", "仅服务器所有者可转让所有权")
		return
	}
	var input transferOwnershipRequest
	if !bind(c, &input) {
		return
	}
	if input.NewOwnerUserID == user.ID {
		fail(c, http.StatusBadRequest, "ALREADY_OWNER", "你已是服务器所有者")
		return
	}
	var member model.Member
	if err := h.deps.DB.First(&member, "guild_id = ? AND user_id = ?", ctx.Guild.ID, input.NewOwnerUserID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "目标成员不存在")
		return
	}
	if err := h.deps.DB.Model(&model.Guild{}).Where("id = ?", ctx.Guild.ID).Update("owner_user_id", input.NewOwnerUserID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "转让所有权失败")
		return
	}
	guild := ctx.Guild
	guild.OwnerUserID = input.NewOwnerUserID
	h.audit(ctx, user, "guild.transfer_ownership", "guild", guild.ID.String(), map[string]any{
		"from_user_id": user.ID, "to_user_id": input.NewOwnerUserID,
	})
	guildID := guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildUpdatePayload(guild),
	})
	c.JSON(http.StatusOK, guild)
}

// myGuildPermissions GET /guilds/{gid}/permissions/@me：服务器级最终权限投影。
// permissions 为十进制字符串形式的 uint64 掩码（扩展位 52–54 超出 JS Number
// 2^53 精度，客户端应以 BigInt 解析，Owl-Desktop docs 04 FR-16）。
func (h *api) myGuildPermissions(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"guild_id": ctx.Guild.ID, "permissions": maskString(ctx.Permissions)})
}

// myChannelPermissions GET /guilds/{gid}/channels/{cid}/permissions/@me：
// 频道级最终权限投影；无 VIEW_CHANNEL 一律 404。
func (h *api) myChannelPermissions(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
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
	c.JSON(http.StatusOK, gin.H{"guild_id": ctx.Guild.ID, "channel_id": channel.ID, "permissions": maskString(bits)})
}

// myChannelPermissionsByChannel GET /channels/{cid}/permissions/@me：顶级频道入口，
// 由频道反查 guild；频道不存在/无 VIEW_CHANNEL 一律 404（防扫频）。
func (h *api) myChannelPermissionsByChannel(c *gin.Context) {
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
	c.JSON(http.StatusOK, gin.H{"guild_id": ctx.Guild.ID, "channel_id": channel.ID, "permissions": maskString(bits)})
}
