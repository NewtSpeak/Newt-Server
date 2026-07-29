package guildapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// 服务器多 banner 列表（服务器外观专项）：每服多张、position 有序、可增删与重排序。
//
// 与既有单张 Guild.BannerURL（Newt-Desktop docs 02 FR-13）并存：单张字段与端点
// 保持兼容不动，多 banner 走独立的 GuildBanner 表与 /banners 复数端点。
// 二进制存储复用本文件夹 assets.go 的公开资产约定（DataDir/profile +
// /public-assets/profile/{name}，文件名含 guild ID 与纳秒版本号，不可枚举、
// 不可变、可长缓存），上传校验（MIME 魔数嗅探 + 8MiB 上限）同 saveGuildAssetFile。
//
// 生命周期取舍：不引用 message.Attachment——附件 GC 会清理未绑定消息的附件
//（24h）且消息保留策略会连带删附件，banner 随服存续、由服管显式增删，混用
// 会被消息清理误删，故独立建表（见 model.GuildBanner 注释）。

// defaultGuildBannerLimit 每服 banner 数量默认上限。
const defaultGuildBannerLimit = 10

// guildBannerLimit 每服 banner 数量上限：环境变量 GUILD_BANNER_MAX_COUNT 全局可调
//（部署级配置，与 clientapi 的 CLIENT_SIGNUP_ENABLED 同模式，避免为单个开关改动
// config 包；未按服细分——banner 是展示性资产，无按服差异化的成本诉求）。
func guildBannerLimit() int {
	raw := os.Getenv("GUILD_BANNER_MAX_COUNT")
	if raw == "" {
		return defaultGuildBannerLimit
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return defaultGuildBannerLimit
	}
	return limit
}

// loadGuildBanners 读取某服全部 banner（position 升序，创建时间兜底稳定）。
func loadGuildBanners(db *gorm.DB, guildID uuid.UUID) ([]model.GuildBanner, error) {
	banners := []model.GuildBanner{}
	err := db.Where("guild_id = ?", guildID).
		Order("position ASC, created_at ASC").Find(&banners).Error
	return banners, err
}

// GuildWithBanners 服务器列表响应条目：Guild 全部字段平铺 + banners 数组。
type GuildWithBanners struct {
	model.Guild
	Banners []model.GuildBanner `json:"banners"`
}

// WithBanners 批量装配服务器列表的 banners（后台 GET /guilds 与用户端
// GET /users/@me/guilds 复用；一次查询避免 N+1）。
func WithBanners(db *gorm.DB, guilds []model.Guild) []GuildWithBanners {
	result := make([]GuildWithBanners, 0, len(guilds))
	if len(guilds) == 0 {
		return result
	}
	ids := make([]uuid.UUID, 0, len(guilds))
	for _, guild := range guilds {
		ids = append(ids, guild.ID)
	}
	grouped := map[uuid.UUID][]model.GuildBanner{}
	var rows []model.GuildBanner
	if err := db.Where("guild_id IN ?", ids).
		Order("position ASC, created_at ASC").Find(&rows).Error; err == nil {
		for _, row := range rows {
			grouped[row.GuildID] = append(grouped[row.GuildID], row)
		}
	}
	for _, guild := range guilds {
		banners := grouped[guild.ID]
		if banners == nil {
			banners = []model.GuildBanner{}
		}
		result = append(result, GuildWithBanners{Guild: guild, Banners: banners})
	}
	return result
}

// publishGuildBannersUpdate banner 增删/排序后广播 GUILD_UPDATE（载荷带最新 banners 全量）。
func (h *api) publishGuildBannersUpdate(guild model.Guild, banners []model.GuildBanner) {
	guildID := guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildBannersUpdatePayload(guild, banners),
	})
}

// listGuildBanners godoc
// @Summary 列出服务器 banner（position 升序）
// @Tags Guild外观
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]any "guild_id / banners / limit"
// @Router /guilds/{guildID}/banners [get]
//
// 成员可读；非成员一律 404（防扫频）。
func (h *api) listGuildBanners(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	banners, err := loadGuildBanners(h.deps.DB, ctx.Guild.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 banner 失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"guild_id": ctx.Guild.ID,
		"banners":  banners,
		"limit":    guildBannerLimit(),
	})
}

// addGuildBanner godoc
// @Summary 新增服务器 banner（multipart 字段 file）
// @Tags Guild外观
// @Security BearerAuth
// @Accept multipart/form-data
// @Produce json
// @Success 201 {object} map[string]any "banner / banners"
// @Failure 400 {object} map[string]any "BANNER_LIMIT_REACHED / UNSUPPORTED_TYPE / FILE_TOO_LARGE"
// @Failure 403 {object} map[string]any "MISSING_PERMISSION"
// @Router /guilds/{guildID}/banners [post]
//
// 需 MANAGE_GUILD；服务端魔数嗅探仅收 PNG/JPEG/WebP/GIF、≤8MiB；
// 追加到列表末尾（position = 当前最大 + 1）。
func (h *api) addGuildBanner(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageGuild)
	if !ok {
		return
	}
	var count int64
	if err := h.deps.DB.Model(&model.GuildBanner{}).Where("guild_id = ?", ctx.Guild.ID).Count(&count).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 banner 失败")
		return
	}
	limit := guildBannerLimit()
	if count >= int64(limit) {
		fail(c, http.StatusBadRequest, "BANNER_LIMIT_REACHED", fmt.Sprintf("banner 数量已达上限 %d 张", limit))
		return
	}
	url, ok := h.saveGuildAssetFile(c, ctx.Guild.ID, "banners")
	if !ok {
		return
	}
	var maxPosition int
	h.deps.DB.Model(&model.GuildBanner{}).Where("guild_id = ?", ctx.Guild.ID).
		Select("COALESCE(MAX(position), -1)").Scan(&maxPosition)
	banner := model.GuildBanner{
		ID:       uuid.New(),
		GuildID:  ctx.Guild.ID,
		URL:      url,
		Position: maxPosition + 1,
	}
	if err := h.deps.DB.Create(&banner).Error; err != nil {
		removeGuildAssetFile(filepath.Join(h.deps.Cfg.DataDir, "profile"), url)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存 banner 失败")
		return
	}
	h.audit(ctx, user, "guild.banner_add", "guild_banner", banner.ID.String(), map[string]any{"url": url, "position": banner.Position})
	banners, _ := loadGuildBanners(h.deps.DB, ctx.Guild.ID)
	h.publishGuildBannersUpdate(ctx.Guild, banners)
	c.JSON(http.StatusCreated, gin.H{"banner": banner, "banners": banners})
}

// removeGuildBanner godoc
// @Summary 删除服务器 banner
// @Tags Guild外观
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]any "banners（删除后的剩余列表）"
// @Failure 403 {object} map[string]any "MISSING_PERMISSION"
// @Failure 404 {object} map[string]any "NOT_FOUND（含归属校验：他服资产不可删）"
// @Router /guilds/{guildID}/banners/{bannerID} [delete]
//
// 需 MANAGE_GUILD；banner 必须属于路径中的服务器（归属校验，防跨服删除）；
// 删除后剩余 banner 重新连续编号。
func (h *api) removeGuildBanner(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageGuild)
	if !ok {
		return
	}
	bannerID, err := uuid.Parse(c.Param("bannerID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "banner 不存在")
		return
	}
	// 归属校验：guild_id 一并入条件，他服资产等同不存在（404，不泄露信息）。
	var banner model.GuildBanner
	if err := h.deps.DB.First(&banner, "id = ? AND guild_id = ?", bannerID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "banner 不存在")
		return
	}
	if err := h.deps.DB.Delete(&model.GuildBanner{}, "id = ?", banner.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除 banner 失败")
		return
	}
	removeGuildAssetFile(filepath.Join(h.deps.Cfg.DataDir, "profile"), banner.URL)
	// 剩余 banner 重新连续编号（保持 0..n-1 无空洞，排序语义稳定）。
	banners, _ := loadGuildBanners(h.deps.DB, ctx.Guild.ID)
	for index := range banners {
		if banners[index].Position != index {
			h.deps.DB.Model(&model.GuildBanner{}).Where("id = ?", banners[index].ID).Update("position", index)
			banners[index].Position = index
		}
	}
	h.audit(ctx, user, "guild.banner_remove", "guild_banner", banner.ID.String(), map[string]any{"url": banner.URL})
	h.publishGuildBannersUpdate(ctx.Guild, banners)
	c.JSON(http.StatusOK, gin.H{"banners": banners})
}

type reorderGuildBannersRequest struct {
	// BannerIDs 全量有序 banner ID 数组：必须与当前列表一一对应（不多不少、不重复），
	// 服务端按数组下标重排 position。选全量数组而非 [{id, position}] 增量：banner
	// 数量小（≤上限 10）且客户端总是整条拖拽排序，全量提交天然原子、无位置空洞
	// 与重复位置歧义（角色排序用增量 [{id, position}] 是因为角色可只动局部层级）。
	BannerIDs []uuid.UUID `json:"banner_ids" binding:"required,min=1"`
}

// reorderGuildBanners godoc
// @Summary 重排序服务器 banner（全量有序 ID 数组）
// @Tags Guild外观
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param body body reorderGuildBannersRequest true "全量有序 banner ID 数组"
// @Success 200 {object} map[string]any "banners（重排后的列表）"
// @Failure 400 {object} map[string]any "BANNER_IDS_MISMATCH"
// @Failure 403 {object} map[string]any "MISSING_PERMISSION"
// @Router /guilds/{guildID}/banners [patch]
//
// 需 MANAGE_GUILD；banner_ids 必须恰好覆盖本服全部 banner，事务内整体生效。
func (h *api) reorderGuildBanners(c *gin.Context) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageGuild)
	if !ok {
		return
	}
	var input reorderGuildBannersRequest
	if !bind(c, &input) {
		return
	}
	existing, err := loadGuildBanners(h.deps.DB, ctx.Guild.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取 banner 失败")
		return
	}
	owned := make(map[uuid.UUID]bool, len(existing))
	for _, banner := range existing {
		owned[banner.ID] = true
	}
	seen := make(map[uuid.UUID]bool, len(input.BannerIDs))
	for _, id := range input.BannerIDs {
		if !owned[id] || seen[id] {
			fail(c, http.StatusBadRequest, "BANNER_IDS_MISMATCH", "banner_ids 必须恰好覆盖本服全部 banner 且不重复")
			return
		}
		seen[id] = true
	}
	if len(input.BannerIDs) != len(existing) {
		fail(c, http.StatusBadRequest, "BANNER_IDS_MISMATCH", "banner_ids 必须恰好覆盖本服全部 banner 且不重复")
		return
	}
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		for index, id := range input.BannerIDs {
			if err := tx.Model(&model.GuildBanner{}).Where("id = ? AND guild_id = ?", id, ctx.Guild.ID).
				Update("position", index).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存 banner 排序失败")
		return
	}
	h.audit(ctx, user, "guild.banner_reorder", "guild", ctx.Guild.ID.String(), map[string]any{"banner_ids": input.BannerIDs})
	banners, _ := loadGuildBanners(h.deps.DB, ctx.Guild.ID)
	h.publishGuildBannersUpdate(ctx.Guild, banners)
	c.JSON(http.StatusOK, gin.H{"banners": banners})
}
