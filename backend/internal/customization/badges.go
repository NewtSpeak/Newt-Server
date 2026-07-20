package customization

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// requireBadgeManager 徽章管理权限：MANAGE_BADGES（系统管理员/服主天然拥有）。
func (h *api) requireBadgeManager(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return nil, user, false
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.ManageBadges) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有管理徽章的权限")
		return nil, user, false
	}
	return ctx, user, true
}

// listBadges GET /guilds/{gid}/badges：本服全部徽章定义（成员可见）。
func (h *api) listBadges(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	var badges []model.Badge
	if err := h.deps.DB.Where("guild_id = ?", ctx.Guild.ID).Order("created_at ASC").Find(&badges).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取徽章失败")
		return
	}
	c.JSON(http.StatusOK, badges)
}

type badgeRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=64"`
	Description string `json:"description" binding:"max=255"`
	Emoji       string `json:"emoji" binding:"max=32"`
	IconURL     string `json:"icon_url" binding:"max=512"`
	Color       string `json:"color" binding:"max=32"`
}

func (r *badgeRequest) validate() (string, bool) {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return "徽章名称不能为空", false
	}
	if r.Color != "" && !hexColorPattern.MatchString(r.Color) {
		return "徽章颜色需为 #RRGGBB 格式", false
	}
	return "", true
}

// createBadge POST /guilds/{gid}/badges：新建徽章定义（需 MANAGE_BADGES）。
func (h *api) createBadge(c *gin.Context) {
	ctx, user, ok := h.requireBadgeManager(c)
	if !ok {
		return
	}
	var input badgeRequest
	if !bind(c, &input) {
		return
	}
	if message, ok := input.validate(); !ok {
		fail(c, http.StatusBadRequest, "INVALID_BADGE", message)
		return
	}
	badge := model.Badge{
		ID: uuid.New(), GuildID: ctx.Guild.ID,
		Name: input.Name, Description: input.Description,
		Emoji: input.Emoji, IconURL: input.IconURL, Color: input.Color,
		CreatedBy: user.ID,
	}
	if err := h.deps.DB.Create(&badge).Error; err != nil {
		fail(c, http.StatusConflict, "BADGE_EXISTS", "徽章名称已存在或数据无效")
		return
	}
	h.audit(ctx, user, "customization.badge_create", "badge", badge.ID.String(), map[string]any{"name": badge.Name})
	c.JSON(http.StatusCreated, badge)
}

// updateBadge PATCH /guilds/{gid}/badges/{bid}：编辑徽章定义。
func (h *api) updateBadge(c *gin.Context) {
	ctx, user, ok := h.requireBadgeManager(c)
	if !ok {
		return
	}
	var input badgeRequest
	if !bind(c, &input) {
		return
	}
	if message, ok := input.validate(); !ok {
		fail(c, http.StatusBadRequest, "INVALID_BADGE", message)
		return
	}
	var badge model.Badge
	if err := h.deps.DB.First(&badge, "id = ? AND guild_id = ?", c.Param("badgeID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "徽章不存在")
		return
	}
	badge.Name, badge.Description = input.Name, input.Description
	badge.Emoji, badge.IconURL, badge.Color = input.Emoji, input.IconURL, input.Color
	if err := h.deps.DB.Save(&badge).Error; err != nil {
		fail(c, http.StatusConflict, "BADGE_UPDATE_FAILED", "徽章更新失败（名称可能重复）")
		return
	}
	h.audit(ctx, user, "customization.badge_update", "badge", badge.ID.String(), map[string]any{"name": badge.Name})
	c.JSON(http.StatusOK, badge)
}

// deleteBadge DELETE /guilds/{gid}/badges/{bid}：删除徽章定义与全部授予记录。
func (h *api) deleteBadge(c *gin.Context) {
	ctx, user, ok := h.requireBadgeManager(c)
	if !ok {
		return
	}
	var badge model.Badge
	if err := h.deps.DB.First(&badge, "id = ? AND guild_id = ?", c.Param("badgeID"), ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "徽章不存在")
		return
	}
	if err := h.deps.DB.Where("badge_id = ?", badge.ID).Delete(&model.UserBadge{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除徽章授予记录失败")
		return
	}
	if err := h.deps.DB.Delete(&badge).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除徽章失败")
		return
	}
	h.audit(ctx, user, "customization.badge_delete", "badge", badge.ID.String(), map[string]any{"name": badge.Name})
	c.Status(http.StatusNoContent)
}

type grantBadgeRequest struct {
	// Days 有效天数（1–3650）；Until RFC3339 截止时间。二者都缺省表示永久。
	Days  int    `json:"days" binding:"omitempty,min=1,max=3650"`
	Until string `json:"until" binding:"omitempty"`
}

// grantView 授予记录视图（附用户名，便于控制台直接展示）。
type grantView struct {
	model.UserBadge
	Username string `json:"username"`
}

// listBadgeGrants GET /guilds/{gid}/badges/{bid}/grants：该徽章的有效授予列表。
func (h *api) listBadgeGrants(c *gin.Context) {
	ctx, _, ok := h.requireBadgeManager(c)
	if !ok {
		return
	}
	badgeID, err := uuid.Parse(c.Param("badgeID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "徽章不存在")
		return
	}
	var grants []grantView
	err = h.deps.DB.Raw(`SELECT user_badges.*, users.username FROM user_badges
		JOIN users ON users.id = user_badges.user_id
		WHERE user_badges.badge_id = ? AND user_badges.guild_id = ?
		ORDER BY user_badges.created_at ASC`, badgeID, ctx.Guild.ID).Scan(&grants).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取授予记录失败")
		return
	}
	if grants == nil {
		grants = []grantView{}
	}
	c.JSON(http.StatusOK, grants)
}

// grantBadge PUT /guilds/{gid}/badges/{bid}/members/{uid}：授予徽章
//（永久 / 有效天数 / 截止日期）；重复授予时更新有效期。
func (h *api) grantBadge(c *gin.Context) {
	ctx, user, ok := h.requireBadgeManager(c)
	if !ok {
		return
	}
	var input grantBadgeRequest
	if err := c.ShouldBindJSON(&input); err != nil && err.Error() != "EOF" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if input.Days > 0 && input.Until != "" {
		fail(c, http.StatusBadRequest, "INVALID_EXPIRY", "days 与 until 只能二选一")
		return
	}
	var expiresAt *time.Time
	now := time.Now().UTC()
	if input.Days > 0 {
		expiry := now.Add(time.Duration(input.Days) * 24 * time.Hour)
		expiresAt = &expiry
	} else if input.Until != "" {
		parsed, err := time.Parse(time.RFC3339, input.Until)
		if err != nil || !parsed.After(now) {
			fail(c, http.StatusBadRequest, "INVALID_EXPIRY", "until 需为未来的 RFC3339 时间")
			return
		}
		utc := parsed.UTC()
		expiresAt = &utc
	}
	badgeID, err1 := uuid.Parse(c.Param("badgeID"))
	targetUserID, err2 := uuid.Parse(c.Param("userID"))
	if err1 != nil || err2 != nil {
		fail(c, http.StatusBadRequest, "INVALID_ID", "徽章或用户 ID 无效")
		return
	}
	var badge model.Badge
	if err := h.deps.DB.First(&badge, "id = ? AND guild_id = ?", badgeID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "徽章不存在")
		return
	}
	var member model.Member
	if err := h.deps.DB.First(&member, "guild_id = ? AND user_id = ?", ctx.Guild.ID, targetUserID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "目标用户不是本服成员")
		return
	}
	grant := model.UserBadge{
		ID: uuid.New(), BadgeID: badge.ID, UserID: targetUserID,
		GuildID: ctx.Guild.ID, GrantedBy: user.ID, ExpiresAt: expiresAt,
	}
	err := h.deps.DB.Where(model.UserBadge{BadgeID: badge.ID, UserID: targetUserID}).
		Assign(map[string]any{"expires_at": expiresAt, "granted_by": user.ID, "guild_id": ctx.Guild.ID}).
		FirstOrCreate(&grant).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "授予徽章失败")
		return
	}
	h.audit(ctx, user, "customization.badge_grant", "badge", badge.ID.String(), map[string]any{
		"target_user_id": targetUserID, "badge_name": badge.Name, "expires_at": expiresAt,
	})
	if h.deps.Bus != nil {
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventBadgeGrant,
			GuildID: &guildID,
			Payload: gin.H{"guild_id": guildID, "user_id": targetUserID, "badge": badge, "expires_at": expiresAt},
		})
	}
	c.JSON(http.StatusOK, grant)
}

// revokeBadge DELETE /guilds/{gid}/badges/{bid}/members/{uid}：回收徽章。
func (h *api) revokeBadge(c *gin.Context) {
	ctx, user, ok := h.requireBadgeManager(c)
	if !ok {
		return
	}
	badgeID, err1 := uuid.Parse(c.Param("badgeID"))
	targetUserID, err2 := uuid.Parse(c.Param("userID"))
	if err1 != nil || err2 != nil {
		fail(c, http.StatusBadRequest, "INVALID_ID", "徽章或用户 ID 无效")
		return
	}
	var badge model.Badge
	if err := h.deps.DB.First(&badge, "id = ? AND guild_id = ?", badgeID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "徽章不存在")
		return
	}
	result := h.deps.DB.Where("badge_id = ? AND user_id = ?", badge.ID, targetUserID).Delete(&model.UserBadge{})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "回收徽章失败")
		return
	}
	h.audit(ctx, user, "customization.badge_revoke", "badge", badge.ID.String(), map[string]any{
		"target_user_id": targetUserID, "badge_name": badge.Name,
	})
	if h.deps.Bus != nil {
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventBadgeRevoke,
			GuildID: &guildID,
			Payload: gin.H{"guild_id": guildID, "user_id": targetUserID, "badge_id": badge.ID},
		})
	}
	c.Status(http.StatusNoContent)
}
