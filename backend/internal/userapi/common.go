package userapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

// api 模块内共享句柄。tokens 仅用于从 Authorization 头提取 sid（会话链）claim：
// 与两个平面共用同一 JWT secret，签名校验结果一致；受众校验已由 deps.Auth 完成。
type api struct {
	deps   appdeps.Deps
	tokens *security.TokenManager
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

// currentSessionID 从 Authorization 头提取当前登录会话链 ID；
// 旧 token（无 sid claim）返回 uuid.Nil。
func (h *api) currentSessionID(c *gin.Context) uuid.UUID {
	raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	sid, err := uuid.Parse(h.tokens.TokenSessionID(raw))
	if err != nil {
		return uuid.Nil
	}
	return sid
}

// publishUserUpdate 资料变更事件：广播给与该用户共享 guild 的在线成员
//（按 guild 广播，复用 hub 成员分发）+ 定向发给本人全部端。
func (h *api) publishUserUpdate(user model.User) {
	if h.deps.Bus == nil {
		return
	}
	payload := eventbus.NewUserUpdatePayload(user)
	var guildIDs []uuid.UUID
	_ = h.deps.DB.Model(&model.Member{}).Where("user_id = ?", user.ID).Pluck("guild_id", &guildIDs).Error
	for i := range guildIDs {
		guildID := guildIDs[i]
		h.deps.Bus.Publish(eventbus.Event{Type: eventbus.EventUserUpdate, GuildID: &guildID, Payload: payload})
	}
	h.deps.Bus.Publish(eventbus.Event{Type: eventbus.EventUserUpdate, UserIDs: []uuid.UUID{user.ID}, Payload: payload})
}
