package social

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm/clause"
)

type privacyView struct {
	FriendRequestFrom         string                       `json:"friend_request_from"`
	DmFrom                    string                       `json:"dm_from"`
	MessageRequestFilter      bool                         `json:"message_request_filter"`
	ShowMutualGuilds          bool                         `json:"show_mutual_guilds"`
	PublicProfileToNonFriends bool                         `json:"public_profile_to_non_friends"`
	GuildOverrides            map[string]guildPrivacyView  `json:"guild_overrides"`
}

type guildPrivacyView struct {
	AllowDM bool `json:"allow_dm"`
}

func privacyToView(p model.PrivacySettings, overrides map[string]guildPrivacyView) privacyView {
	if overrides == nil {
		overrides = map[string]guildPrivacyView{}
	}
	return privacyView{
		FriendRequestFrom:         p.FriendRequestFrom,
		DmFrom:                    p.DmFrom,
		MessageRequestFilter:      p.MessageRequestFilter,
		ShowMutualGuilds:          p.ShowMutualGuilds,
		PublicProfileToNonFriends: p.PublicProfileToNonFriends,
		GuildOverrides:            overrides,
	}
}

func (h *api) loadGuildOverrides(userID uuid.UUID) map[string]guildPrivacyView {
	var rows []model.GuildMemberPrivacy
	_ = h.deps.DB.Where("user_id = ?", userID).Find(&rows).Error
	out := make(map[string]guildPrivacyView, len(rows))
	for _, r := range rows {
		out[r.GuildID.String()] = guildPrivacyView{AllowDM: r.AllowDM}
	}
	return out
}

// getPrivacy GET /users/@me/privacy
func (h *api) getPrivacy(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	p := h.loadOrDefaultPrivacy(user.ID)
	c.JSON(http.StatusOK, privacyToView(p, h.loadGuildOverrides(user.ID)))
}

type privacyPatch struct {
	FriendRequestFrom         *string `json:"friend_request_from"`
	DmFrom                    *string `json:"dm_from"`
	MessageRequestFilter      *bool   `json:"message_request_filter"`
	ShowMutualGuilds          *bool   `json:"show_mutual_guilds"`
	PublicProfileToNonFriends *bool   `json:"public_profile_to_non_friends"`
}

func validFriendFrom(v string) bool {
	switch v {
	case model.FriendRequestEveryone, model.FriendRequestMutualFriends,
		model.FriendRequestMutualGuilds, model.FriendRequestNobody:
		return true
	}
	return false
}

func validDmFrom(v string) bool {
	switch v {
	case model.DmFromEveryone, model.DmFromFriends, model.DmFromMutualGuilds, model.DmFromNobody:
		return true
	}
	return false
}

// patchPrivacy PATCH /users/@me/privacy
func (h *api) patchPrivacy(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input privacyPatch
	if !bind(c, &input) {
		return
	}
	p := h.loadOrDefaultPrivacy(user.ID)
	p.UserID = user.ID
	if input.FriendRequestFrom != nil {
		if !validFriendFrom(*input.FriendRequestFrom) {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "friend_request_from 无效")
			return
		}
		p.FriendRequestFrom = *input.FriendRequestFrom
	}
	if input.DmFrom != nil {
		if !validDmFrom(*input.DmFrom) {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "dm_from 无效")
			return
		}
		p.DmFrom = *input.DmFrom
	}
	if input.MessageRequestFilter != nil {
		p.MessageRequestFilter = *input.MessageRequestFilter
	}
	if input.ShowMutualGuilds != nil {
		p.ShowMutualGuilds = *input.ShowMutualGuilds
	}
	if input.PublicProfileToNonFriends != nil {
		p.PublicProfileToNonFriends = *input.PublicProfileToNonFriends
	}
	p.UpdatedAt = time.Now().UTC()
	if err := h.deps.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"friend_request_from", "dm_from", "message_request_filter",
			"show_mutual_guilds", "public_profile_to_non_friends", "updated_at",
		}),
	}).Create(&p).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存隐私设置失败")
		return
	}
	view := privacyToView(p, h.loadGuildOverrides(user.ID))
	raw, _ := json.Marshal(view)
	// 兼容客户端：推 USER_SETTINGS_UPDATE，settings.privacy 顶层键
	settingsDoc, _ := json.Marshal(map[string]any{"privacy": view})
	h.publishToUser(user.ID, eventbus.EventUserSettingsUpdate, eventbus.NewUserSettingsUpdatePayload(settingsDoc))
	_ = raw
	c.JSON(http.StatusOK, view)
}

type guildPrivacyPut struct {
	AllowDM *bool `json:"allow_dm" binding:"required"`
}

// putGuildPrivacy PUT /users/@me/guilds/:guildID/privacy
func (h *api) putGuildPrivacy(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	// 须为本服成员
	var n int64
	h.deps.DB.Model(&model.Member{}).Where("user_id = ? AND guild_id = ?", user.ID, guildID).Count(&n)
	if n == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	var input guildPrivacyPut
	if !bind(c, &input) {
		return
	}
	allow := input.AllowDM != nil && *input.AllowDM
	row := model.GuildMemberPrivacy{
		UserID: user.ID, GuildID: guildID, AllowDM: allow, UpdatedAt: time.Now().UTC(),
	}
	if allow {
		// 默认 true：删除覆盖行（稀疏）
		_ = h.deps.DB.Delete(&model.GuildMemberPrivacy{}, "user_id = ? AND guild_id = ?", user.ID, guildID).Error
	} else {
		if err := h.deps.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "guild_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"allow_dm", "updated_at"}),
		}).Create(&row).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存失败")
			return
		}
	}
	p := h.loadOrDefaultPrivacy(user.ID)
	view := privacyToView(p, h.loadGuildOverrides(user.ID))
	settingsDoc, _ := json.Marshal(map[string]any{"privacy": view})
	h.publishToUser(user.ID, eventbus.EventUserSettingsUpdate, eventbus.NewUserSettingsUpdatePayload(settingsDoc))
	c.JSON(http.StatusOK, gin.H{"guild_id": guildID, "allow_dm": allow})
}
