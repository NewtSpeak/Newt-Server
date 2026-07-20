package platformadmin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

func (h *api) audit(c *gin.Context, action, targetID string, detail map[string]any) {
	actor := h.deps.CurrentUser(c)
	actorID := actor.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: "system_admin",
		Action: action, TargetType: "user", TargetID: targetID, Detail: detail,
	})
}

// listUsers GET /admin/users?q=&limit=&offset=：平台用户目录（搜索用户名/邮箱，分页）。
func (h *api) listUsers(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	query := h.deps.DB.Model(&model.User{})
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query = query.Where("LOWER(username) LIKE ? OR LOWER(email) LIKE ?", like, like)
	}
	switch c.Query("filter") {
	case "disabled":
		query = query.Where("disabled_at IS NOT NULL")
	case "admin":
		query = query.Where("system_admin = true")
	case "bot":
		query = query.Where("is_bot = true")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "统计用户失败")
		return
	}
	var users []model.User
	if err := query.Order("created_at ASC").Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取用户失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users, "total": total, "limit": limit, "offset": offset})
}

// targetUser 解析路径用户并做「不能操作自己」防御。
func (h *api) targetUser(c *gin.Context, forbidSelf bool) (model.User, bool) {
	var target model.User
	userID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return target, false
	}
	if err := h.deps.DB.First(&target, "id = ?", userID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return target, false
	}
	if forbidSelf && target.ID == h.deps.CurrentUser(c).ID {
		fail(c, http.StatusBadRequest, "CANNOT_TARGET_SELF", "不能对自己执行此操作")
		return target, false
	}
	return target, true
}

// revokeAllSessions 吊销该用户两个受众的全部有效 refresh token。
func (h *api) revokeAllSessions(userID uuid.UUID) error {
	now := time.Now().UTC()
	return h.deps.DB.Model(&model.RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}

// otherActiveAdmins 除 exclude 外未被禁用的系统管理员数量（防自锁）。
func (h *api) otherActiveAdmins(exclude uuid.UUID) int64 {
	var count int64
	h.deps.DB.Model(&model.User{}).
		Where("system_admin = true AND disabled_at IS NULL AND id <> ?", exclude).
		Count(&count)
	return count
}

// disableUser POST /admin/users/{id}/disable：平台禁用账号并吊销全部会话。
func (h *api) disableUser(c *gin.Context) {
	target, ok := h.targetUser(c, true)
	if !ok {
		return
	}
	if target.Disabled() {
		c.JSON(http.StatusOK, target)
		return
	}
	if target.SystemAdmin && h.otherActiveAdmins(target.ID) == 0 {
		fail(c, http.StatusBadRequest, "LAST_SYSTEM_ADMIN", "不能禁用最后一名系统管理员")
		return
	}
	now := time.Now().UTC()
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", target.ID).Update("disabled_at", now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "禁用账号失败")
		return
	}
	if err := h.revokeAllSessions(target.ID); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销会话失败")
		return
	}
	// 强制下线：refresh token 吊销只阻止续期，已建立的 Gateway WS 须立即断开。
	h.revokeGatewaySessions(target.ID)
	h.audit(c, "platform.user_disable", target.ID.String(), map[string]any{"username": target.Username})
	target.DisabledAt = &now
	c.JSON(http.StatusOK, target)
}

// revokeGatewaySessions 经内部事件通知各 Gateway hub 立即断开该用户全部 WS 会话（4010）。
func (h *api) revokeGatewaySessions(userID uuid.UUID) {
	if h.deps.Bus == nil {
		return
	}
	h.deps.Bus.Publish(eventbus.Event{
		Type:    eventbus.InternalSessionRevoke,
		UserIDs: []uuid.UUID{userID},
	})
}

// enableUser POST /admin/users/{id}/enable：解除平台禁用。
func (h *api) enableUser(c *gin.Context) {
	target, ok := h.targetUser(c, false)
	if !ok {
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", target.ID).Update("disabled_at", nil).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解禁账号失败")
		return
	}
	h.audit(c, "platform.user_enable", target.ID.String(), map[string]any{"username": target.Username})
	target.DisabledAt = nil
	c.JSON(http.StatusOK, target)
}

type resetPasswordRequest struct {
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// resetPassword POST /admin/users/{id}/reset-password：管理员设置新密码并吊销全部会话。
func (h *api) resetPassword(c *gin.Context) {
	target, ok := h.targetUser(c, false)
	if !ok {
		return
	}
	var input resetPasswordRequest
	if !bind(c, &input) {
		return
	}
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_PASSWORD", err.Error())
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", target.ID).Update("password_hash", hash).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "重置密码失败")
		return
	}
	if err := h.revokeAllSessions(target.ID); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "吊销会话失败")
		return
	}
	// 强制下线：密码已被管理员重置，在线端立即断开，须用新密码重新登录。
	h.revokeGatewaySessions(target.ID)
	h.audit(c, "platform.user_reset_password", target.ID.String(), map[string]any{"username": target.Username})
	c.JSON(http.StatusOK, gin.H{"user_id": target.ID, "sessions_revoked": true})
}

type systemAdminRequest struct {
	SystemAdmin *bool `json:"system_admin" binding:"required"`
}

// patchSystemAdmin PATCH /admin/users/{id}/system-admin：授予/回收系统管理员。
// 回收后该用户的后台 refresh token 立即失效（后台 refresh 轮换会复查 SystemAdmin）。
func (h *api) patchSystemAdmin(c *gin.Context) {
	target, ok := h.targetUser(c, true)
	if !ok {
		return
	}
	var input systemAdminRequest
	if !bind(c, &input) {
		return
	}
	grant := *input.SystemAdmin
	if target.SystemAdmin == grant {
		c.JSON(http.StatusOK, target)
		return
	}
	if !grant && h.otherActiveAdmins(target.ID) == 0 {
		fail(c, http.StatusBadRequest, "LAST_SYSTEM_ADMIN", "不能回收最后一名系统管理员")
		return
	}
	if grant && target.IsBot {
		fail(c, http.StatusBadRequest, "BOT_CANNOT_BE_ADMIN", "机器人账号不能成为系统管理员")
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", target.ID).Update("system_admin", grant).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新系统管理员身份失败")
		return
	}
	if !grant {
		// 回收后吊销其后台受众会话，access token 到期后即无法续期。
		now := time.Now().UTC()
		h.deps.DB.Model(&model.RefreshToken{}).
			Where("user_id = ? AND audience = ? AND revoked_at IS NULL", target.ID, security.AudienceAdmin).
			Update("revoked_at", now)
	}
	h.audit(c, "platform.user_system_admin", target.ID.String(), map[string]any{
		"username": target.Username, "system_admin": grant,
	})
	target.SystemAdmin = grant
	c.JSON(http.StatusOK, target)
}
