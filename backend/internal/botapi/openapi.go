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

// botMemberView 成员目录/详情视图：含 role_ids，供反应角色 bot 判断是否已赋角。
// member_id 与 user_id 均可作为后续 PUT/DELETE .../members/{id}/roles/... 的路径标识。
type botMemberView struct {
	MemberID uuid.UUID   `json:"member_id"`
	UserID   uuid.UUID   `json:"user_id"`
	Username string      `json:"username"`
	Nickname string      `json:"nickname"`
	IsBot    bool        `json:"is_bot"`
	IsOwner  bool        `json:"is_owner"`
	RoleIDs  []uuid.UUID `json:"role_ids"`
}

// listMembers GET /bot-api/v1/guilds/:guildID/members：服务器成员目录
//（含 is_bot、is_owner 与 role_ids；反应角色 bot 用 role_ids 做幂等检查）。
func (s *service) listMembers(c *gin.Context) {
	ctx, ok := s.botGuildAccess(c)
	if !ok {
		return
	}
	type memberRow struct {
		ID       uuid.UUID
		UserID   uuid.UUID
		Username string
		Nickname string
		IsBot    bool
	}
	var rows []memberRow
	err := s.db.Raw(`SELECT members.id, members.user_id, users.username, members.nickname, users.is_bot
		FROM members JOIN users ON users.id = members.user_id
		WHERE members.guild_id = ? ORDER BY members.created_at ASC`, ctx.Guild.ID).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取成员失败")
		return
	}
	memberIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		memberIDs = append(memberIDs, r.ID)
	}
	bindings := s.memberRoleBindings(memberIDs)
	views := make([]botMemberView, 0, len(rows))
	for _, r := range rows {
		roleIDs := bindings[r.ID]
		if roleIDs == nil {
			roleIDs = []uuid.UUID{}
		}
		views = append(views, botMemberView{
			MemberID: r.ID, UserID: r.UserID, Username: r.Username, Nickname: r.Nickname,
			IsBot: r.IsBot, IsOwner: r.UserID == ctx.Guild.OwnerUserID, RoleIDs: roleIDs,
		})
	}
	c.JSON(http.StatusOK, gin.H{"members": views})
}

// getMember GET /bot-api/v1/guilds/:guildID/members/:memberID：
// 单成员详情；:memberID 接受 members.id 或 user_id（反应事件载荷给的是 user_id）。
func (s *service) getMember(c *gin.Context) {
	ctx, ok := s.botGuildAccess(c)
	if !ok {
		return
	}
	pathID, err := uuid.Parse(c.Param("memberID"))
	if err != nil {
		notFound(c)
		return
	}
	var member model.Member
	if err := s.db.First(&member, "id = ? AND guild_id = ?", pathID, ctx.Guild.ID).Error; err != nil {
		if err := s.db.First(&member, "user_id = ? AND guild_id = ?", pathID, ctx.Guild.ID).Error; err != nil {
			notFound(c)
			return
		}
	}
	var user model.User
	if err := s.db.Select("id", "username", "is_bot").First(&user, "id = ?", member.UserID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取用户失败")
		return
	}
	bindings := s.memberRoleBindings([]uuid.UUID{member.ID})
	roleIDs := bindings[member.ID]
	if roleIDs == nil {
		roleIDs = []uuid.UUID{}
	}
	c.JSON(http.StatusOK, botMemberView{
		MemberID: member.ID, UserID: member.UserID, Username: user.Username, Nickname: member.Nickname,
		IsBot: user.IsBot, IsOwner: member.UserID == ctx.Guild.OwnerUserID, RoleIDs: roleIDs,
	})
}

// memberRoleBindings 批量拉取 member_id → role_ids。
func (s *service) memberRoleBindings(memberIDs []uuid.UUID) map[uuid.UUID][]uuid.UUID {
	bindings := make(map[uuid.UUID][]uuid.UUID, len(memberIDs))
	if len(memberIDs) == 0 {
		return bindings
	}
	var links []model.MemberRole
	if err := s.db.Where("member_id IN ?", memberIDs).Find(&links).Error; err != nil {
		return bindings
	}
	for _, link := range links {
		bindings[link.MemberID] = append(bindings[link.MemberID], link.RoleID)
	}
	return bindings
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
