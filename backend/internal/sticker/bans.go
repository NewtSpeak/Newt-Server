package sticker

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm/clause"
)

type banRequest struct {
	Reason string `json:"reason"`
}

// listGuildBans GET /guilds/{guild_id}/sticker-pack-bans
func (h *api) listGuildBans(c *gin.Context) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	ctx, err := perms.LoadGuild(h.db(), user, guildID)
	if err != nil {
		notFound(c)
		return
	}
	// 成员可读 ban 列表（选择器过滤）；写操作需 MANAGE_EXPRESSIONS
	_ = ctx
	var bans []model.GuildPackBan
	if err := h.db().Where("guild_id = ?", guildID).Order("created_at DESC").Find(&bans).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 ban 列表失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"bans": bans})
}

// banGuildPack PUT /guilds/{guild_id}/sticker-pack-bans/{pack_id}
func (h *api) banGuildPack(c *gin.Context) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	ctx, err := perms.LoadGuild(h.db(), user, guildID)
	if err != nil {
		notFound(c)
		return
	}
	if !ctx.SystemAdmin && !rbac.Has(ctx.Permissions, rbac.ManageExpressions) && !ctx.Owner {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "需要 MANAGE_EXPRESSIONS 权限")
		return
	}
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return
	}
	var input banRequest
	_ = c.ShouldBindJSON(&input) // 可选 body
	reason := clampRunes(strings.TrimSpace(input.Reason), maxBanReasonRunes)
	if utf8.RuneCountInString(reason) > maxBanReasonRunes {
		reason = clampRunes(reason, maxBanReasonRunes)
	}
	ban := model.GuildPackBan{
		GuildID:   guildID,
		PackID:    packID,
		BannedBy:  user.ID,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}
	err = h.db().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "guild_id"}, {Name: "pack_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"banned_by", "reason", "created_at"}),
	}).Create(&ban).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "ban 失败")
		return
	}
	payload := gin.H{
		"guild_id": guildID,
		"pack_id":  strID(packID),
		"reason":   reason,
		"banned_by": user.ID,
	}
	h.publishToGuild(guildID, eventbus.EventGuildStickerPackBanAdd, payload)
	audit.Log(h.db(), audit.Entry{
		ActorID: &user.ID, ActorType: "guild_admin", GuildID: &guildID,
		Action: "sticker.pack.guild_ban", TargetType: "sticker_pack", TargetID: strID(packID),
		Detail: map[string]any{"reason": reason},
	})
	c.JSON(http.StatusOK, ban)
}

// unbanGuildPack DELETE /guilds/{guild_id}/sticker-pack-bans/{pack_id}
func (h *api) unbanGuildPack(c *gin.Context) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	ctx, err := perms.LoadGuild(h.db(), user, guildID)
	if err != nil {
		notFound(c)
		return
	}
	if !ctx.SystemAdmin && !rbac.Has(ctx.Permissions, rbac.ManageExpressions) && !ctx.Owner {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "需要 MANAGE_EXPRESSIONS 权限")
		return
	}
	result := h.db().Where("guild_id = ? AND pack_id = ?", guildID, packID).
		Delete(&model.GuildPackBan{})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解 ban 失败")
		return
	}
	payload := gin.H{"guild_id": guildID, "pack_id": strID(packID)}
	h.publishToGuild(guildID, eventbus.EventGuildStickerPackBanRemove, payload)
	audit.Log(h.db(), audit.Entry{
		ActorID: &user.ID, ActorType: "guild_admin", GuildID: &guildID,
		Action: "sticker.pack.guild_unban", TargetType: "sticker_pack", TargetID: strID(packID),
	})
	c.Status(http.StatusNoContent)
}
