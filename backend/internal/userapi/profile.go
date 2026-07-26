package userapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/activity"
	"github.com/owlspeak/owl-server/backend/internal/cosmetics"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/platformbadge"
)

// me GET /users/@me：当前用户完整资料（含 email 等私有字段，本人可见）。
// 系统所有者自动附带 platform badge（登录与刷新后保持一致）。
func (h *api) me(c *gin.Context) {
	c.JSON(http.StatusOK, platformbadge.ViewOf(h.deps.CurrentUser(c)))
}

type patchMeRequest struct {
	// DisplayName 显示名（1–32 字符，docs 01 FR-12）；传空字符串清除（回退用户名展示）。
	DisplayName *string `json:"display_name"`
	// Bio 个性签名（≤190 字符）；传空字符串清除。
	Bio *string `json:"bio"`
}

// patchMe PATCH /users/@me：修改显示名 / 个性签名；成功后发布 USER_UPDATE。
func (h *api) patchMe(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input patchMeRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{}
	if input.DisplayName != nil {
		name := strings.TrimSpace(*input.DisplayName)
		if length := utf8.RuneCountInString(name); name != "" && (length < 1 || length > 32) {
			fail(c, http.StatusBadRequest, "INVALID_DISPLAY_NAME", "显示名长度须为 1 到 32 个字符")
			return
		}
		updates["display_name"] = name
	}
	if input.Bio != nil {
		bio := strings.TrimSpace(*input.Bio)
		if utf8.RuneCountInString(bio) > 190 {
			fail(c, http.StatusBadRequest, "INVALID_BIO", "个性签名不能超过 190 个字符")
			return
		}
		updates["bio"] = bio
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "EMPTY_PATCH", "没有需要更新的字段")
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取资料失败")
		return
	}
	h.publishUserUpdate(fresh)
	c.JSON(http.StatusOK, fresh)
}

// publicProfile 公开资料投影：不含 email / system_admin 等私有字段。
// 与 USER_UPDATE 载荷字段对齐，便于客户端资料卡统一渲染。
type publicProfile struct {
	ID             uuid.UUID `json:"id"`
	Username       string    `json:"username"`
	DisplayName    string    `json:"display_name"`
	Avatar         string    `json:"avatar"` // 头像可访问 URL（/public-assets/profile/...），空串表示未设置
	AvatarAnimated bool      `json:"avatar_animated"`
	Banner         string    `json:"banner"`
	AccentColor    string    `json:"accent_color"`
	Bio            string    `json:"bio"`
	// Cosmetics 全量装备投影（full 模式，含 profile_border/profile_effect），
	// 资料卡单请求拿全装扮；ACL 与本投影一致（共同 guild 才可见）。
	Cosmetics map[string]cosmetics.EquippedSlotView `json:"cosmetics,omitempty"`
	// ActivityLevel 平台活跃度等级（资料卡展示；0 = 暂无活跃记录）。
	ActivityLevel int `json:"activity_level"`
}

// publicProfile GET /users/:id：查看他人公开资料。
// 仅限与请求者共享至少一个 guild 的用户（本人恒可查自己）；其余一律 404
//（含目标不存在），不区分「不存在」与「无共同服务器」，防用户枚举。
// 两个平面语义一致：SystemAdmin 不短路（后台管理员如需全量用户管理走后台专属端点）。
func (h *api) publicProfile(c *gin.Context) {
	requester := h.deps.CurrentUser(c)
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	if targetID != requester.ID {
		var shared int64
		err := h.deps.DB.Model(&model.Member{}).
			Where("user_id = ? AND guild_id IN (?)", targetID,
				h.deps.DB.Model(&model.Member{}).Select("guild_id").Where("user_id = ?", requester.ID)).
			Count(&shared).Error
		if err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询失败")
			return
		}
		if shared == 0 {
			fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
			return
		}
	}
	var target model.User
	if err := h.deps.DB.First(&target, "id = ?", targetID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	equipped := cosmetics.ResolveEquippedForUsers(h.deps.DB, []uuid.UUID{target.ID}, false)
	c.JSON(http.StatusOK, publicProfile{
		ID: target.ID, Username: target.Username, DisplayName: target.DisplayName,
		Avatar: target.AvatarURL, AvatarAnimated: target.AvatarAnimated,
		Banner: target.BannerURL, AccentColor: target.AccentColor, Bio: target.Bio,
		Cosmetics:     equipped[target.ID],
		ActivityLevel: activity.LevelOf(h.deps.DB, target.ID),
	})
}
