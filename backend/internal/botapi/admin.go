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

// 后台管理平面（/api/v1，aud=admin）：bot 注册/资料/令牌生命周期、
// 安装到服（创建 Member）与卸载。角色/频道覆盖等权限赋予复用既有 RBAC 端点
//（bot 即 Member，控制台直接用成员角色绑定接口操作）。

// currentAdmin 由 deps.CurrentUser 注入（Register 时保存），读取当前后台管理员。
type adminHandlers struct {
	*service
	currentUser func(*gin.Context) model.User
}

// botView 管理端 bot 视图：档案 + 关联 bot 用户名 + 令牌数。
type botView struct {
	model.Bot
	Username    string `json:"username"`
	TokenCount  int64  `json:"token_count"`
	GuildCount  int64  `json:"guild_count"`
}

func (h *adminHandlers) botViewOne(bot model.Bot) botView {
	view := botView{Bot: bot}
	h.db.Model(&model.User{}).Select("username").Where("id = ?", bot.UserID).Scan(&view.Username)
	h.db.Model(&model.BotToken{}).Where("bot_id = ? AND revoked_at IS NULL", bot.ID).Count(&view.TokenCount)
	h.db.Model(&model.Member{}).Where("user_id = ?", bot.UserID).Count(&view.GuildCount)
	return view
}

type createBotRequest struct {
	Name        string `json:"name" binding:"required,min=2,max=64"`
	Username    string `json:"username" binding:"required,min=2,max=32"`
	Description string `json:"description" binding:"max=512"`
	AvatarURL   string `json:"avatar_url" binding:"max=512"`
}

// createBot POST /bots：创建 bot 档案 + 关联的 IsBot 用户（不可密码登录）。
func (h *adminHandlers) createBot(c *gin.Context) {
	var input createBotRequest
	if !bind(c, &input) {
		return
	}
	actor := h.currentUser(c)
	username := strings.TrimSpace(input.Username)
	botUser := model.User{
		ID:       uuid.New(),
		Username: username,
		Email:    strings.ToLower(username) + "@bots.owlspeak.internal",
		// bot 不走密码登录：占位哈希对任何输入校验失败（bcrypt 格式不匹配）。
		PasswordHash: "!bot-no-password",
		IsBot:        true,
	}
	bot := model.Bot{
		ID:          uuid.New(),
		UserID:      botUser.ID,
		OwnerUserID: actor.ID,
		Name:        strings.TrimSpace(input.Name),
		Description: strings.TrimSpace(input.Description),
		AvatarURL:   strings.TrimSpace(input.AvatarURL),
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&botUser).Error; err != nil {
			return err
		}
		return tx.Create(&bot).Error
	})
	if err != nil {
		fail(c, http.StatusConflict, "BOT_CREATE_FAILED", "创建失败：用户名可能已被占用")
		return
	}
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "bot.create", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"name": bot.Name, "username": username, "bot_user_id": botUser.ID},
	})
	c.JSON(http.StatusCreated, h.botViewOne(bot))
}

// listBots GET /bots。
func (h *adminHandlers) listBots(c *gin.Context) {
	var bots []model.Bot
	if err := h.db.Order("created_at DESC").Find(&bots).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取机器人列表失败")
		return
	}
	views := make([]botView, 0, len(bots))
	for _, bot := range bots {
		views = append(views, h.botViewOne(bot))
	}
	c.JSON(http.StatusOK, views)
}

// getBot GET /bots/:botID。
func (h *adminHandlers) getBot(c *gin.Context) {
	bot, ok := h.loadBot(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.botViewOne(bot))
}

type updateBotRequest struct {
	Name        *string `json:"name" binding:"omitempty,min=2,max=64"`
	Description *string `json:"description" binding:"omitempty,max=512"`
	AvatarURL   *string `json:"avatar_url" binding:"omitempty,max=512"`
}

// updateBot PATCH /bots/:botID。
func (h *adminHandlers) updateBot(c *gin.Context) {
	bot, ok := h.loadBot(c)
	if !ok {
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
	c.JSON(http.StatusOK, h.botViewOne(bot))
}

// deleteBot DELETE /bots/:botID：吊销全部令牌、退出全部服务器并删除档案。
// bot 用户行保留（消息作者引用完整性），令牌全灭后无法再被使用。
func (h *adminHandlers) deleteBot(c *gin.Context) {
	bot, ok := h.loadBot(c)
	if !ok {
		return
	}
	actor := h.currentUser(c)
	// 摘除语音在房会话：交给 voice 模块经 InternalCapsDirty 正规踢出。
	h.kickBotVoiceSessions(bot.UserID)
	now := time.Now().UTC()
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BotToken{}).Where("bot_id = ? AND revoked_at IS NULL", bot.ID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM member_roles WHERE member_id IN (SELECT id FROM members WHERE user_id = ?)`, bot.UserID).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", bot.UserID).Delete(&model.Member{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Bot{}, "id = ?", bot.ID).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除机器人失败")
		return
	}
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "bot.delete", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"name": bot.Name, "bot_user_id": bot.UserID},
	})
	c.Status(http.StatusNoContent)
}

// ---------- 令牌管理 ----------

type createTokenRequest struct {
	Name      string     `json:"name" binding:"max=64"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// createToken POST /bots/:botID/tokens：签发长期令牌，明文仅本次响应返回。
func (h *adminHandlers) createToken(c *gin.Context) {
	bot, ok := h.loadBot(c)
	if !ok {
		return
	}
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
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "bot.token_create", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"token_id": token.ID, "name": token.Name},
	})
	c.JSON(http.StatusCreated, gin.H{"token": token, "plain": plain})
}

// listTokens GET /bots/:botID/tokens（仅元数据，不含明文/哈希）。
func (h *adminHandlers) listTokens(c *gin.Context) {
	bot, ok := h.loadBot(c)
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

// revokeToken DELETE /bots/:botID/tokens/:tokenID：吊销令牌（立即生效）。
func (h *adminHandlers) revokeToken(c *gin.Context) {
	bot, ok := h.loadBot(c)
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
		ActorID: &actor.ID, ActorType: "system_admin",
		Action: "bot.token_revoke", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"token_id": tokenID},
	})
	c.Status(http.StatusNoContent)
}

// ---------- 安装到服 / 卸载（权限赋予复用成员角色端点） ----------

// guildBotView 某服已安装 bot 视图：档案 + member_id（角色绑定用）+ 已绑定角色。
type guildBotView struct {
	model.Bot
	Username string      `json:"username"`
	MemberID uuid.UUID   `json:"member_id"`
	RoleIDs  []uuid.UUID `json:"role_ids"`
}

// listGuildBots GET /guilds/:guildID/bots。
func (h *adminHandlers) listGuildBots(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	type row struct {
		model.Bot
		Username string
		MemberID uuid.UUID
	}
	var rows []row
	err := h.db.Raw(`SELECT bots.*, users.username, members.id AS member_id
		FROM bots
		JOIN users ON users.id = bots.user_id
		JOIN members ON members.user_id = bots.user_id AND members.guild_id = ?
		ORDER BY bots.created_at ASC`, ctx.Guild.ID).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取已安装机器人失败")
		return
	}
	views := make([]guildBotView, 0, len(rows))
	for _, r := range rows {
		view := guildBotView{Bot: r.Bot, Username: r.Username, MemberID: r.MemberID, RoleIDs: []uuid.UUID{}}
		var links []model.MemberRole
		if err := h.db.Where("member_id = ?", r.MemberID).Find(&links).Error; err == nil {
			for _, link := range links {
				view.RoleIDs = append(view.RoleIDs, link.RoleID)
			}
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, views)
}

// installBot PUT /guilds/:guildID/bots/:botID：把 bot 安装进服务器（创建 Member）。
// 安装后即可用既有成员角色端点为其手动赋权。
func (h *adminHandlers) installBot(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, ok := h.loadBot(c)
	if !ok {
		return
	}
	member := model.Member{GuildID: ctx.Guild.ID, UserID: bot.UserID}
	if err := h.db.Where(member).Attrs(model.Member{ID: uuid.New()}).FirstOrCreate(&member).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "安装机器人失败")
		return
	}
	actor := h.currentUser(c)
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin", GuildID: &ctx.Guild.ID,
		Action: "bot.install", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"member_id": member.ID, "bot_user_id": bot.UserID},
	})
	c.JSON(http.StatusOK, gin.H{"member_id": member.ID, "bot_id": bot.ID, "guild_id": ctx.Guild.ID})
}

// uninstallBot DELETE /guilds/:guildID/bots/:botID：卸载（删 Member 与角色绑定）。
// 若 bot 在该服语音在房，经 InternalCapsDirty 触发正规踢出流程。
func (h *adminHandlers) uninstallBot(c *gin.Context) {
	ctx, ok := h.guildAccess(c, rbac.ManageBots)
	if !ok {
		return
	}
	bot, ok := h.loadBot(c)
	if !ok {
		return
	}
	var member model.Member
	if err := h.db.First(&member, "guild_id = ? AND user_id = ?", ctx.Guild.ID, bot.UserID).Error; err != nil {
		notFound(c)
		return
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("member_id = ?", member.ID).Delete(&model.MemberRole{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Member{}, "id = ?", member.ID).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "卸载机器人失败")
		return
	}
	h.kickBotVoiceSessions(bot.UserID)
	actor := h.currentUser(c)
	audit.Log(h.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin", GuildID: &ctx.Guild.ID,
		Action: "bot.uninstall", TargetType: "bot", TargetID: bot.ID.String(),
		Detail: map[string]any{"bot_user_id": bot.UserID},
	})
	c.Status(http.StatusNoContent)
}

// ---------- 辅助 ----------

func (h *adminHandlers) loadBot(c *gin.Context) (model.Bot, bool) {
	var bot model.Bot
	id, err := uuid.Parse(c.Param("botID"))
	if err != nil {
		notFound(c)
		return bot, false
	}
	if err := h.db.First(&bot, "id = ?", id).Error; err != nil {
		notFound(c)
		return bot, false
	}
	return bot, true
}

// guildAccess 加载调用者在目标服的权限上下文并要求指定权限位（系统管短路全权限）。
func (h *adminHandlers) guildAccess(c *gin.Context, required rbac.Permission) (*perms.GuildContext, bool) {
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		notFound(c)
		return nil, false
	}
	ctx, err := perms.LoadGuild(h.db, h.currentUser(c), guildID)
	if err != nil {
		notFound(c)
		return nil, false
	}
	if !ctx.Has(required) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少 MANAGE_BOTS 权限")
		return nil, false
	}
	return ctx, true
}

// kickBotVoiceSessions 对 bot 用户全部在房语音会话发布 caps 脏通知：
// 成员关系已变（卸载/删除），voice 模块重算后会走正规踢出（PERMISSION_REVOKED）。
func (h *adminHandlers) kickBotVoiceSessions(botUserID uuid.UUID) {
	var states []model.VoiceState
	if err := h.db.Where("user_id = ? AND channel_id IS NOT NULL", botUserID).Find(&states).Error; err != nil {
		return
	}
	for _, vs := range states {
		if vs.ChannelID == nil {
			continue
		}
		h.bus.Publish(eventbus.Event{
			Type: eventbus.InternalCapsDirty,
			Payload: eventbus.CapsDirtyPayload{
				GuildID:   vs.GuildID.String(),
				ChannelID: vs.ChannelID.String(),
				UserID:    botUserID.String(),
				Reason:    "bot_uninstalled",
			},
		})
	}
}
