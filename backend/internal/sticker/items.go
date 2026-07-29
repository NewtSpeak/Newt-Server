package sticker

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

type patchItemRequest struct {
	Name      *string `json:"name"`
	SortOrder *int    `json:"sort_order"`
}

// uploadItem POST /users/@me/sticker-packs/{pack_id}/items
// Content-Type: multipart/form-data，字段 file + 可选 name / sort_order
// 或原始 body + Content-Type 图片 MIME，query name=
func (h *api) uploadItem(c *gin.Context) {
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
		fail(c, http.StatusBadRequest, "PACK_NOT_ACTIVE", "仅 active 包可添加条目")
		return
	}

	maxItems := h.maxItemsFor(user.ID)
	if h.countActiveItems(pack.ID) >= maxItems {
		fail(c, http.StatusBadRequest, "ITEM_LIMIT", "包内条目数量已达上限")
		return
	}

	data, mime, name, sortOrder, sortOrderSet, ok := h.readUpload(c)
	if !ok {
		return
	}

	var item model.StickerItem
	err := h.db().Transaction(func(tx *gorm.DB) error {
		asset, err := h.ensureAsset(tx, data, mime)
		if err != nil {
			return err
		}
		mark := markFromHash(asset.ContentHash)
		displayName := normalizeItemName(name)
		if displayName == "" {
			// 未命名时默认用 mark，保证选择器始终有展示名
			displayName = mark
		}
		order := sortOrder
		if !sortOrderSet {
			order = nextItemSortOrder(tx, pack.ID)
		}
		now := time.Now().UTC()
		item = model.StickerItem{
			ID:          h.ids.Next(),
			PackID:      pack.ID,
			Kind:        pack.Kind, // 强制与包一致
			Name:        displayName,
			ContentHash: asset.ContentHash,
			Mark:        mark,
			AssetID:     asset.ID,
			Width:       asset.Width,
			Height:      asset.Height,
			Animated:    asset.Animated,
			SortOrder:   order,
			Status:      model.StickerItemActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		return tx.Create(&item).Error
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
			log.Printf("sticker uploadItem pack=%d: %v", pack.ID, err)
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "上传条目失败")
		}
		return
	}

	// 无自定义上传封面且未指定条目封面时，记录首条为 cover_item（解析时仍可回退首条）
	if pack.CoverAssetID == nil && pack.CoverItemID == nil {
		_ = h.db().Model(&model.StickerPack{}).
			Where("id = ? AND cover_item_id IS NULL AND cover_asset_id IS NULL", pack.ID).
			Update("cover_item_id", item.ID).Error
		pack.CoverItemID = &item.ID
	}

	view := toItemView(item, assetURL(h.storageKeyOf(item.AssetID)))
	h.publishToUser(user.ID, eventbus.EventStickerItemCreate, view)
	h.publishPackEvent(eventbus.EventStickerItemCreate, pack, h.packView(pack, h.countActiveItems(pack.ID), nil))
	// 再推一次 item 给 installers
	var libs []model.UserPackLibrary
	_ = h.db().Where("pack_id = ? AND status = ?", pack.ID, model.UserPackLibraryActive).Find(&libs).Error
	for _, lib := range libs {
		h.publishToUser(lib.UserID, eventbus.EventStickerItemCreate, view)
	}
	c.JSON(http.StatusCreated, view)
}

func (h *api) readUpload(c *gin.Context) (data []byte, mime, name string, sortOrder int, sortOrderSet, ok bool) {
	name = normalizeItemName(c.Query("name"))
	if name == "" {
		name = normalizeItemName(c.PostForm("name"))
	}
	if utf8.RuneCountInString(name) > maxItemNameRunes {
		fail(c, http.StatusBadRequest, "INVALID_NAME", "展示名不超过 100 字符")
		return nil, "", "", 0, false, false
	}
	if raw := c.PostForm("sort_order"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			sortOrder = n
			sortOrderSet = true
		}
	}
	if raw := c.Query("sort_order"); raw != "" && !sortOrderSet {
		if n, err := strconv.Atoi(raw); err == nil {
			sortOrder = n
			sortOrderSet = true
		}
	}

	maxBytes := h.maxFileBytes() // ≤0 不限制

	ct := normalizeMIME(c.GetHeader("Content-Type"))
	if strings.HasPrefix(ct, "multipart/") {
		file, err := c.FormFile("file")
		if err != nil {
			fail(c, http.StatusBadRequest, "MISSING_FILE", "需要 multipart 字段 file")
			return nil, "", "", 0, false, false
		}
		if file.Size > 0 && h.fileExceedsLimit(file.Size) {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", errFileTooLarge.Error())
			return nil, "", "", 0, false, false
		}
		f, err := file.Open()
		if err != nil {
			fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传文件失败")
			return nil, "", "", 0, false, false
		}
		defer f.Close()
		data, err = readBodyLimited(f, maxBytes)
		if err != nil {
			if errIs(err, errFileTooLarge) {
				fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", errFileTooLarge.Error())
			} else {
				fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传文件失败")
			}
			return nil, "", "", 0, false, false
		}
		if len(data) == 0 {
			fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空")
			return nil, "", "", 0, false, false
		}
		mime = normalizeMIME(file.Header.Get("Content-Type"))
		mime = sniffMediaMIME(data, mime)
		if name == "" {
			name = normalizeItemName(file.Filename)
		}
		return data, mime, name, sortOrder, sortOrderSet, true
	}

	// 原始 body
	raw, err := readBodyLimited(c.Request.Body, maxBytes)
	if err != nil {
		if errIs(err, errFileTooLarge) {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", errFileTooLarge.Error())
		} else {
			fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或读取失败")
		}
		return nil, "", "", 0, false, false
	}
	if len(raw) == 0 {
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空")
		return nil, "", "", 0, false, false
	}
	mime = sniffMediaMIME(raw, ct)
	return raw, mime, name, sortOrder, sortOrderSet, true
}

// nextItemSortOrder 取包内当前最大 sort_order + 1（空包返回 0）。
func nextItemSortOrder(tx *gorm.DB, packID int64) int {
	// COALESCE：空包 MAX 为 NULL → -1，再 +1 得到 0。
	var max int
	if err := tx.Raw(
		`SELECT COALESCE(MAX(sort_order), -1) FROM sticker_items WHERE pack_id = ?`,
		packID,
	).Scan(&max).Error; err != nil {
		return 0
	}
	return max + 1
}

// readBodyLimited 读取全部内容；maxBytes≤0 不限制，>0 时超限返回 errFileTooLarge。
func readBodyLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errFileTooLarge
	}
	return data, nil
}

// patchItem PATCH /users/@me/sticker-packs/{pack_id}/items/{item_id}
func (h *api) patchItem(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	if pack.Status != model.StickerPackActive {
		fail(c, http.StatusBadRequest, "PACK_NOT_ACTIVE", "仅 active 包可编辑条目")
		return
	}
	var item model.StickerItem
	if err := h.db().First(&item, "id = ? AND pack_id = ? AND status = ?",
		itemID, pack.ID, model.StickerItemActive).Error; err != nil {
		notFound(c)
		return
	}
	var input patchItemRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if input.Name != nil {
		n := normalizeItemName(*input.Name)
		if n == "" {
			fail(c, http.StatusBadRequest, "INVALID_NAME", "展示名不能为空")
			return
		}
		if utf8.RuneCountInString(n) > maxItemNameRunes {
			fail(c, http.StatusBadRequest, "INVALID_NAME", "展示名不超过 100 字符")
			return
		}
		updates["name"] = n
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}
	if len(updates) <= 1 {
		fail(c, http.StatusBadRequest, "EMPTY_PATCH", "没有需要更新的字段")
		return
	}
	if err := h.db().Model(&item).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新条目失败")
		return
	}
	_ = h.db().First(&item, "id = ?", item.ID)
	view := toItemView(item, assetURL(h.storageKeyOf(item.AssetID)))
	h.publishToUser(user.ID, eventbus.EventStickerItemUpdate, view)
	c.JSON(http.StatusOK, view)
}

// deleteItem DELETE /users/@me/sticker-packs/{pack_id}/items/{item_id}
// 所有者删除条目 = 硬删条目行语义用 purged（作者侧不可恢复单条；整包软删另论）。
// 文档 C.4：仅管理员可 purged；所有者删除条目采用 purged 以释放配额并递减 ref_count。
// 实现取舍：所有者删条目标记 purged + ref_count--（与「从自己包移除」一致）。
func (h *api) deleteItem(c *gin.Context) {
	packID, ok := parseSnowflakeParam(c, "packID")
	if !ok {
		return
	}
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	user := h.currentUser(c)
	pack, ok := h.loadOwnedPack(c, packID, user.ID)
	if !ok {
		return
	}
	var item model.StickerItem
	if err := h.db().First(&item, "id = ? AND pack_id = ? AND status = ?",
		itemID, pack.ID, model.StickerItemActive).Error; err != nil {
		notFound(c)
		return
	}
	err := h.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&item).Updates(map[string]any{
			"status":     model.StickerItemPurged,
			"updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		if pack.CoverItemID != nil && *pack.CoverItemID == item.ID {
			// 清除条目封面；若有自定义上传封面则不受影响
			if err := tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).
				Update("cover_item_id", nil).Error; err != nil {
				return err
			}
			// 无自定义封面时，把下一首条记为 cover_item
			if pack.CoverAssetID == nil {
				var next model.StickerItem
				if tx.Where("pack_id = ? AND status = ? AND id <> ?",
					pack.ID, model.StickerItemActive, item.ID).
					Order("sort_order ASC, id ASC").First(&next).Error == nil {
					_ = tx.Model(&model.StickerPack{}).Where("id = ?", pack.ID).
						Update("cover_item_id", next.ID).Error
				}
			}
		}
		return h.releaseAsset(tx, item.AssetID)
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除条目失败")
		return
	}
	view := toItemView(item, "")
	view.Status = model.StickerItemPurged
	h.publishToUser(user.ID, eventbus.EventStickerItemDelete, view)
	var libs []model.UserPackLibrary
	_ = h.db().Where("pack_id = ?", pack.ID).Find(&libs).Error
	for _, lib := range libs {
		h.publishToUser(lib.UserID, eventbus.EventStickerItemDelete, view)
	}
	c.Status(http.StatusNoContent)
}

// getItem GET /sticker-items/{item_id}：渲染/预览用，不要求在库（L.2）。
func (h *api) getItem(c *gin.Context) {
	itemID, ok := parseSnowflakeParam(c, "itemID")
	if !ok {
		return
	}
	var item model.StickerItem
	if err := h.db().First(&item, "id = ?", itemID).Error; err != nil {
		notFound(c)
		return
	}
	if item.Status == model.StickerItemPurged {
		// R.6：尽量展示；无法解析则 404 让客户端占位
		fail(c, http.StatusGone, "ITEM_PURGED", "条目已被管理员删除")
		return
	}
	view := toItemView(item, assetURL(h.storageKeyOf(item.AssetID)))
	c.JSON(http.StatusOK, view)
}
