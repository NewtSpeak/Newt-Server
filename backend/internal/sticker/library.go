package sticker

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type copyItemRequest struct {
	SourceItemID string `json:"source_item_id" binding:"required"`
	Name         string `json:"name"`
}

// listLibrary GET /users/@me/sticker-library
func (h *api) listLibrary(c *gin.Context) {
	user := h.currentUser(c)
	includeHidden := c.Query("include_hidden") == "true"
	q := h.db().Where("user_id = ?", user.ID)
	if !includeHidden {
		q = q.Where("status = ?", model.UserPackLibraryActive)
	}
	var libs []model.UserPackLibrary
	if err := q.Order("sort_order ASC, installed_at DESC").Find(&libs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取贴图库失败")
		return
	}
	packIDs := make([]int64, 0, len(libs))
	for _, l := range libs {
		packIDs = append(packIDs, l.PackID)
	}
	packMap := map[int64]model.StickerPack{}
	if len(packIDs) > 0 {
		var packs []model.StickerPack
		_ = h.db().Where("id IN ?", packIDs).Find(&packs).Error
		for _, p := range packs {
			packMap[p.ID] = refreshSoftDeleteStatus(h.db(), p)
		}
	}
	entries := make([]libraryEntryView, 0, len(libs))
	for _, l := range libs {
		e := libraryEntryView{
			PackID:      strID(l.PackID),
			Status:      l.Status,
			InstalledAt: l.InstalledAt,
			SortOrder:   l.SortOrder,
		}
		if p, ok := packMap[l.PackID]; ok && p.Status != model.StickerPackPurged {
			v := h.packView(p, h.countActiveItems(p.ID), nil)
			e.Pack = &v
		}
		entries = append(entries, e)
	}
	c.JSON(http.StatusOK, gin.H{"library": entries})
}

// installPack PUT /users/@me/sticker-library/{pack_id}
func (h *api) installPack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return
	}
	pack = refreshSoftDeleteStatus(h.db(), pack)

	// 自建包无需 Install
	if pack.OwnerUserID == user.ID {
		fail(c, http.StatusBadRequest, "OWN_PACK", "自建包已在库中，无需 Install")
		return
	}

	var ctxGuild uuid.UUID
	if raw := c.Query("guild_id"); raw != "" {
		if gid, err := uuid.Parse(raw); err == nil {
			ctxGuild = gid
		}
	}
	// 服独属：必须在本服上下文
	if pack.Scope == model.StickerScopeGuild {
		if pack.GuildID == nil {
			notFound(c)
			return
		}
		if _, err := perms.LoadGuild(h.db(), user, *pack.GuildID); err != nil {
			fail(c, http.StatusForbidden, "NOT_MEMBER", "须为本服成员才能 Install 服独属包")
			return
		}
		ctxGuild = *pack.GuildID
	}

	if err := canInstall(h.db(), pack, ctxGuild); err != nil {
		fail(c, http.StatusForbidden, "CANNOT_INSTALL", err.Error())
		return
	}

	now := time.Now().UTC()
	lib := model.UserPackLibrary{
		UserID:      user.ID,
		PackID:      pack.ID,
		Status:      model.UserPackLibraryActive,
		InstalledAt: now,
		SortOrder:   0,
	}
	// 幂等：已存在则更新为 active
	err := h.db().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "pack_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "installed_at"}),
	}).Create(&lib).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "Install 失败")
		return
	}
	view := libraryEntryView{
		PackID:      strID(pack.ID),
		Status:      model.UserPackLibraryActive,
		InstalledAt: now,
		Pack:        ptrPack(h.packView(pack, h.countActiveItems(pack.ID), nil)),
	}
	h.publishToUser(user.ID, eventbus.EventStickerLibraryUpdate, view)
	c.JSON(http.StatusOK, view)
}

// uninstallPack DELETE /users/@me/sticker-library/{pack_id}
func (h *api) uninstallPack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	result := h.db().Where("user_id = ? AND pack_id = ?", user.ID, packID).
		Delete(&model.UserPackLibrary{})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "卸装失败")
		return
	}
	h.publishToUser(user.ID, eventbus.EventStickerLibraryUpdate, gin.H{
		"pack_id": strID(packID),
		"status":  "removed",
	})
	c.Status(http.StatusNoContent)
}

// copyItem POST /users/@me/sticker-packs/{target_pack_id}/items/copy
func (h *api) copyItem(c *gin.Context) {
	targetPackID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	target, ok := h.loadOwnedPack(c, targetPackID, user.ID)
	if !ok {
		return
	}
	if target.Status != model.StickerPackActive {
		fail(c, http.StatusBadRequest, "PACK_NOT_ACTIVE", "目标包须为 active")
		return
	}
	var input copyItemRequest
	if !bind(c, &input) {
		return
	}
	sourceID, err := parseSnowflakeString(strings.TrimSpace(input.SourceItemID))
	if err != nil || sourceID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_SOURCE", "source_item_id 非法")
		return
	}

	var source model.StickerItem
	if err := h.db().First(&source, "id = ? AND status = ?", sourceID, model.StickerItemActive).Error; err != nil {
		notFound(c)
		return
	}
	var sourcePack model.StickerPack
	if err := h.db().First(&sourcePack, "id = ?", source.PackID).Error; err != nil {
		notFound(c)
		return
	}
	sourcePack = refreshSoftDeleteStatus(h.db(), sourcePack)
	if err := canCopy(h.db(), sourcePack); err != nil {
		fail(c, http.StatusForbidden, "CANNOT_COPY", err.Error())
		return
	}
	// L.4：目标 kind 相同
	if target.Kind != source.Kind || target.Kind != sourcePack.Kind {
		fail(c, http.StatusBadRequest, "KIND_MISMATCH", "目标包 kind 须与源条目一致")
		return
	}
	if h.countActiveItems(target.ID) >= h.maxItemsFor(user.ID) {
		fail(c, http.StatusBadRequest, "ITEM_LIMIT", "目标包条目数量已达上限")
		return
	}

	name := normalizeItemName(input.Name)
	if name == "" {
		name = normalizeItemName(source.Name)
	}
	if name == "" {
		name = source.Mark
	}

	var item model.StickerItem
	err = h.db().Transaction(func(tx *gorm.DB) error {
		// 共享 asset：ref_count++
		var asset model.StickerAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&asset, "id = ?", source.AssetID).Error; err != nil {
			return err
		}
		if err := tx.Model(&asset).Update("ref_count", gorm.Expr("ref_count + 1")).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		srcItemID := source.ID
		srcPackID := source.PackID
		item = model.StickerItem{
			ID:           h.ids.Next(),
			PackID:       target.ID,
			Kind:         target.Kind,
			Name:         name,
			ContentHash:  source.ContentHash,
			Mark:         source.Mark, // 同 hash → 同 mark（A1）
			AssetID:      source.AssetID,
			Width:        source.Width,
			Height:       source.Height,
			Animated:     source.Animated,
			SourceItemID: &srcItemID,
			SourcePackID: &srcPackID,
			SortOrder:    0,
			Status:       model.StickerItemActive,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		return tx.Create(&item).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "复制条目失败")
		return
	}
	if target.CoverAssetID == nil && target.CoverItemID == nil {
		_ = h.db().Model(&model.StickerPack{}).
			Where("id = ? AND cover_item_id IS NULL AND cover_asset_id IS NULL", target.ID).
			Update("cover_item_id", item.ID).Error
	}
	view := toItemView(item, assetURL(h.storageKeyOf(item.AssetID)))
	h.publishToUser(user.ID, eventbus.EventStickerItemCreate, view)
	c.JSON(http.StatusCreated, view)
}

// listAvailable GET /users/@me/sticker-available?guild_id=&kind=
func (h *api) listAvailable(c *gin.Context) {
	user := h.currentUser(c)
	var guildID uuid.UUID
	if raw := c.Query("guild_id"); raw != "" {
		gid, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_GUILD", "guild_id 非法")
			return
		}
		// 校验成员
		if _, err := perms.LoadGuild(h.db(), user, gid); err != nil {
			fail(c, http.StatusForbidden, "NOT_MEMBER", "非本服成员")
			return
		}
		guildID = gid
	}
	kindFilter := model.StickerKind(strings.ToLower(strings.TrimSpace(c.Query("kind"))))

	packIDs, err := listAvailablePackIDs(h.db(), user.ID, guildID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取可用集合失败")
		return
	}
	if len(packIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"packs": []any{}, "items": []any{}})
		return
	}
	var packs []model.StickerPack
	_ = h.db().Where("id IN ?", packIDs).Find(&packs).Error
	var items []model.StickerItem
	q := h.db().Where("pack_id IN ? AND status = ?", packIDs, model.StickerItemActive)
	if kindFilter == model.StickerKindEmote || kindFilter == model.StickerKindSticker {
		q = q.Where("kind = ?", kindFilter)
	}
	_ = q.Order("pack_id ASC, sort_order ASC, id ASC").Find(&items).Error

	assetIDs := make([]int64, 0, len(items))
	for _, it := range items {
		assetIDs = append(assetIDs, it.AssetID)
	}
	keys := h.loadAssetMap(assetIDs)

	packViews := make([]packView, 0, len(packs))
	for _, p := range packs {
		if kindFilter != "" && p.Kind != kindFilter {
			continue
		}
		packViews = append(packViews, h.packView(p, 0, nil))
	}
	itemViews := make([]itemView, 0, len(items))
	for _, it := range items {
		itemViews = append(itemViews, toItemView(it, assetURL(keys[it.AssetID])))
	}
	c.JSON(http.StatusOK, gin.H{"packs": packViews, "items": itemViews})
}

func ptrPack(v packView) *packView { return &v }
