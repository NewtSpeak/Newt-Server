package botapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
)

// bot 开放平面（/bot-api/v1）基础资源端点：bot 自身档案、已安装服务器、
// 可见频道与成员目录。消息/流式/卡片/语音由 message、voice 包复用挂载。

// me GET /bot-api/v1/me：bot 档案 + 关联用户身份。
func (s *service) me(c *gin.Context) {
	bot := currentBot(c)
	user := CurrentBotUser(c)
	c.JSON(http.StatusOK, gin.H{"bot": bot, "user": user})
}

// myGuilds GET /bot-api/v1/guilds：bot 已被安装进的服务器列表。
func (s *service) myGuilds(c *gin.Context) {
	user := CurrentBotUser(c)
	var guilds []model.Guild
	err := s.db.Joins("JOIN members ON members.guild_id = guilds.id").
		Where("members.user_id = ?", user.ID).
		Order("guilds.created_at ASC").Find(&guilds).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取服务器列表失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"guilds": guilds})
}

// listChannels GET /bot-api/v1/guilds/:guildID/channels：bot 可见（VIEW_CHANNEL）的频道。
func (s *service) listChannels(c *gin.Context) {
	ctx, ok := s.botGuildAccess(c)
	if !ok {
		return
	}
	channels, err := ctx.VisibleChannels(s.db)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取频道失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

// listMembers GET /bot-api/v1/guilds/:guildID/members：服务器成员目录（含用户名与 bot 标记）。
func (s *service) listMembers(c *gin.Context) {
	ctx, ok := s.botGuildAccess(c)
	if !ok {
		return
	}
	type memberRow struct {
		ID       uuid.UUID `json:"member_id"`
		UserID   uuid.UUID `json:"user_id"`
		Username string    `json:"username"`
		Nickname string    `json:"nickname"`
		IsBot    bool      `json:"is_bot"`
	}
	var rows []memberRow
	err := s.db.Raw(`SELECT members.id, members.user_id, users.username, members.nickname, users.is_bot
		FROM members JOIN users ON users.id = members.user_id
		WHERE members.guild_id = ? ORDER BY members.created_at ASC`, ctx.Guild.ID).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取成员失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": rows})
}

// myGuildPermissions GET /bot-api/v1/guilds/:guildID/permissions/@me：bot 在该服的最终权限位。
func (s *service) myGuildPermissions(c *gin.Context) {
	ctx, ok := s.botGuildAccess(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"guild_id": ctx.Guild.ID, "permissions": int64(uint64(ctx.Permissions))})
}

// botGuildAccess bot 的服级访问上下文：未安装（非成员）一律 404，不泄露存在性。
func (s *service) botGuildAccess(c *gin.Context) (*perms.GuildContext, bool) {
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		notFound(c)
		return nil, false
	}
	ctx, err := perms.LoadGuild(s.db, CurrentBotUser(c), guildID)
	if err != nil {
		notFound(c)
		return nil, false
	}
	return ctx, true
}
