package cosmetics

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// listShop GET /cosmetics/shop?category=&tag=&q=
func (h *api) listShop(c *gin.Context) {
	now := time.Now().UTC()
	user := h.currentUser(c)
	category := strings.TrimSpace(c.Query("category"))
	tagKey := strings.TrimSpace(c.Query("tag"))
	qtext := strings.TrimSpace(c.Query("q"))

	// 解析 tag
	var tagItemIDs []int64
	var tagBundleIDs []int64
	if tagKey != "" {
		var tag model.CosmeticTag
		if err := h.db().Where("key = ?", tagKey).First(&tag).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"items": []itemView{}, "bundles": []bundleView{}})
			return
		}
		_ = h.db().Model(&model.CosmeticItemTag{}).Where("tag_id = ?", tag.ID).Pluck("item_id", &tagItemIDs).Error
		_ = h.db().Model(&model.CosmeticBundleTag{}).Where("tag_id = ?", tag.ID).Pluck("bundle_id", &tagBundleIDs).Error
	}

	iq := h.db().Where("status = ?", model.CosmeticStatusPublished)
	if category != "" {
		iq = iq.Where("category_key = ?", category)
	}
	if tagKey != "" {
		if len(tagItemIDs) == 0 {
			iq = iq.Where("1 = 0")
		} else {
			iq = iq.Where("id IN ?", tagItemIDs)
		}
	}
	if qtext != "" {
		like := "%" + qtext + "%"
		iq = iq.Where("name ILIKE ? OR description ILIKE ?", like, like)
	}
	var items []model.CosmeticItem
	_ = iq.Order("sort_order ASC, created_at DESC").Limit(200).Find(&items).Error

	// 时间窗过滤
	filtered := make([]model.CosmeticItem, 0, len(items))
	for _, it := range items {
		if itemAvailableNow(it, now) {
			filtered = append(filtered, it)
		}
	}

	// 库存 owned
	ownedSet := map[int64]bool{}
	{
		var inv []model.UserCosmeticInventory
		_ = h.db().Where("user_id = ?", user.ID).Find(&inv).Error
		for _, row := range inv {
			if inventoryActive(row, now) {
				ownedSet[row.ItemID] = true
			}
		}
	}

	slots := h.categorySlotMap()
	assetIDs := []int64{}
	itemIDs := make([]int64, 0, len(filtered))
	for _, it := range filtered {
		itemIDs = append(itemIDs, it.ID)
		assetIDs = append(assetIDs, h.collectAssetIDsFromItem(it)...)
	}
	assets := h.loadAssetsByIDs(assetIDs)
	tagMap := h.tagsForItems(itemIDs)

	itemViews := make([]itemView, 0, len(filtered))
	for _, it := range filtered {
		v := h.itemView(it, assets, tagMap[it.ID], slots[it.CategoryKey])
		v.Status = ""
		owned := ownedSet[it.ID]
		v.Owned = &owned
		itemViews = append(itemViews, v)
	}

	// Bundles（无 category 过滤时展示；有 category 时仍可展示包含该品类的包）
	bq := h.db().Where("status = ?", model.CosmeticStatusPublished)
	if tagKey != "" {
		if len(tagBundleIDs) == 0 {
			bq = bq.Where("1 = 0")
		} else {
			bq = bq.Where("id IN ?", tagBundleIDs)
		}
	}
	if qtext != "" {
		like := "%" + qtext + "%"
		bq = bq.Where("name ILIKE ? OR description ILIKE ?", like, like)
	}
	var bundles []model.CosmeticBundle
	_ = bq.Order("sort_order ASC, created_at DESC").Limit(100).Find(&bundles).Error

	bundleViews := make([]bundleView, 0, len(bundles))
	for _, b := range bundles {
		if !bundleAvailableNow(b, now) {
			continue
		}
		// category 过滤：捆绑内至少有一件匹配
		if category != "" {
			var links []model.CosmeticBundleItem
			_ = h.db().Where("bundle_id = ?", b.ID).Find(&links).Error
			ids := make([]int64, 0, len(links))
			for _, l := range links {
				ids = append(ids, l.ItemID)
			}
			if len(ids) == 0 {
				continue
			}
			var cnt int64
			h.db().Model(&model.CosmeticItem{}).Where("id IN ? AND category_key = ?", ids, category).Count(&cnt)
			if cnt == 0 {
				continue
			}
		}
		bv := h.buildBundleView(b, false, false)
		// owned_all
		var links []model.CosmeticBundleItem
		_ = h.db().Where("bundle_id = ?", b.ID).Find(&links).Error
		allOwned := len(links) > 0
		for _, l := range links {
			if !ownedSet[l.ItemID] {
				allOwned = false
				break
			}
		}
		if len(links) == 0 {
			allOwned = false
		}
		bv.OwnedAll = &allOwned
		bundleViews = append(bundleViews, bv)
	}

	c.JSON(http.StatusOK, gin.H{"items": itemViews, "bundles": bundleViews})
}
