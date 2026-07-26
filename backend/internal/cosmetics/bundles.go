package cosmetics

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

type bundleWriteRequest struct {
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	PricePoints    *int       `json:"price_points"`
	Status         string     `json:"status"`
	SortOrder      *int       `json:"sort_order"`
	ItemIDs        []string   `json:"item_ids"`
	TagIDs         []string   `json:"tag_ids"`
	AvailableFrom  *time.Time `json:"available_from"`
	AvailableUntil *time.Time `json:"available_until"`
}

// createBundle POST /admin/cosmetics/bundles
func (h *api) createBundle(c *gin.Context) {
	user := h.currentUser(c)
	var input bundleWriteRequest
	if !bind(c, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "INVALID_NAME", "name 必填")
		return
	}
	price := 0
	if input.PricePoints != nil {
		if *input.PricePoints < 0 {
			fail(c, http.StatusBadRequest, "INVALID_PRICE", "price_points 不能为负")
			return
		}
		price = *input.PricePoints
	}
	status := model.CosmeticStatusDraft
	if s := strings.TrimSpace(input.Status); s != "" {
		status = model.CosmeticItemStatus(s)
	}
	sortOrder := 0
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	now := time.Now().UTC()
	b := model.CosmeticBundle{
		ID: h.ids.Next(), Name: clampRunes(name, maxNameRunes),
		Description: clampRunes(strings.TrimSpace(input.Description), maxDescRunes),
		PricePoints: price, Status: status, SortOrder: sortOrder,
		AvailableFrom: input.AvailableFrom, AvailableUntil: input.AvailableUntil,
		CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.db().Create(&b).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建捆绑失败")
		return
	}
	if len(input.ItemIDs) > 0 {
		if err := h.setBundleItems(b.ID, parseTagIDList(input.ItemIDs)); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_ITEMS", err.Error())
			return
		}
	}
	if len(input.TagIDs) > 0 {
		_ = h.setBundleTags(b.ID, parseTagIDList(input.TagIDs))
	}
	h.publishCatalogUpdate(gin.H{"op": "bundle_create", "bundle_id": strID(b.ID)})
	c.JSON(http.StatusCreated, h.buildBundleView(b, true, true))
}

// patchBundle PATCH /admin/cosmetics/bundles/:bundleID
func (h *api) patchBundle(c *gin.Context) {
	bundleID, ok := parseSnowflakeParam(c, "bundleID")
	if !ok {
		return
	}
	var b model.CosmeticBundle
	if err := h.db().First(&b, "id = ?", bundleID).Error; err != nil {
		notFound(c)
		return
	}
	var input bundleWriteRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if s := strings.TrimSpace(input.Name); s != "" {
		updates["name"] = clampRunes(s, maxNameRunes)
	}
	if input.Description != "" || input.Name != "" {
		updates["description"] = clampRunes(strings.TrimSpace(input.Description), maxDescRunes)
	}
	if input.PricePoints != nil {
		if *input.PricePoints < 0 {
			fail(c, http.StatusBadRequest, "INVALID_PRICE", "price_points 不能为负")
			return
		}
		updates["price_points"] = *input.PricePoints
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}
	if s := strings.TrimSpace(input.Status); s != "" {
		updates["status"] = model.CosmeticItemStatus(s)
	}
	if input.AvailableFrom != nil {
		updates["available_from"] = *input.AvailableFrom
	}
	if input.AvailableUntil != nil {
		updates["available_until"] = *input.AvailableUntil
	}
	if err := h.db().Model(&b).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新捆绑失败")
		return
	}
	if input.ItemIDs != nil {
		if err := h.setBundleItems(b.ID, parseTagIDList(input.ItemIDs)); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_ITEMS", err.Error())
			return
		}
	}
	if input.TagIDs != nil {
		_ = h.setBundleTags(b.ID, parseTagIDList(input.TagIDs))
	}
	_ = h.db().First(&b, "id = ?", bundleID)
	h.publishCatalogUpdate(gin.H{"op": "bundle_update", "bundle_id": strID(b.ID)})
	c.JSON(http.StatusOK, h.buildBundleView(b, true, true))
}

func (h *api) setBundleItems(bundleID int64, itemIDs []int64) error {
	_ = h.db().Where("bundle_id = ?", bundleID).Delete(&model.CosmeticBundleItem{}).Error
	for _, iid := range itemIDs {
		var item model.CosmeticItem
		if err := h.db().First(&item, "id = ?", iid).Error; err != nil {
			return err
		}
		if err := h.db().Create(&model.CosmeticBundleItem{BundleID: bundleID, ItemID: iid}).Error; err != nil {
			return err
		}
	}
	return nil
}

// getBundle GET /cosmetics/bundles/:bundleID
func (h *api) getBundle(c *gin.Context) {
	bundleID, ok := parseSnowflakeParam(c, "bundleID")
	if !ok {
		return
	}
	admin := c.GetBool("cosmetics_admin")
	var b model.CosmeticBundle
	if err := h.db().First(&b, "id = ?", bundleID).Error; err != nil {
		notFound(c)
		return
	}
	if !admin && !bundleAvailableNow(b, time.Now().UTC()) {
		notFound(c)
		return
	}
	c.JSON(http.StatusOK, h.buildBundleView(b, true, admin))
}

// adminListBundles GET /admin/cosmetics/bundles
func (h *api) adminListBundles(c *gin.Context) {
	var rows []model.CosmeticBundle
	if err := h.db().Order("sort_order ASC, created_at DESC").Limit(200).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取捆绑失败")
		return
	}
	views := make([]bundleView, 0, len(rows))
	for _, b := range rows {
		views = append(views, h.buildBundleView(b, false, true))
	}
	c.JSON(http.StatusOK, gin.H{"bundles": views})
}

func (h *api) buildBundleView(b model.CosmeticBundle, withItems, includeStatus bool) bundleView {
	var links []model.CosmeticBundleItem
	_ = h.db().Where("bundle_id = ?", b.ID).Find(&links).Error
	itemIDs := make([]int64, 0, len(links))
	idStrs := make([]string, 0, len(links))
	for _, l := range links {
		itemIDs = append(itemIDs, l.ItemID)
		idStrs = append(idStrs, strID(l.ItemID))
	}
	tags := h.tagsForBundles([]int64{b.ID})[b.ID]
	if tags == nil {
		tags = []tagView{}
	}
	preview := ""
	if b.PreviewAssetID != nil {
		assets := h.loadAssetsByIDs([]int64{*b.PreviewAssetID})
		if a, ok := assets[*b.PreviewAssetID]; ok {
			preview = assetPublicURL(a)
		}
	}
	v := bundleView{
		ID: strID(b.ID), Name: b.Name, Description: b.Description, PreviewURL: preview,
		PricePoints: b.PricePoints, SortOrder: b.SortOrder, Tags: tags, ItemIDs: idStrs,
		AvailableFrom: b.AvailableFrom, AvailableUntil: b.AvailableUntil,
	}
	if includeStatus {
		v.Status = string(b.Status)
	}
	if withItems && len(itemIDs) > 0 {
		var items []model.CosmeticItem
		_ = h.db().Where("id IN ?", itemIDs).Find(&items).Error
		slots := h.categorySlotMap()
		assetIDSet := []int64{}
		for _, it := range items {
			assetIDSet = append(assetIDSet, h.collectAssetIDsFromItem(it)...)
		}
		assets := h.loadAssetsByIDs(assetIDSet)
		tagMap := h.tagsForItems(itemIDs)
		for _, it := range items {
			iv := h.itemView(it, assets, tagMap[it.ID], slots[it.CategoryKey])
			if !includeStatus {
				iv.Status = ""
			}
			v.Items = append(v.Items, iv)
			if preview == "" && iv.PreviewURL != "" {
				preview = iv.PreviewURL
				v.PreviewURL = preview
			}
		}
	}
	return v
}
