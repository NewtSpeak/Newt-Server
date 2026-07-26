package botapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// 服级机器人管理：服主 / MANAGE_BOTS 在本服创建独属 bot（自动安装），
// 签发 token、改资料、删除。后台 /api/v1 与用户端 /gapi/v1 共用本文件 handler。

// mountGuildBotRoutes 挂载 /guilds/:guildID/bots 全家桶。
func (h *adminHandlers) mountGuildBotRoutes(authed *gin.RouterGroup) {
	authed.GET("/guilds/:guildID/bots", h.listGuildBots)
	authed.POST("/guilds/:guildID/bots", h.createGuildBot)
	authed.GET("/guilds/:guildID/bots/:botID", h.getGuildBot)
	authed.PATCH("/guilds/:guildID/bots/:botID", h.updateGuildBot)
	// DELETE：服级 bot 整档删除；平台级 bot 仅卸载本服
	authed.DELETE("/guilds/:guildID/bots/:botID", h.deleteOrUninstallGuildBot)
	authed.POST("/guilds/:guildID/bots/:botID/tokens", h.createGuildBotToken)
	authed.GET("/guilds/:guildID/bots/:botID/tokens", h.listGuildBotTokens)
	authed.DELETE("/guilds/:guildID/bots/:botID/tokens/:tokenID", h.revokeGuildBotToken)
	// 兼容旧「安装平台 bot」路径（仅 home_guild 为空的 bot）
	authed.PUT("/guilds/:guildID/bots/:botID", h.installBot)
}

type createGuildBotRequest struct {
	Name        string `json:"name" binding:"required,min=2,max=64"`
	Username    string `json:"username" binding:"required,min=2,max=32"`
	Description string `json:"description" binding:"max=512"`
	AvatarURL   string `json:"avatar_url" binding:"max=512"`
}

// createGuildBot POST /guilds/:guildID/bots
// 创建服级机器人：User(IsBot)+Bot(home_guild=本服)+Member，一步完成。
func (h *adminHandlers) createGuildBot(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	var input createGuildBotRequest
	if !bind(c, &input) {
		return
	}
	actor := h.currentUser(c)
	username := strings.TrimSpace(input.Username)
	guildID := ctx.Guild.ID
	botUser := model.User{
		ID:           uuid.New(),
		Username:     username,
		Email:        strings.ToLower(username) + "@bots.owlspeak.internal",
		PasswordHash: "!bot-no-password",
		IsBot:        true,
	}
	bot := model.Bot{
		ID:          uuid.New(),
		UserID:      botUser.ID,
		OwnerUserID: actor.ID,
		HomeGuildID: &guildID,
		Name:        strings.TrimSpace(input.Name),
		Description: strings.TrimSpace(input.Description),
		AvatarURL:   strings.TrimSpace(input.AvatarURL),
	}
	member := model.Member{
		ID:      uuid.New(),
		GuildID: guildID,
		UserID:  botUser.ID,
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&botUser).Error; err != nil {
			return err
		}
		if err := tx.Create(&bot).Error; err != nil {
			return err
		}
		return tx.Create(&member).Error
	})
	if err != nil {
		fail(c, http.StatusConflict, "BOT_CREATE_FAILED", "创建失败：用户名可能已被占用")
		return
	}
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: h.actorType(actor), GuildID: &guildID,
		Action: "bot.create_guild", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{
			"name": bot.Name, "username": username,
			"bot_user_id": botUser.ID, "home_guild_id": guildID,
		},
	})
	if h.bus != nil {
		h.bus.Publish(eventbus.Event{
			Type: eventbus.EventGuildMemberAdd, GuildID: &guildID,
			Payload: eventbus.NewGuildMemberAddPayload(member, botUser),
		})
	}
	c.JSON(http.StatusCreated, h.guildBotViewOne(bot, member.ID))
}

// getGuildBot GET /guilds/:guildID/bots/:botID
func (h *adminHandlers) getGuildBot(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, member, ok := h.loadGuildBot(c, ctx.Guild.ID)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.guildBotViewOne(bot, member.ID))
}

// updateGuildBot PATCH /guilds/:guildID/bots/:botID
// 仅允许管理本服已安装 bot；服级 bot 的 home 必须为本服。
func (h *adminHandlers) updateGuildBot(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, member, ok := h.loadGuildBot(c, ctx.Guild.ID)
	if !ok {
		return
	}
	if bot.HomeGuildID != nil && *bot.HomeGuildID != ctx.Guild.ID {
		fail(c, http.StatusForbidden, "BOT_HOME_MISMATCH", "该机器人归属其他服务器，无法在此修改")
		return
	}
	var input updateBotRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{}
	if input.Name != nil {
		updates["name"] = strings.TrimSpace(*input.Name)
	}
	if input.Description != nil {
		updates["description"] = strings.TrimSpace(*input.Description)
	}
	if input.AvatarURL != nil {
		updates["avatar_url"] = strings.TrimSpace(*input.AvatarURL)
	}
	if len(updates) > 0 {
		if err := h.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(updates).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新机器人失败")
			return
		}
		h.db.First(&bot, "id = ?", bot.ID)
	}
	c.JSON(http.StatusOK, h.guildBotViewOne(bot, member.ID))
}

// deleteOrUninstallGuildBot DELETE /guilds/:guildID/bots/:botID
// - 服级 bot（home=本服）：吊销 token + 退服 + 删档案
// - 平台 bot：仅卸载本服（兼容旧行为）
func (h *adminHandlers) deleteOrUninstallGuildBot(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, member, ok := h.loadGuildBot(c, ctx.Guild.ID)
	if !ok {
		return
	}
	actor := h.currentUser(c)
	isHome := bot.HomeGuildID != nil && *bot.HomeGuildID == ctx.Guild.ID

	if isHome {
		h.kickBotVoiceSessions(bot.UserID)
		now := time.Now().UTC()
		err := h.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.BotToken{}).Where("bot_id = ? AND revoked_at IS NULL", bot.ID).
				Update("revoked_at", now).Error; err != nil {
				return err
			}
			if err := tx.Where("member_id = ?", member.ID).Delete(&model.MemberRole{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&model.Member{}, "id = ?", member.ID).Error; err != nil {
				return err
			}
			return tx.Delete(&model.Bot{}, "id = ?", bot.ID).Error
		})
		if err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除机器人失败")
			return
		}
		audit.Log(h.db, audit.Entry{
			ActorID: &actor.ID, ActorType: h.actorType(actor), GuildID: &ctx.Guild.ID,
			Action: "bot.delete_guild", TargetType: "bot", TargetID: bot.ID.String(),
			Detail: map[string]any{"name": bot.Name, "bot_user_id": bot.UserID},
		})
		if h.bus != nil {
			guildID := ctx.Guild.ID
			h.bus.Publish(eventbus.Event{
				Type: eventbus.EventGuildMemberRemove, GuildID: &guildID,
				Payload: eventbus.NewGuildMemberRemovePayload(member, "bot_deleted"),
			})
		}
		c.Status(http.StatusNoContent)
		return
	}

	// 平台 bot：仅卸载
	h.uninstallBot(c)
}

// ---------- 服级 token ----------

func (h *adminHandlers) createGuildBotToken(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, _, ok := h.loadGuildBot(c, ctx.Guild.ID)
	if !ok {
		return
	}
	if bot.HomeGuildID != nil && *bot.HomeGuildID != ctx.Guild.ID {
		fail(c, http.StatusForbidden, "BOT_HOME_MISMATCH", "该机器人归属其他服务器")
		return
	}
	// 复用 createToken 主体：临时改写 path 已由 loadGuildBot 校验
	h.createTokenForBot(c, bot)
}

func (h *adminHandlers) listGuildBotTokens(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, _, ok := h.loadGuildBot(c, ctx.Guild.ID)
	if !ok {
		return
	}
	var tokens []model.BotToken
	if err := h.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").Find(&tokens).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取令牌失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"tokens": tokens})
}

func (h *adminHandlers) revokeGuildBotToken(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, _, ok := h.loadGuildBot(c, ctx.Guild.ID)
	if !ok {
		return
	}
	tokenID, err := uuid.Parse(c.Param("tokenID"))
	if err != nil {
		notFound(c)
		return
	}
	now := time.Now().UTC()
	result := h.db.Model(&model.BotToken{}).
		Where("id = ? AND bot_id = ? AND revoked_at IS NULL", tokenID, bot.ID).
		Update("revoked_at", now)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销令牌失败")
		return
	}
	if result.RowsAffected == 0 {
		notFound(c)
		return
	}
	actor := h.currentUser(c)
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: h.actorType(actor), GuildID: &ctx.Guild.ID,
		Action: "bot.token_revoke", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"token_id": tokenID},
	})
	c.Status(http.StatusNoContent)
}

func (h *adminHandlers) createTokenForBot(c *gin.Context, bot model.Bot) {
	var input createTokenRequest
	if !bind(c, &input) {
		return
	}
	plain, hash, displayPrefix, err := newBotToken()
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "生成令牌失败")
		return
	}
	token := model.BotToken{
		ID:        uuid.New(),
		BotID:     bot.ID,
		Name:      strings.TrimSpace(input.Name),
		TokenHash: hash,
		Prefix:    displayPrefix,
		ExpiresAt: input.ExpiresAt,
	}
	if err := h.db.Create(&token).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存令牌失败")
		return
	}
	actor := h.currentUser(c)
	var guildID *uuid.UUID
	if bot.HomeGuildID != nil {
		guildID = bot.HomeGuildID
	}
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: h.actorType(actor), GuildID: guildID,
		Action: "bot.token_create", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"token_id": token.ID, "name": token.Name},
	})
	c.JSON(http.StatusCreated, gin.H{"token": token, "plain": plain})
}

// ---------- 视图 / 加载 ----------

func (h *adminHandlers) guildBotViewOne(bot model.Bot, memberID uuid.UUID) guildBotView {
	view := guildBotView{Bot: bot, MemberID: memberID, RoleIDs: []uuid.UUID{}}
	h.db.Model(&model.User{}).Select("username").Where("id = ?", bot.UserID).Scan(&view.Username)
	var links []model.MemberRole
	if err := h.db.Where("member_id = ?", memberID).Find(&links).Error; err == nil {
		for _, link := range links {
			view.RoleIDs = append(view.RoleIDs, link.RoleID)
		}
	}
	return view
}

// loadGuildBot 要求 bot 已安装在该服。
func (h *adminHandlers) loadGuildBot(c *gin.Context, guildID uuid.UUID) (model.Bot, model.Member, bool) {
	var zeroBot model.Bot
	var zeroMember model.Member
	botID, err := uuid.Parse(c.Param("botID"))
	if err != nil {
		notFound(c)
		return zeroBot, zeroMember, false
	}
	var bot model.Bot
	if err := h.db.First(&bot, "id = ?", botID).Error; err != nil {
		notFound(c)
		return zeroBot, zeroMember, false
	}
	var member model.Member
	if err := h.db.First(&member, "guild_id = ? AND user_id = ?", guildID, bot.UserID).Error; err != nil {
		notFound(c)
		return zeroBot, zeroMember, false
	}
	return bot, member, true
}

func (h *adminHandlers) actorType(user model.User) string {
	if user.SystemAdmin {
		return "system_admin"
	}
	return "guild_admin"
}

// requireGuildManageBots 供 RegisterClient 使用的简写（与 guildAccess 相同）。
func (h *adminHandlers) requireGuildManageBots(c *gin.Context) (*perms.GuildContext, bool) {
	return h.guildAccess(c, rbac.ManageBots)
}
