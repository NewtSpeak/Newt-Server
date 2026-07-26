package cosmetics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inventoryEntryView struct {
	ID         string     `json:"id"`
	ItemID     string     `json:"item_id"`
	Source     string     `json:"source"`
	SourceRef  string     `json:"source_ref,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	AcquiredAt time.Time  `json:"acquired_at"`
	Item       *itemView  `json:"item,omitempty"`
}

// listInventory GET /users/@me/cosmetics/inventory
func (h *api) listInventory(c *gin.Context) {
	user := h.currentUser(c)
	now := time.Now().UTC()
	var rows []model.UserCosmeticInventory
	if err := h.db().Where("user_id = ?", user.ID).Order("acquired_at DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取库存失败")
		return
	}
	itemIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		if inventoryActive(r, now) {
			itemIDs = append(itemIDs, r.ItemID)
		}
	}
	itemMap := map[int64]model.CosmeticItem{}
	if len(itemIDs) > 0 {
		var items []model.CosmeticItem
		_ = h.db().Where("id IN ?", itemIDs).Find(&items).Error
		for _, it := range items {
			itemMap[it.ID] = it
		}
	}
	slots := h.categorySlotMap()
	assetIDs := []int64{}
	for _, it := range itemMap {
		assetIDs = append(assetIDs, h.collectAssetIDsFromItem(it)...)
	}
	assets := h.loadAssetsByIDs(assetIDs)
	tagMap := h.tagsForItems(itemIDs)

	entries := make([]inventoryEntryView, 0, len(rows))
	for _, r := range rows {
		if !inventoryActive(r, now) {
			continue
		}
		e := inventoryEntryView{
			ID: strID(r.ID), ItemID: strID(r.ItemID), Source: string(r.Source),
			SourceRef: r.SourceRef, ExpiresAt: r.ExpiresAt, AcquiredAt: r.AcquiredAt,
		}
		if it, ok := itemMap[r.ItemID]; ok {
			v := h.itemView(it, assets, tagMap[it.ID], slots[it.CategoryKey])
			v.Status = ""
			owned := true
			v.Owned = &owned
			e.Item = &v
		}
		entries = append(entries, e)
	}
	c.JSON(http.StatusOK, gin.H{"inventory": entries})
}

// grantItem 幂等授予库存（事务内可用）。
func (h *api) grantItem(tx *gorm.DB, userID uuid.UUID, itemID int64, source model.CosmeticInventorySource, sourceRef string, expiresAt *time.Time) (created bool, err error) {
	now := time.Now().UTC()
	var existing model.UserCosmeticInventory
	err = tx.Where("user_id = ? AND item_id = ?", userID, itemID).First(&existing).Error
	if err == nil {
		// 已有：新授予为永久则清除过期时间（过期行重购必须复活，否则扣了积分库存仍过期）；
		// 新 expires 更晚则续期；永久行不被限时授予降级。
		if expiresAt == nil {
			if existing.ExpiresAt != nil {
				if err := tx.Model(&existing).Update("expires_at", nil).Error; err != nil {
					return false, err
				}
			}
		} else if existing.ExpiresAt != nil && expiresAt.After(*existing.ExpiresAt) {
			if err := tx.Model(&existing).Update("expires_at", expiresAt).Error; err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if err != gorm.ErrRecordNotFound {
		return false, err
	}
	row := model.UserCosmeticInventory{
		ID: h.ids.Next(), UserID: userID, ItemID: itemID,
		Source: source, SourceRef: sourceRef, ExpiresAt: expiresAt, AcquiredAt: now,
	}
	if err := tx.Create(&row).Error; err != nil {
		return false, err
	}
	return true, nil
}

// adminGrant POST /admin/cosmetics/grant
func (h *api) adminGrant(c *gin.Context) {
	var input struct {
		UserID    string     `json:"user_id" binding:"required"`
		ItemID    string     `json:"item_id" binding:"required"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !bind(c, &input) {
		return
	}
	userID, err := uuid.Parse(input.UserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_USER", "user_id 非法")
		return
	}
	itemID, err := parseSnowflakeString(input.ItemID)
	if err != nil || itemID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_ITEM", "item_id 非法")
		return
	}
	var item model.CosmeticItem
	if err := h.db().First(&item, "id = ?", itemID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "商品不存在")
		return
	}
	var created bool
	err = h.db().Transaction(func(tx *gorm.DB) error {
		var e error
		created, e = h.grantItem(tx, userID, itemID, model.CosmeticSourceAdminGrant, "admin", input.ExpiresAt)
		return e
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "发放失败")
		return
	}
	h.publishToUser(userID, eventbus.EventCosmeticInventoryUpdate, gin.H{
		"item_id": strID(itemID), "created": created, "source": "admin_grant",
	})
	c.JSON(http.StatusOK, gin.H{"ok": true, "created": created, "item_id": strID(itemID)})
}

func (h *api) userOwnsItem(userID uuid.UUID, itemID int64, now time.Time) bool {
	var inv model.UserCosmeticInventory
	if err := h.db().Where("user_id = ? AND item_id = ?", userID, itemID).First(&inv).Error; err != nil {
		return false
	}
	return inventoryActive(inv, now)
}

// ensurePointsRow 确保积分行存在（FOR UPDATE 外先调用）。
func (h *api) ensurePointsRow(tx *gorm.DB, userID uuid.UUID) error {
	row := model.UserPoints{UserID: userID, Balance: 0, UpdatedAt: time.Now().UTC()}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func (h *api) adjustPoints(tx *gorm.DB, userID uuid.UUID, delta int64, reason, refType, refID string) (int64, error) {
	return adjustPointsIn(tx, userID, delta, reason, refType, refID)
}

// adjustPointsIn 包级积分调整（行锁 + 流水，事务内使用）；ledger ID 用包级雪花单例。
func adjustPointsIn(tx *gorm.DB, userID uuid.UUID, delta int64, reason, refType, refID string) (int64, error) {
	row := model.UserPoints{UserID: userID, Balance: 0, UpdatedAt: time.Now().UTC()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return 0, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "user_id = ?", userID).Error; err != nil {
		return 0, err
	}
	next := row.Balance + delta
	if next < 0 {
		return 0, fmt.Errorf("INSUFFICIENT_POINTS")
	}
	if err := tx.Model(&row).Updates(map[string]any{
		"balance": next, "updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return 0, err
	}
	ledger := model.UserPointsLedger{
		ID: cosmeticsIDs.Next(), UserID: userID, Delta: delta, BalanceAfter: next,
		Reason: reason, RefType: refType, RefID: refID, CreatedAt: time.Now().UTC(),
	}
	if err := tx.Create(&ledger).Error; err != nil {
		return 0, err
	}
	return next, nil
}
