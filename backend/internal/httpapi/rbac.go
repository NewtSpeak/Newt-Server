package httpapi

// RBAC 结构管理端点（角色/频道/权限覆盖/成员角色绑定）已抽出为共享包
// internal/guildapi（双认证平面复用，server.New 装配挂载），本文件仅保留
// 服务器创建/列表与成员列表所需的最小上下文。

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/guildapi"
	"github.com/newtspeak/newt-server/backend/internal/guildseed"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/snapshot"
	"gorm.io/gorm"
)

type createGuildRequest struct {
	Name string `json:"name" binding:"required,min=2,max=100"`
}

// createGuild godoc
// @Summary 创建服务器
// @Tags RBAC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param body body createGuildRequest true "服务器资料"
// @Success 201 {object} model.Guild
// @Router /guilds [post]
func (a *API) createGuild(c *gin.Context) {
	var input createGuildRequest
	if !bind(c, &input) {
		return
	}
	user := currentUser(c)
	guild := model.Guild{ID: uuid.New(), Name: strings.TrimSpace(input.Name), OwnerUserID: user.ID}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&guild).Error; err != nil {
			return err
		}
		member := model.Member{ID: uuid.New(), GuildID: guild.ID, UserID: user.ID}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		// 默认角色种子（@everyone + 内置管理员）与用户端建服共用同一实现。
		if err := guildseed.SeedDefaultRoles(tx, guild.ID); err != nil {
			return err
		}
		return guildseed.BindOwnerToManagedAdmin(tx, guild.ID, member.ID)
	})
	if err != nil {
		fail(c, 500, "DATABASE_ERROR", "创建服务器失败")
		return
	}
	// GUILD_CREATE 对建服者定向下发全量快照（docs 14 §3.2）。
	if payload, err := snapshot.NewGuildCreatePayload(a.db, user, guild.ID); err == nil {
		a.publish(eventbus.Event{
			Type: eventbus.EventGuildCreate, GuildID: &guild.ID,
			UserIDs: []uuid.UUID{user.ID}, Payload: payload,
		})
	}
	c.JSON(http.StatusCreated, guild)
}

// listGuilds godoc
// @Summary 列出当前用户加入的服务器（含 icon_url 与 banners 列表）
// @Tags RBAC
// @Security BearerAuth
// @Produce json
// @Success 200 {array} guildapi.GuildWithBanners
// @Router /guilds [get]
func (a *API) listGuilds(c *gin.Context) {
	user := currentUser(c)
	var guilds []model.Guild
	query := a.db.Order("guilds.created_at DESC")
	if !user.SystemAdmin {
		query = query.Joins("JOIN members ON members.guild_id = guilds.id").Where("members.user_id = ?", user.ID)
	}
	if err := query.Find(&guilds).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取服务器列表失败")
		return
	}
	// 附带每服 banner 列表（服务器外观专项）：Guild 字段平铺不变，新增 banners 键。
	c.JSON(http.StatusOK, guildapi.WithBanners(a.db, guilds))
}

// guildForUser 加载服务器并校验当前用户可见性（成员或系统管理员）；不可见统一 404。
func (a *API) guildForUser(c *gin.Context) (model.Guild, bool) {
	user := currentUser(c)
	var guild model.Guild
	if err := a.db.First(&guild, "id = ?", c.Param("guildID")).Error; err != nil {
		fail(c, 404, "NOT_FOUND", "服务器不存在")
		return guild, false
	}
	if !user.SystemAdmin {
		var member model.Member
		if err := a.db.First(&member, "guild_id = ? AND user_id = ?", guild.ID, user.ID).Error; err != nil {
			fail(c, 404, "NOT_FOUND", "服务器不存在")
			return guild, false
		}
	}
	return guild, true
}
