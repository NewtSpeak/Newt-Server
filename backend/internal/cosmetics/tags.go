package cosmetics

import (
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

var tagKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// listTags GET /cosmetics/tags
func (h *api) listTags(c *gin.Context) {
	var rows []model.CosmeticTag
	if err := h.db().Order("sort_order ASC, name ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取标签失败")
		return
	}
	views := make([]tagView, 0, len(rows))
	for _, r := range rows {
		views = append(views, tagView{ID: strID(r.ID), Key: r.Key, Name: r.Name, Color: r.Color})
	}
	c.JSON(http.StatusOK, gin.H{"tags": views})
}

type tagRequest struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	SortOrder *int   `json:"sort_order"`
}

// createTag POST /admin/cosmetics/tags
func (h *api) createTag(c *gin.Context) {
	var input tagRequest
	if !bind(c, &input) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(input.Key))
	if !tagKeyPattern.MatchString(key) || utf8.RuneCountInString(key) > maxTagKeyRunes {
		fail(c, http.StatusBadRequest, "INVALID_KEY", "key 须为小写字母数字与连字符")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "INVALID_NAME", "name 必填")
		return
	}
	color := strings.TrimSpace(input.Color)
	if color != "" && !hexColorPattern.MatchString(color) {
		fail(c, http.StatusBadRequest, "INVALID_COLOR", "color 须为 #RRGGBB")
		return
	}
	sortOrder := 0
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	now := time.Now().UTC()
	tag := model.CosmeticTag{
		ID: h.ids.Next(), Key: key, Name: clampRunes(name, maxNameRunes),
		Color: color, SortOrder: sortOrder, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.db().Create(&tag).Error; err != nil {
		fail(c, http.StatusConflict, "CONFLICT", "标签已存在")
		return
	}
	h.publishCatalogUpdate(gin.H{"op": "tag_create", "tag": tagView{ID: strID(tag.ID), Key: tag.Key, Name: tag.Name, Color: tag.Color}})
	c.JSON(http.StatusCreated, tagView{ID: strID(tag.ID), Key: tag.Key, Name: tag.Name, Color: tag.Color})
}

// patchTag PATCH /admin/cosmetics/tags/:tagID
func (h *api) patchTag(c *gin.Context) {
	tagID, ok := parseSnowflakeParam(c, "tagID")
	if !ok {
		return
	}
	var tag model.CosmeticTag
	if err := h.db().First(&tag, "id = ?", tagID).Error; err != nil {
		notFound(c)
		return
	}
	var input tagRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if s := strings.TrimSpace(input.Name); s != "" {
		updates["name"] = clampRunes(s, maxNameRunes)
	}
	if input.Color != "" {
		if !hexColorPattern.MatchString(strings.TrimSpace(input.Color)) {
			fail(c, http.StatusBadRequest, "INVALID_COLOR", "color 须为 #RRGGBB")
			return
		}
		updates["color"] = strings.TrimSpace(input.Color)
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}
	if err := h.db().Model(&tag).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新标签失败")
		return
	}
	_ = h.db().First(&tag, "id = ?", tagID)
	c.JSON(http.StatusOK, tagView{ID: strID(tag.ID), Key: tag.Key, Name: tag.Name, Color: tag.Color})
}

// deleteTag DELETE /admin/cosmetics/tags/:tagID
func (h *api) deleteTag(c *gin.Context) {
	tagID, ok := parseSnowflakeParam(c, "tagID")
	if !ok {
		return
	}
	_ = h.db().Where("tag_id = ?", tagID).Delete(&model.CosmeticItemTag{}).Error
	_ = h.db().Where("tag_id = ?", tagID).Delete(&model.CosmeticBundleTag{}).Error
	if err := h.db().Delete(&model.CosmeticTag{}, "id = ?", tagID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除标签失败")
		return
	}
	h.publishCatalogUpdate(gin.H{"op": "tag_delete", "tag_id": strID(tagID)})
	c.Status(http.StatusNoContent)
}

func (h *api) tagsForItems(itemIDs []int64) map[int64][]tagView {
	out := map[int64][]tagView{}
	if len(itemIDs) == 0 {
		return out
	}
	type row struct {
		ItemID int64
		TagID  int64
		Key    string
		Name   string
		Color  string
	}
	var rows []row
	_ = h.db().Raw(`SELECT it.item_id, t.id AS tag_id, t.key, t.name, t.color
		FROM cosmetic_item_tags it
		JOIN cosmetic_tags t ON t.id = it.tag_id
		WHERE it.item_id IN ?`, itemIDs).Scan(&rows).Error
	for _, r := range rows {
		out[r.ItemID] = append(out[r.ItemID], tagView{ID: strID(r.TagID), Key: r.Key, Name: r.Name, Color: r.Color})
	}
	return out
}

func (h *api) tagsForBundles(bundleIDs []int64) map[int64][]tagView {
	out := map[int64][]tagView{}
	if len(bundleIDs) == 0 {
		return out
	}
	type row struct {
		BundleID int64
		TagID    int64
		Key      string
		Name     string
		Color    string
	}
	var rows []row
	_ = h.db().Raw(`SELECT bt.bundle_id, t.id AS tag_id, t.key, t.name, t.color
		FROM cosmetic_bundle_tags bt
		JOIN cosmetic_tags t ON t.id = bt.tag_id
		WHERE bt.bundle_id IN ?`, bundleIDs).Scan(&rows).Error
	for _, r := range rows {
		out[r.BundleID] = append(out[r.BundleID], tagView{ID: strID(r.TagID), Key: r.Key, Name: r.Name, Color: r.Color})
	}
	return out
}

func (h *api) setItemTags(itemID int64, tagIDs []int64) error {
	if err := h.db().Where("item_id = ?", itemID).Delete(&model.CosmeticItemTag{}).Error; err != nil {
		return err
	}
	for _, tid := range tagIDs {
		if tid <= 0 {
			continue
		}
		if err := h.db().Create(&model.CosmeticItemTag{ItemID: itemID, TagID: tid}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (h *api) setBundleTags(bundleID int64, tagIDs []int64) error {
	if err := h.db().Where("bundle_id = ?", bundleID).Delete(&model.CosmeticBundleTag{}).Error; err != nil {
		return err
	}
	for _, tid := range tagIDs {
		if tid <= 0 {
			continue
		}
		if err := h.db().Create(&model.CosmeticBundleTag{BundleID: bundleID, TagID: tid}).Error; err != nil {
			return err
		}
	}
	return nil
}

func parseTagIDList(raw []string) []int64 {
	ids := make([]int64, 0, len(raw))
	for _, s := range raw {
		id, err := parseSnowflakeString(strings.TrimSpace(s))
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}
