package platformadmin

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/gorm"
)

type changeUsernameRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// changeOwnUsername PATCH /admin/account/username：系统管理员修改自己的登录用户名。
// 改名不吊销任何会话：JWT 与 refresh token 均按 user id 关联，用户名不参与鉴权，
// 现有登录全部继续有效；下次登录使用新用户名或邮箱。
func (h *api) changeOwnUsername(c *gin.Context) {
	var input changeUsernameRequest
	if !bind(c, &input) {
		return
	}
	user := h.deps.CurrentUser(c)

	username := strings.TrimSpace(input.Username)
	if n := utf8.RuneCountInString(username); n < 2 || n > 32 {
		fail(c, http.StatusBadRequest, "INVALID_USERNAME", "用户名长度需为 2-32 个字符")
		return
	}
	// 登录 identifier 同时匹配用户名与邮箱，含 @ 或空白的用户名会造成登录歧义。
	if strings.ContainsRune(username, '@') || strings.ContainsFunc(username, unicode.IsSpace) {
		fail(c, http.StatusBadRequest, "INVALID_USERNAME", "用户名不能包含空白字符或 @")
		return
	}
	// 改登录标识属账号接管类操作，access token 泄露不应足以完成，须当前密码二次确认。
	if !security.VerifyPassword(user.PasswordHash, input.Password) {
		fail(c, http.StatusForbidden, "INVALID_PASSWORD", "当前密码不正确")
		return
	}
	if username == user.Username {
		c.JSON(http.StatusOK, user)
		return
	}
	// 登录查询用 LOWER(username) 匹配，唯一索引（大小写敏感）拦不住大小写变体撞名，
	// 须 LOWER 预检；排除自己以放行自身大小写重命名。并发竞态窗口由唯一索引兜底。
	var conflicts int64
	if err := h.deps.DB.Model(&model.User{}).
		Where("LOWER(username) = ? AND id <> ?", strings.ToLower(username), user.ID).
		Count(&conflicts).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "校验用户名失败")
		return
	}
	if conflicts > 0 {
		fail(c, http.StatusConflict, "USERNAME_TAKEN", "该用户名已被占用")
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Update("username", username).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			fail(c, http.StatusConflict, "USERNAME_TAKEN", "该用户名已被占用")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "修改用户名失败")
		return
	}
	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取用户失败")
		return
	}
	h.publishUserUpdate(fresh)
	h.audit(c, "platform.account_username_change", user.ID.String(), map[string]any{
		"from": user.Username, "to": fresh.Username,
	})
	c.JSON(http.StatusOK, fresh)
}

// publishUserUpdate 资料变更事件：广播给与该用户共享 guild 的在线成员 + 定向发本人全部端。
// 与 userapi 的同名实现保持一致（platformadmin 不依赖 userapi，就地实现）。
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
