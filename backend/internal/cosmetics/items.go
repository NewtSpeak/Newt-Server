package cosmetics

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type itemWriteRequest struct {
	CategoryKey    string          `json:"category_key"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Payload        json.RawMessage `json:"payload"`
	PricePoints    *int            `json:"price_points"`
	Status         string          `json:"status"`
	SortOrder      *int            `json:"sort_order"`
	TagIDs         []string        `json:"tag_ids"`
	AvailableFrom  *time.Time      `json:"available_from"`
	AvailableUntil *time.Time      `json:"available_until"`
}

// createItem POST /admin/cosmetics/items
func (h *api) createItem(c *gin.Context) {
	user := h.currentUser(c)
	var input itemWriteRequest
	if !bind(c, &input) {
		return
	}
	catKey := strings.TrimSpace(input.CategoryKey)
	var cat model.CosmeticCategory
	if err := h.db().First(&cat, "key = ?", catKey).Error; err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CATEGORY", "品类不存在")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "INVALID_NAME", "name 必填")
		return
	}
	status := model.CosmeticStatusDraft
	if s := strings.TrimSpace(input.Status); s != "" {
		status = model.CosmeticItemStatus(s)
		if status != model.CosmeticStatusDraft && status != model.CosmeticStatusPublished && status != model.CosmeticStatusArchived {
			fail(c, http.StatusBadRequest, "INVALID_STATUS", "status 非法")
			return
		}
	}
	price := 0
	if input.PricePoints != nil {
		if *input.PricePoints < 0 {
			fail(c, http.StatusBadRequest, "INVALID_PRICE", "price_points 不能为负")
			return
		}
		price = *input.PricePoints
	}
	sortOrder := 0
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	payload := normalizePayloadJSON(string(input.Payload))
	now := time.Now().UTC()
	item := model.CosmeticItem{
		ID: h.ids.Next(), CategoryKey: cat.Key, Name: clampRunes(name, maxNameRunes),
		Description: clampRunes(strings.TrimSpace(input.Description), maxDescRunes),
		AssetsJSON: "{}", PayloadJSON: payload, PricePoints: price, Status: status,
		SortOrder: sortOrder, AvailableFrom: input.AvailableFrom, AvailableUntil: input.AvailableUntil,
		CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if status == model.CosmeticStatusPublished {
		schema, _ := parseCategorySchema(cat.SchemaJSON)
		if err := validateItemAgainstSchema(schema, map[string]int64{}, nil); err != nil {
			// 发布时要求资产；创建时若直接 published 且无资产则降为 draft 或报错
			fail(c, http.StatusBadRequest, "INCOMPLETE_ASSETS", "发布前请上传必填资产："+err.Error())
			return
		}
	}
	if err := h.db().Create(&item).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建商品失败")
		return
	}
	if len(input.TagIDs) > 0 {
		_ = h.setItemTags(item.ID, parseTagIDList(input.TagIDs))
	}
	h.publishCatalogUpdate(gin.H{"op": "item_create", "item_id": strID(item.ID)})
	c.JSON(http.StatusCreated, h.buildItemView(item, cat.Slot, true))
}

// patchItem PATCH /admin/cosmetics/items/:itemID
func (h *api) patchItem(c *gin.Context) {
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	var item model.CosmeticItem
	if err := h.db().First(&item, "id = ?", itemID).Error; err != nil {
		notFound(c)
		return
	}
	var input itemWriteRequest
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
	if len(input.Payload) > 0 {
		updates["payload_json"] = normalizePayloadJSON(string(input.Payload))
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
	if input.AvailableFrom != nil {
		updates["available_from"] = *input.AvailableFrom
	}
	if input.AvailableUntil != nil {
		updates["available_until"] = *input.AvailableUntil
	}
	if s := strings.TrimSpace(input.Status); s != "" {
		st := model.CosmeticItemStatus(s)
		if st != model.CosmeticStatusDraft && st != model.CosmeticStatusPublished && st != model.CosmeticStatusArchived {
			fail(c, http.StatusBadRequest, "INVALID_STATUS", "status 非法")
			return
		}
		if st == model.CosmeticStatusPublished {
			var cat model.CosmeticCategory
			_ = h.db().First(&cat, "key = ?", item.CategoryKey)
			schema, _ := parseCategorySchema(cat.SchemaJSON)
			assets, _ := parseAssetsMap(item.AssetsJSON)
			if err := validateItemAgainstSchema(schema, assets, nil); err != nil {
				fail(c, http.StatusBadRequest, "INCOMPLETE_ASSETS", err.Error())
				return
			}
		}
		updates["status"] = st
	}
	if err := h.db().Model(&item).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新商品失败")
		return
	}
	if input.TagIDs != nil {
		_ = h.setItemTags(item.ID, parseTagIDList(input.TagIDs))
	}
	_ = h.db().First(&item, "id = ?", itemID)
	h.publishCatalogUpdate(gin.H{"op": "item_update", "item_id": strID(item.ID)})
	c.JSON(http.StatusOK, h.buildItemView(item, "", true))
}

// uploadItemAsset PUT /admin/cosmetics/items/:itemID/assets/:slot
func (h *api) uploadItemAsset(c *gin.Context) {
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	slotKey := strings.TrimSpace(c.Param("slot"))
	if slotKey == "" {
		fail(c, http.StatusBadRequest, "INVALID_SLOT", "资产槽不能为空")
		return
	}
	var item model.CosmeticItem
	if err := h.db().First(&item, "id = ?", itemID).Error; err != nil {
		notFound(c)
		return
	}
	var cat model.CosmeticCategory
	if err := h.db().First(&cat, "key = ?", item.CategoryKey).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "品类丢失")
		return
	}
	schema, _ := parseCategorySchema(cat.SchemaJSON)
	var slotDef *AssetSlotDef
	for i := range schema.AssetSlots {
		if schema.AssetSlots[i].Key == slotKey {
			slotDef = &schema.AssetSlots[i]
			break
		}
	}
	if slotDef == nil {
		fail(c, http.StatusBadRequest, "UNKNOWN_SLOT", "品类未定义该资产槽")
		return
	}
	maxBytes := maxBytesForSlot(*slotDef)
	data, err := readBodyLimited(c, maxBytes)
	if err != nil {
		if err.Error() == "FILE_TOO_LARGE" {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", "文件过大")
			return
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传失败")
		return
	}
	if len(data) == 0 {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "空文件")
		return
	}
	ct := c.GetHeader("Content-Type")
	// MIME 校验前置到引用计数变更之前：类型被拒时不产生任何资产引用（防引用泄漏）
	mime := sniffMediaMIME(data, ct)
	if _, ok := allowedMIME[mime]; !ok {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "不支持的媒体类型 "+mime)
		return
	}
	if !mimeBelongsGroup(mime, slotDef.MIMEGroups) {
		fail(c, http.StatusBadRequest, "MIME_NOT_ALLOWED", "该槽位不接受此媒体类型")
		return
	}
	// 事务化：锁 item 行防并发双上传交叉覆盖；新资产 +ref 与旧资产 -ref 原子完成
	txErr := h.db().Transaction(func(tx *gorm.DB) error {
		var locked model.CosmeticItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", itemID).Error; err != nil {
			return err
		}
		asset, err := h.storeAssetBytes(tx, data, ct, maxBytes)
		if err != nil {
			return err
		}
		assets, _ := parseAssetsMap(locked.AssetsJSON)
		oldID := assets[slotKey]
		// 释放被替换的旧引用；同内容重传（oldID == asset.ID）也释放一次，
		// 抵消 storeAssetBytes 的重复 +1，保证每个槽位净引用恒为 1
		if oldID != 0 {
			if err := h.releaseAsset(tx, oldID); err != nil {
				return err
			}
		}
		assets[slotKey] = asset.ID
		updates := map[string]any{
			"assets_json": encodeAssetsMap(assets),
			"updated_at":  time.Now().UTC(),
		}
		// preview 不持有独立引用：为空时设为本次资产；
		// 指向被替换旧资产且旧资产已退出全部槽位时改指新资产，避免悬空
		if locked.PreviewAssetID == nil {
			updates["preview_asset_id"] = asset.ID
		} else if oldID != 0 && *locked.PreviewAssetID == oldID {
			stillUsed := false
			for _, id := range assets {
				if id == oldID {
					stillUsed = true
					break
				}
			}
			if !stillUsed {
				updates["preview_asset_id"] = asset.ID
			}
		}
		return tx.Model(&locked).Updates(updates).Error
	})
	if txErr != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资产失败")
		return
	}
	_ = h.db().First(&item, "id = ?", itemID)
	c.JSON(http.StatusOK, h.buildItemView(item, cat.Slot, true))
}

// getItem GET /cosmetics/items/:itemID
func (h *api) getItem(c *gin.Context) {
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	admin := c.GetBool("cosmetics_admin")
	var item model.CosmeticItem
	if err := h.db().First(&item, "id = ?", itemID).Error; err != nil {
		notFound(c)
		return
	}
	if !admin && !itemAvailableNow(item, time.Now().UTC()) {
		// 已拥有仍可看详情
		user := h.currentUser(c)
		var cnt int64
		h.db().Model(&model.UserCosmeticInventory{}).
			Where("user_id = ? AND item_id = ?", user.ID, itemID).Count(&cnt)
		if cnt == 0 {
			notFound(c)
			return
		}
	}
	// includeStatus：管理端需要看到 draft/published；用户端隐藏状态字段。
	c.JSON(http.StatusOK, h.buildItemView(item, "", admin))
}

// adminListItems GET /admin/cosmetics/items
func (h *api) adminListItems(c *gin.Context) {
	q := h.db().Model(&model.CosmeticItem{})
	if cat := strings.TrimSpace(c.Query("category")); cat != "" {
		q = q.Where("category_key = ?", cat)
	}
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		q = q.Where("status = ?", st)
	}
	var items []model.CosmeticItem
	if err := q.Order("sort_order ASC, created_at DESC").Limit(500).Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取商品失败")
		return
	}
	views := make([]itemView, 0, len(items))
	for _, it := range items {
		views = append(views, h.buildItemView(it, "", true))
	}
	c.JSON(http.StatusOK, gin.H{"items": views})
}

func (h *api) buildItemView(item model.CosmeticItem, slot string, includeStatus bool) itemView {
	ids := h.collectAssetIDsFromItem(item)
	assets := h.loadAssetsByIDs(ids)
	if slot == "" {
		var cat model.CosmeticCategory
		if err := h.db().First(&cat, "key = ?", item.CategoryKey).Error; err == nil {
			slot = cat.Slot
		}
	}
	tags := h.tagsForItems([]int64{item.ID})[item.ID]
	v := h.itemView(item, assets, tags, slot)
	if !includeStatus {
		v.Status = ""
	}
	return v
}

// categorySlotMap 批量查 slot
func (h *api) categorySlotMap() map[string]string {
	var cats []model.CosmeticCategory
	_ = h.db().Find(&cats).Error
	m := make(map[string]string, len(cats))
	for _, c := range cats {
		m[c.Key] = c.Slot
	}
	return m
}

func (h *api) categorySchemaMap() map[string]CategorySchema {
	var cats []model.CosmeticCategory
	_ = h.db().Find(&cats).Error
	m := make(map[string]CategorySchema, len(cats))
	for _, c := range cats {
		s, _ := parseCategorySchema(c.SchemaJSON)
		m[c.Key] = s
	}
	return m
}
