package sticker

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"gorm.io/gorm"
)

type createPackRequest struct {
	Name            string  `json:"name" binding:"required"`
	Description     string  `json:"description"`
	Kind            string  `json:"kind" binding:"required"`  // emote | sticker
	Scope           string  `json:"scope" binding:"required"` // account | guild
	GuildID         *string `json:"guild_id"`
	AllowBrowseFull *bool   `json:"allow_browse_full"`
}

type patchPackRequest struct {
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	AllowBrowseFull *bool   `json:"allow_browse_full"`
	// CoverItemID 指定包内条目为封面（会清除自定义上传封面）。
	CoverItemID *string `json:"cover_item_id"`
	// ClearCover 清除自定义上传封面与条目封面指定，回退为首条。
	ClearCover bool `json:"clear_cover"`
	// ClearCustomCover 仅清除上传封面，保留 cover_item_id。
	ClearCustomCover bool `json:"clear_custom_cover"`
}

// listMyPacks GET /users/@me/sticker-packs
func (h *api) listMyPacks(c *gin.Context) {
	user := h.currentUser(c)
	var packs []model.StickerPack
	if err := h.db().Where("owner_user_id = ? AND status <> ?", user.ID, model.StickerPackPurged).
		Order("created_at DESC").Find(&packs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取贴图包失败")
		return
	}
	views := make([]packView, 0, len(packs))
	for _, p := range packs {
		p = refreshSoftDeleteStatus(h.db(), p)
		count := h.countActiveItems(p.ID)
		views = append(views, h.packView(p, count, nil))
	}
	c.JSON(http.StatusOK, gin.H{"packs": views})
}

// createPack POST /users/@me/sticker-packs
func (h *api) createPack(c *gin.Context) {
	user := h.currentUser(c)
	var input createPackRequest
	if !bind(c, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > maxPackNameRunes {
		fail(c, http.StatusBadRequest, "INVALID_NAME", "包名不能为空且不超过 100 字符")
		return
	}
	kind := model.StickerKind(strings.ToLower(strings.TrimSpace(input.Kind)))
	if kind != model.StickerKindEmote && kind != model.StickerKindSticker {
		fail(c, http.StatusBadRequest, "INVALID_KIND", "kind 须为 emote 或 sticker")
		return
	}
	scope := model.StickerPackScope(strings.ToLower(strings.TrimSpace(input.Scope)))
	var guildID *uuid.UUID
	switch scope {
	case model.StickerScopeAccount:
		if input.GuildID != nil && strings.TrimSpace(*input.GuildID) != "" {
			fail(c, http.StatusBadRequest, "INVALID_SCOPE", "账号级包不可绑定 guild_id")
			return
		}
	case model.StickerScopeGuild:
		if input.GuildID == nil || strings.TrimSpace(*input.GuildID) == "" {
			fail(c, http.StatusBadRequest, "INVALID_SCOPE", "服独属包必须指定 guild_id")
			return
		}
		gid, err := uuid.Parse(strings.TrimSpace(*input.GuildID))
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_GUILD", "guild_id 非法")
			return
		}
		gctx, err := perms.LoadGuild(h.db(), user, gid)
		if err != nil {
			fail(c, http.StatusForbidden, "NOT_MEMBER", "须为本服成员才能创建服独属包")
			return
		}
		// 服独属包仅允许服务器所有者创建（归属权归属该服主）
		if !gctx.Owner {
			fail(c, http.StatusForbidden, "NOT_OWNER", "仅服务器所有者可为该服创建贴图包")
			return
		}
		guildID = &gid
	default:
		fail(c, http.StatusBadRequest, "INVALID_SCOPE", "scope 须为 account 或 guild")
		return
	}

	maxPacks := h.maxPacksFor(user.ID)
	var owned int64
	h.db().Model(&model.StickerPack{}).
		Where("owner_user_id = ? AND status NOT IN ?", user.ID,
			[]model.StickerPackStatus{model.StickerPackPurged}).
		Count(&owned)
	// 软删也占配额，防止刷包
	if int(owned) >= maxPacks {
		fail(c, http.StatusBadRequest, "PACK_LIMIT", "自建贴图包数量已达上限")
		return
	}

	allowBrowse := true
	if input.AllowBrowseFull != nil {
		allowBrowse = *input.AllowBrowseFull
	}
	now := time.Now().UTC()
	pack := model.StickerPack{
		ID:              h.ids.Next(),
		OwnerUserID:     user.ID,
		Scope:           scope,
		GuildID:         guildID,
		Kind:            kind,
		Name:            name,
		Description:     clampRunes(strings.TrimSpace(input.Description), maxPackDescRunes),
		AllowBrowseFull: allowBrowse,
		Status:          model.StickerPackActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := h.db().Create(&pack).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建贴图包失败")
		return
	}
	view := h.packView(pack, 0, nil)
	h.publishToUser(user.ID, eventbus.EventStickerPackCreate, view)
	if pack.Scope == model.StickerScopeGuild && pack.GuildID != nil {
		h.publishToGuild(*pack.GuildID, eventbus.EventStickerPackCreate, view)
	}
	c.JSON(http.StatusCreated, view)
}

// patchPack PATCH /users/@me/sticker-packs/{pack_id}
func (h *api) patchPack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	if pack.Status != model.StickerPackActive {
		fail(c, http.StatusBadRequest, "PACK_NOT_ACTIVE", "仅 active 包可编辑")
		return
	}
	var input patchPackRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || utf8.RuneCountInString(name) > maxPackNameRunes {
			fail(c, http.StatusBadRequest, "INVALID_NAME", "包名不能为空且不超过 100 字符")
			return
		}
		updates["name"] = name
	}
	if input.Description != nil {
		updates["description"] = clampRunes(strings.TrimSpace(*input.Description), maxPackDescRunes)
	}
	if input.AllowBrowseFull != nil {
		updates["allow_browse_full"] = *input.AllowBrowseFull
	}
	var releaseCoverAssetID *int64
	if input.ClearCover {
		if pack.CoverAssetID != nil {
			aid := *pack.CoverAssetID
			releaseCoverAssetID = &aid
		}
		updates["cover_item_id"] = nil
		updates["cover_asset_id"] = nil
	} else if input.ClearCustomCover {
		if pack.CoverAssetID != nil {
			aid := *pack.CoverAssetID
			releaseCoverAssetID = &aid
		}
		updates["cover_asset_id"] = nil
	} else if input.CoverItemID != nil {
		cid, err := parseSnowflakeString(strings.TrimSpace(*input.CoverItemID))
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_COVER", "cover_item_id 非法")
			return
		}
		var item model.StickerItem
		if err := h.db().First(&item, "id = ? AND pack_id = ? AND status = ?",
			cid, pack.ID, model.StickerItemActive).Error; err != nil {
			fail(c, http.StatusBadRequest, "INVALID_COVER", "封面条目不在本包内")
			return
		}
		// 指定包内条目为封面时，清除自定义上传封面以免优先级盖住
		if pack.CoverAssetID != nil {
			aid := *pack.CoverAssetID
			releaseCoverAssetID = &aid
		}
		updates["cover_item_id"] = cid
		updates["cover_asset_id"] = nil
	}
	if len(updates) <= 1 {
		fail(c, http.StatusBadRequest, "EMPTY_PATCH", "没有需要更新的字段")
		return
	}
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).Updates(updates).Error; err != nil {
			return err
		}
		if releaseCoverAssetID != nil {
			return h.releaseAsset(tx, *releaseCoverAssetID)
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新贴图包失败")
		return
	}
	_ = h.db().First(&pack, "id = ?", pack.ID)
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackUpdate, pack, view)
	c.JSON(http.StatusOK, view)
}

// uploadPackCover PUT /users/@me/sticker-packs/{pack_id}/cover
// 上传独立封面图（multipart file 或原始图片 body）。
func (h *api) uploadPackCover(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	if pack.Status != model.StickerPackActive {
		fail(c, http.StatusBadRequest, "PACK_NOT_ACTIVE", "仅 active 包可设置封面")
		return
	}
	data, mime, _, _, _, ok := h.readUpload(c)
	if !ok {
		return
	}
	oldCover := pack.CoverAssetID
	err := h.db().Transaction(func(tx *gorm.DB) error {
		asset, err := h.ensureAsset(tx, data, mime)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).Updates(map[string]any{
			"cover_asset_id": asset.ID,
			"updated_at":     time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		// 旧自定义封面引用释放（若与新资产不同）
		if oldCover != nil && *oldCover != asset.ID {
			return h.releaseAsset(tx, *oldCover)
		}
		return nil
	})
	if err != nil {
		switch {
		case errIs(err, errUnsupportedMIME):
			fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", err.Error())
		case errIs(err, errFileTooLarge):
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", err.Error())
		case errIs(err, errInvalidImage):
			fail(c, http.StatusBadRequest, "INVALID_IMAGE", err.Error())
		default:
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "上传封面失败")
		}
		return
	}
	_ = h.db().First(&pack, "id = ?", pack.ID)
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackUpdate, pack, view)
	c.JSON(http.StatusOK, view)
}

// deletePackCover DELETE /users/@me/sticker-packs/{pack_id}/cover
// 清除自定义上传封面，回退到 cover_item_id 或包内首条。
func (h *api) deletePackCover(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	if pack.CoverAssetID == nil {
		view := h.packView(pack, h.countActiveItems(pack.ID), nil)
		c.JSON(http.StatusOK, view)
		return
	}
	oldID := *pack.CoverAssetID
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).Updates(map[string]any{
			"cover_asset_id": nil,
			"updated_at":     time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		return h.releaseAsset(tx, oldID)
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "清除封面失败")
		return
	}
	_ = h.db().First(&pack, "id = ?", pack.ID)
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackUpdate, pack, view)
	c.JSON(http.StatusOK, view)
}

// softDeletePack DELETE /users/@me/sticker-packs/{pack_id}
func (h *api) softDeletePack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	if pack.Status != model.StickerPackActive {
		fail(c, http.StatusBadRequest, "PACK_NOT_ACTIVE", "仅 active 包可软删")
		return
	}
	now := time.Now().UTC()
	deadline := now.AddDate(0, 0, softDeleteRestoreDays)
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).Updates(map[string]any{
			"status":           model.StickerPackSoftDeleted,
			"soft_deleted_at":  now,
			"restore_deadline": deadline,
			"updated_at":       now,
		}).Error; err != nil {
			return err
		}
		// C.5：Install 行软隐藏
		return tx.Model(&model.UserPackLibrary{}).
			Where("pack_id = ? AND status = ?", pack.ID, model.UserPackLibraryActive).
			Update("status", model.UserPackLibraryHidden).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "软删失败")
		return
	}
	pack.Status = model.StickerPackSoftDeleted
	pack.SoftDeletedAt = &now
	pack.RestoreDeadline = &deadline
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackDelete, pack, view)
	h.notifyLibraryInstallers(pack.ID, "hidden")
	audit.Log(h.db(), audit.Entry{
		ActorID: &user.ID, ActorType: "user", Action: "sticker.pack.soft_delete",
		TargetType: "sticker_pack", TargetID: strID(pack.ID),
	})
	c.JSON(http.StatusOK, view)
}

// restorePack POST /users/@me/sticker-packs/{pack_id}/restore
func (h *api) restorePack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	pack = refreshSoftDeleteStatus(h.db(), pack)
	if pack.Status != model.StickerPackSoftDeleted {
		if pack.Status == model.StickerPackSoftDeletedExpired {
			fail(c, http.StatusBadRequest, "RESTORE_EXPIRED", "已超过 180 天恢复期限")
			return
		}
		fail(c, http.StatusBadRequest, "PACK_NOT_RESTORABLE", "当前状态不可恢复")
		return
	}
	now := time.Now().UTC()
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).Updates(map[string]any{
			"status":           model.StickerPackActive,
			"soft_deleted_at":  nil,
			"restore_deadline": nil,
			"updated_at":       now,
		}).Error; err != nil {
			return err
		}
		// C.6：Install 全部回 active
		return tx.Model(&model.UserPackLibrary{}).
			Where("pack_id = ? AND status = ?", pack.ID, model.UserPackLibraryHidden).
			Update("status", model.UserPackLibraryActive).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "恢复失败")
		return
	}
	pack.Status = model.StickerPackActive
	pack.SoftDeletedAt = nil
	pack.RestoreDeadline = nil
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackRestore, pack, view)
	h.notifyLibraryInstallers(pack.ID, "active")
	c.JSON(http.StatusOK, view)
}

// getPack GET /sticker-packs/{pack_id} 预览
func (h *api) getPack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return
	}
	pack = refreshSoftDeleteStatus(h.db(), pack)
	if pack.Status == model.StickerPackPurged {
		notFound(c)
		return
	}
	user := h.currentUser(c)
	isOwner := pack.OwnerUserID == user.ID

	// 上下文 guild：查询参数，用于服独属可见性判断
	var ctxGuild uuid.UUID
	if raw := c.Query("guild_id"); raw != "" {
		if gid, err := uuid.Parse(raw); err == nil {
			ctxGuild = gid
		}
	}

	// 服独属：非所有者须在本服
	if pack.Scope == model.StickerScopeGuild && !isOwner {
		if pack.GuildID == nil {
			notFound(c)
			return
		}
		if _, err := perms.LoadGuild(h.db(), user, *pack.GuildID); err != nil {
			notFound(c)
			return
		}
		ctxGuild = *pack.GuildID
	}

	// 全局 ban：非所有者仅可看元数据（历史消息场景用 getItem）；预览全量 items 拒绝
	showItems := false
	if isOwner {
		showItems = true
	} else if pack.Status == model.StickerPackActive && pack.AllowBrowseFull {
		if pack.Scope == model.StickerScopeGuild {
			showItems = true
		} else {
			showItems = true
		}
		if guildBanned(h.db(), ctxGuild, pack.ID) {
			// 仍可返回元数据，但不给 items / 入库入口
			showItems = false
		}
	}

	var items []itemView
	count := h.countActiveItems(pack.ID)
	if showItems {
		items = h.loadItemViews(pack.ID)
	} else if focus := c.Query("item_id"); focus != "" {
		// allow_browse_full=false：仅当前点开的一项
		if iid, err := parseSnowflakeString(focus); err == nil {
			var item model.StickerItem
			if h.db().First(&item, "id = ? AND pack_id = ? AND status = ?",
				iid, pack.ID, model.StickerItemActive).Error == nil {
				key := h.storageKeyOf(item.AssetID)
				items = []itemView{toItemView(item, assetURL(key))}
			}
		}
	}

	view := h.packView(pack, count, items)
	// 是否已在当前用户的贴图库（自建包视为已在库）
	alreadyInstalled := isOwner
	if !isOwner {
		var n int64
		_ = h.db().Model(&model.UserPackLibrary{}).
			Where("user_id = ? AND pack_id = ? AND status = ?",
				user.ID, pack.ID, model.UserPackLibraryActive).
			Count(&n).Error
		alreadyInstalled = n > 0
	}
	// 附带可操作标志，方便客户端 Preview UI
	// can_install：允许收藏 + 非本人 + 尚未 Install
	canInstallFlag := canInstall(h.db(), pack, ctxGuild) == nil && !isOwner && !alreadyInstalled
	canCopyFlag := canCopy(h.db(), pack) == nil && pack.Scope == model.StickerScopeAccount
	c.JSON(http.StatusOK, gin.H{
		"pack":              view,
		"can_install":       canInstallFlag,
		"can_copy":          canCopyFlag,
		"is_owner":          isOwner,
		"already_installed": alreadyInstalled,
	})
}

func (h *api) loadOwnedPack(c *gin.Context, packID int64, owner uuid.UUID) (model.StickerPack, bool) {
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return pack, false
	}
	if pack.OwnerUserID != owner {
		// 不可见
		notFound(c)
		return pack, false
	}
	if pack.Status == model.StickerPackPurged {
		notFound(c)
		return pack, false
	}
	return refreshSoftDeleteStatus(h.db(), pack), true
}

func (h *api) countActiveItems(packID int64) int {
	var n int64
	h.db().Model(&model.StickerItem{}).
		Where("pack_id = ? AND status = ?", packID, model.StickerItemActive).Count(&n)
	return int(n)
}

// resolveCover 解析封面 URL 与是否自定义上传。
// 优先级：cover_asset_id > cover_item_id（active）> 包内首条 active（sort_order, id）。
func (h *api) resolveCover(pack model.StickerPack) (url string, custom bool) {
	if pack.CoverAssetID != nil {
		if key := h.storageKeyOf(*pack.CoverAssetID); key != "" {
			return assetURL(key), true
		}
	}
	if pack.CoverItemID != nil {
		var item model.StickerItem
		if h.db().Select("asset_id").First(&item,
			"id = ? AND pack_id = ? AND status = ?",
			*pack.CoverItemID, pack.ID, model.StickerItemActive).Error == nil {
			if key := h.storageKeyOf(item.AssetID); key != "" {
				return assetURL(key), false
			}
		}
	}
	var first model.StickerItem
	if h.db().Select("asset_id").
		Where("pack_id = ? AND status = ?", pack.ID, model.StickerItemActive).
		Order("sort_order ASC, id ASC").First(&first).Error == nil {
		if key := h.storageKeyOf(first.AssetID); key != "" {
			return assetURL(key), false
		}
	}
	return "", false
}

// packView 组装带解析封面的包投影。
func (h *api) packView(p model.StickerPack, itemCount int, items []itemView) packView {
	url, custom := h.resolveCover(p)
	return toPackViewWithCover(p, itemCount, items, url, custom)
}

func (h *api) loadItemViews(packID int64) []itemView {
	var items []model.StickerItem
	_ = h.db().Where("pack_id = ? AND status = ?", packID, model.StickerItemActive).
		Order("sort_order ASC, id ASC").Find(&items).Error
	assetIDs := make([]int64, 0, len(items))
	for _, it := range items {
		assetIDs = append(assetIDs, it.AssetID)
	}
	keys := h.loadAssetMap(assetIDs)
	views := make([]itemView, 0, len(items))
	for _, it := range items {
		views = append(views, toItemView(it, assetURL(keys[it.AssetID])))
	}
	return views
}

func (h *api) maxPacksFor(userID uuid.UUID) int {
	var o model.StickerQuotaOverride
	if h.db().First(&o, "user_id = ?", userID).Error == nil && o.MaxPacks > 0 {
		return o.MaxPacks
	}
	return defaultMaxPacksPerUser
}

func (h *api) maxItemsFor(userID uuid.UUID) int {
	var o model.StickerQuotaOverride
	if h.db().First(&o, "user_id = ?", userID).Error == nil && o.MaxItemsPack > 0 {
		return o.MaxItemsPack
	}
	return defaultMaxItemsPerPack
}

func (h *api) publishToUser(userID uuid.UUID, eventType string, payload any) {
	if h.bus() == nil {
		return
	}
	h.bus().Publish(eventbus.Event{
		Type:    eventType,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

func (h *api) publishToGuild(guildID uuid.UUID, eventType string, payload any) {
	if h.bus() == nil {
		return
	}
	h.bus().Publish(eventbus.Event{
		Type:    eventType,
		GuildID: &guildID,
		Payload: payload,
	})
}

func (h *api) publishPackEvent(eventType string, pack model.StickerPack, view packView) {
	h.publishToUser(pack.OwnerUserID, eventType, view)
	if pack.Scope == model.StickerScopeGuild && pack.GuildID != nil {
		h.publishToGuild(*pack.GuildID, eventType, view)
	}
	// Install 引用方也需刷新：推 library 安装者
	var libs []model.UserPackLibrary
	_ = h.db().Where("pack_id = ?", pack.ID).Find(&libs).Error
	for _, lib := range libs {
		if lib.UserID == pack.OwnerUserID {
			continue
		}
		h.publishToUser(lib.UserID, eventType, view)
	}
}

func (h *api) notifyLibraryInstallers(packID int64, status string) {
	var libs []model.UserPackLibrary
	_ = h.db().Where("pack_id = ?", packID).Find(&libs).Error
	for _, lib := range libs {
		h.publishToUser(lib.UserID, eventbus.EventStickerLibraryUpdate, gin.H{
			"pack_id": strID(packID),
			"status":  status,
		})
	}
}
