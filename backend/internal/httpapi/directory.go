package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
)

// listChannels godoc
// @Summary 列出当前用户可见的频道
// @Tags RBAC
// @Security BearerAuth
// @Produce json
// @Success 200 {array} model.Channel
// @Router /guilds/{guildID}/channels [get]
func (a *API) listChannels(c *gin.Context) {
	user := currentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	ctx, err := perms.LoadGuild(a.db, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	channels, err := ctx.VisibleChannels(a.db)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取频道失败")
		return
	}
	c.JSON(http.StatusOK, channels)
}

type memberSummary struct {
	ID       uuid.UUID   `json:"id"`
	UserID   uuid.UUID   `json:"user_id"`
	Username string      `json:"username"`
	Nickname string      `json:"nickname"`
	IsOwner  bool        `json:"is_owner"`
	RoleIDs  []uuid.UUID `json:"role_ids"`
}

// listMembers godoc
// @Summary 列出服务器成员
// @Tags RBAC
// @Security BearerAuth
// @Produce json
// @Success 200 {array} memberSummary
// @Router /guilds/{guildID}/members [get]
func (a *API) listMembers(c *gin.Context) {
	guild, ok := a.guildForUser(c)
	if !ok {
		return
	}
	type row struct {
		model.Member
		Username string
	}
	var rows []row
	err := a.db.Raw(`SELECT members.*, users.username FROM members JOIN users ON users.id = members.user_id WHERE members.guild_id = ? ORDER BY members.created_at ASC`, guild.ID).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取成员失败")
		return
	}
	memberIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		memberIDs = append(memberIDs, r.Member.ID)
	}
	bindings := make(map[uuid.UUID][]uuid.UUID)
	if len(memberIDs) > 0 {
		var links []model.MemberRole
		if err := a.db.Where("member_id IN ?", memberIDs).Find(&links).Error; err == nil {
			for _, link := range links {
				bindings[link.MemberID] = append(bindings[link.MemberID], link.RoleID)
			}
		}
	}
	result := make([]memberSummary, 0, len(rows))
	for _, r := range rows {
		roleIDs := bindings[r.Member.ID]
		if roleIDs == nil {
			roleIDs = []uuid.UUID{}
		}
		result = append(result, memberSummary{
			ID:       r.Member.ID,
			UserID:   r.Member.UserID,
			Username: r.Username,
			Nickname: r.Member.Nickname,
			IsOwner:  r.Member.UserID == guild.OwnerUserID,
			RoleIDs:  roleIDs,
		})
	}
	c.JSON(http.StatusOK, result)
}
