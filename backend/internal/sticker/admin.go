package sticker

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

func (h *api) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.currentUser(c).SystemAdmin {
			fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可执行平台治理操作")
			c.Abort()
		}
	}
}

// adminListPacks GET /admin/sticker-packs?status=&q=
func (h *api) adminListPacks(c *gin.Context) {
	q := h.db().Model(&model.StickerPack{}).Order("created_at DESC").Limit(100)
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if search := strings.TrimSpace(c.Query("q")); search != "" {
		q = q.Where("name ILIKE ?", "%"+search+"%")
	}
	var packs []model.StickerPack
	if err := q.Find(&packs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取失败")
		return
	}
	views := make([]packView, 0, len(packs))
	for _, p := range packs {
		views = append(views, h.packView(p, h.countActiveItems(p.ID), nil))
	}
	c.JSON(http.StatusOK, gin.H{"packs": views})
}

type adminBanRequest struct {
	Reason string `json:"reason"`
}

// adminGlobalBan POST /admin/sticker-packs/{pack_id}/global-ban
func (h *api) adminGlobalBan(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	admin := h.currentUser(c)
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return
	}
	if pack.Status == model.StickerPackPurged {
		fail(c, http.StatusBadRequest, "ALREADY_PURGED", "包已硬删")
		return
	}
	var input adminBanRequest
	_ = c.ShouldBindJSON(&input)
	now := time.Now().UTC()
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&pack).Updates(map[string]any{
			"status":     model.StickerPackGloballyBanned,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		// 可选：强制 library 隐藏
		return tx.Model(&model.UserPackLibrary{}).
			Where("pack_id = ?", pack.ID).
			Update("status", model.UserPackLibraryHidden).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "全局 ban 失败")
		return
	}
	pack.Status = model.StickerPackGloballyBanned
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackUpdate, pack, view)
	h.notifyLibraryInstallers(pack.ID, "hidden")
	audit.Log(h.db(), audit.Entry{
		ActorID: &admin.ID, ActorType: "system_admin",
		Action: "sticker.pack.global_ban", TargetType: "sticker_pack", TargetID: strID(pack.ID),
		Detail: map[string]any{"reason": input.Reason},
	})
	c.JSON(http.StatusOK, view)
}

// adminGlobalUnban DELETE /admin/sticker-packs/{pack_id}/global-ban
func (h *api) adminGlobalUnban(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	admin := h.currentUser(c)
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return
	}
	if pack.Status != model.StickerPackGloballyBanned {
		fail(c, http.StatusBadRequest, "NOT_BANNED", "包未处于全局 ban 状态")
		return
	}
	// 若仍在软删期，解 ban 应回到 soft_deleted 而非 active
	next := model.StickerPackActive
	if pack.SoftDeletedAt != nil {
		if pack.RestoreDeadline != nil && time.Now().UTC().After(*pack.RestoreDeadline) {
			next = model.StickerPackSoftDeletedExpired
		} else {
			next = model.StickerPackSoftDeleted
		}
	}
	now := time.Now().UTC()
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&pack).Updates(map[string]any{
			"status":     next,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		if next == model.StickerPackActive {
			return tx.Model(&model.UserPackLibrary{}).
				Where("pack_id = ? AND status = ?", pack.ID, model.UserPackLibraryHidden).
				Update("status", model.UserPackLibraryActive).Error
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解 ban 失败")
		return
	}
	pack.Status = next
	view := h.packView(pack, h.countActiveItems(pack.ID), nil)
	h.publishPackEvent(eventbus.EventStickerPackUpdate, pack, view)
	if next == model.StickerPackActive {
		h.notifyLibraryInstallers(pack.ID, "active")
	}
	audit.Log(h.db(), audit.Entry{
		ActorID: &admin.ID, ActorType: "system_admin",
		Action: "sticker.pack.global_unban", TargetType: "sticker_pack", TargetID: strID(pack.ID),
	})
	c.JSON(http.StatusOK, view)
}

// adminPurgePack DELETE /admin/sticker-packs/{pack_id}
func (h *api) adminPurgePack(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	admin := h.currentUser(c)
	var pack model.StickerPack
	if err := h.db().First(&pack, "id = ?", packID).Error; err != nil {
		notFound(c)
		return
	}
	if pack.Status == model.StickerPackPurged {
		c.Status(http.StatusNoContent)
		return
	}
	now := time.Now().UTC()
	err := h.db().Transaction(func(tx *gorm.DB) error {
		// 所有 active items → purged + release assets
		var items []model.StickerItem
		if err := tx.Where("pack_id = ? AND status = ?", pack.ID, model.StickerItemActive).Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			if err := tx.Model(&item).Updates(map[string]any{
				"status": model.StickerItemPurged, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := h.releaseAsset(tx, item.AssetID); err != nil {
				return err
			}
		}
		if pack.CoverAssetID != nil {
			if err := h.releaseAsset(tx, *pack.CoverAssetID); err != nil {
				return err
			}
		}
		if err := tx.Model(&pack).Updates(map[string]any{
			"status":         model.StickerPackPurged,
			"cover_asset_id": nil,
			"updated_at":     now,
		}).Error; err != nil {
			return err
		}
		return tx.Where("pack_id = ?", pack.ID).Delete(&model.UserPackLibrary{}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "硬删失败")
		return
	}
	pack.Status = model.StickerPackPurged
	pack.CoverAssetID = nil
	view := h.packView(pack, 0, nil)
	h.publishPackEvent(eventbus.EventStickerPackDelete, pack, view)
	audit.Log(h.db(), audit.Entry{
		ActorID: &admin.ID, ActorType: "system_admin",
		Action: "sticker.pack.purge", TargetType: "sticker_pack", TargetID: strID(pack.ID),
	})
	c.Status(http.StatusNoContent)
}

// adminPurgeItem DELETE /admin/sticker-packs/{pack_id}/items/{item_id}
func (h *api) adminPurgeItem(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	admin := h.currentUser(c)
	var item model.StickerItem
	if err := h.db().First(&item, "id = ? AND pack_id = ?", itemID, packID).Error; err != nil {
		notFound(c)
		return
	}
	if item.Status == model.StickerItemPurged {
		c.Status(http.StatusNoContent)
		return
	}
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&item).Updates(map[string]any{
			"status": model.StickerItemPurged, "updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		return h.releaseAsset(tx, item.AssetID)
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "硬删条目失败")
		return
	}
	view := toItemView(item, "")
	view.Status = model.StickerItemPurged
	h.publishToUser(admin.ID, eventbus.EventStickerItemDelete, view)
	// 通知包所有者与 installers
	var pack model.StickerPack
	if h.db().First(&pack, "id = ?", packID).Error == nil {
		h.publishToUser(pack.OwnerUserID, eventbus.EventStickerItemDelete, view)
		var libs []model.UserPackLibrary
		_ = h.db().Where("pack_id = ?", packID).Find(&libs).Error
		for _, lib := range libs {
			h.publishToUser(lib.UserID, eventbus.EventStickerItemDelete, view)
		}
	}
	audit.Log(h.db(), audit.Entry{
		ActorID: &admin.ID, ActorType: "system_admin",
		Action: "sticker.item.purge", TargetType: "sticker_item", TargetID: strID(item.ID),
		Detail: map[string]any{"pack_id": strID(packID)},
	})
	c.Status(http.StatusNoContent)
}

type quotaRequest struct {
	MaxPacks     int `json:"max_packs"`
	MaxItemsPack int `json:"max_items_per_pack"`
}

// adminGetQuota GET /admin/sticker-quotas/{user_id}
func (h *api) adminGetQuota(c *gin.Context) {
	userID, ok := parseUUIDParam(c, "userID")
	if !ok {
		return
	}
	var o model.StickerQuotaOverride
	err := h.db().First(&o, "user_id = ?", userID).Error
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"user_id":             userID,
			"max_packs":           defaultMaxPacksPerUser,
			"max_items_per_pack":  defaultMaxItemsPerPack,
			"override":            false,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user_id":            o.UserID,
		"max_packs":          effective(o.MaxPacks, defaultMaxPacksPerUser),
		"max_items_per_pack": effective(o.MaxItemsPack, defaultMaxItemsPerPack),
		"override":           true,
		"updated_at":         o.UpdatedAt,
		"updated_by":         o.UpdatedBy,
	})
}

// adminPutQuota PUT /admin/sticker-quotas/{user_id}
func (h *api) adminPutQuota(c *gin.Context) {
	userID, ok := parseUUIDParam(c, "userID")
	if !ok {
		return
	}
	admin := h.currentUser(c)
	var input quotaRequest
	if !bind(c, &input) {
		return
	}
	if input.MaxPacks < 0 || input.MaxItemsPack < 0 {
		fail(c, http.StatusBadRequest, "INVALID_QUOTA", "配额不能为负")
		return
	}
	// 0 表示清除覆盖、回默认：删除行
	if input.MaxPacks == 0 && input.MaxItemsPack == 0 {
		_ = h.db().Where("user_id = ?", userID).Delete(&model.StickerQuotaOverride{})
		audit.Log(h.db(), audit.Entry{
			ActorID: &admin.ID, ActorType: "system_admin",
			Action: "sticker.quota.clear", TargetType: "user", TargetID: userID.String(),
		})
		c.JSON(http.StatusOK, gin.H{
			"user_id": userID, "max_packs": defaultMaxPacksPerUser,
			"max_items_per_pack": defaultMaxItemsPerPack, "override": false,
		})
		return
	}
	o := model.StickerQuotaOverride{
		UserID:       userID,
		MaxPacks:     input.MaxPacks,
		MaxItemsPack: input.MaxItemsPack,
		UpdatedAt:    time.Now().UTC(),
		UpdatedBy:    admin.ID,
	}
	err := h.db().Save(&o).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存配额失败")
		return
	}
	audit.Log(h.db(), audit.Entry{
		ActorID: &admin.ID, ActorType: "system_admin",
		Action: "sticker.quota.set", TargetType: "user", TargetID: userID.String(),
		Detail: map[string]any{"max_packs": input.MaxPacks, "max_items_per_pack": input.MaxItemsPack},
	})
	c.JSON(http.StatusOK, gin.H{
		"user_id": userID, "max_packs": effective(o.MaxPacks, defaultMaxPacksPerUser),
		"max_items_per_pack": effective(o.MaxItemsPack, defaultMaxItemsPerPack),
		"override": true, "updated_at": o.UpdatedAt, "updated_by": o.UpdatedBy,
	})
}

func effective(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// 保证 uuid 包在配额 API 中可用（userID 已用）。
var _ = uuid.Nil