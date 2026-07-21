package customization

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
)

// api 模块内共享句柄。clientPlane=true 表示挂载在用户端（/gapi/v1）：
// 权限计算按普通用户语义（系统管理员不短路，对齐 clientapi 设计）。
type api struct {
	deps        appdeps.Deps
	clientPlane bool
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// guildCtx 加载当前用户在路径服务器内的权限上下文；不可见一律 404（防扫频）。
// 系统所有者（system_admin）在用户端亦保留全权限短路（docs 04 FR-32）。
func (h *api) guildCtx(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	return ctx, user, true
}

func (h *api) audit(ctx *perms.GuildContext, actor model.User, action, targetType, targetID string, detail map[string]any) {
	actorID := actor.ID
	actorType := "user"
	if ctx.SystemAdmin {
		actorType = "system_admin"
	} else if ctx.Owner {
		actorType = "guild_admin"
	}
	guildID := ctx.Guild.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID:    &actorID,
		ActorType:  actorType,
		GuildID:    &guildID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
	})
}

// publishToUserGuilds 将用户资料变更事件广播到该用户加入的全部服务器
//（在线成员据此刷新展示），并定向推给本人的全部连接。
func (h *api) publishToUserGuilds(userID uuid.UUID, eventType string, payload any) {
	if h.deps.Bus == nil {
		return
	}
	var guildIDs []uuid.UUID
	_ = h.deps.DB.Model(&model.Member{}).Where("user_id = ?", userID).Pluck("guild_id", &guildIDs).Error
	for i := range guildIDs {
		guildID := guildIDs[i]
		h.deps.Bus.Publish(eventbus.Event{Type: eventType, GuildID: &guildID, Payload: payload})
	}
	h.deps.Bus.Publish(eventbus.Event{Type: eventType, UserIDs: []uuid.UUID{userID}, Payload: payload})
}
