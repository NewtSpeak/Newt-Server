package cosmetics

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// listCategories GET /cosmetics/categories 与 admin 列表共用。
func (h *api) listCategories(c *gin.Context) {
	admin := c.GetBool("cosmetics_admin")
	q := h.db().Model(&model.CosmeticCategory{})
	if !admin {
		q = q.Where("enabled = ?", true)
	}
	var rows []model.CosmeticCategory
	if err := q.Order("sort_order ASC, key ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取品类失败")
		return
	}
	views := make([]categoryView, 0, len(rows))
	for _, r := range rows {
		views = append(views, h.categoryView(r))
	}
	c.JSON(http.StatusOK, gin.H{"categories": views, "version": catalogVersion(rows)})
}

func catalogVersion(rows []model.CosmeticCategory) string {
	var max time.Time
	for _, r := range rows {
		if r.UpdatedAt.After(max) {
			max = r.UpdatedAt
		}
	}
	if max.IsZero() {
		return "0"
	}
	return max.UTC().Format(time.RFC3339Nano)
}

type upsertCategoryRequest struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Slot        string          `json:"slot"`
	Schema      json.RawMessage `json:"schema"`
	SortOrder   *int            `json:"sort_order"`
	Enabled     *bool           `json:"enabled"`
}

// createCategory POST /admin/cosmetics/categories
func (h *api) createCategory(c *gin.Context) {
	var input upsertCategoryRequest
	if !bind(c, &input) {
		return
	}
	key := strings.TrimSpace(input.Key)
	if key == "" || utf8.RuneCountInString(key) > 64 {
		fail(c, http.StatusBadRequest, "INVALID_KEY", "key 必填且 ≤64")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "INVALID_NAME", "name 必填")
		return
	}
	slot := strings.TrimSpace(input.Slot)
	if slot == "" {
		slot = key
	}
	schemaRaw := string(input.Schema)
	if schemaRaw == "" {
		schemaRaw = "{}"
	}
	schema, err := parseCategorySchema(schemaRaw)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_SCHEMA", err.Error())
		return
	}
	if err := validateCategorySchema(schema); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_SCHEMA", err.Error())
		return
	}
	now := time.Now().UTC()
	sortOrder := 100
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	cat := model.CosmeticCategory{
		Key: key, Name: clampRunes(name, maxNameRunes),
		Description: clampRunes(strings.TrimSpace(input.Description), maxDescRunes),
		Slot: slot, SchemaJSON: mustJSON(schema), SortOrder: sortOrder, Enabled: enabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := h.db().Create(&cat).Error; err != nil {
		fail(c, http.StatusConflict, "CONFLICT", "品类已存在或写入失败")
		return
	}
	h.publishCatalogUpdate(gin.H{"op": "category_create", "category": h.categoryView(cat)})
	c.JSON(http.StatusCreated, h.categoryView(cat))
}

// patchCategory PATCH /admin/cosmetics/categories/:key
func (h *api) patchCategory(c *gin.Context) {
	key := strings.TrimSpace(c.Param("key"))
	var cat model.CosmeticCategory
	if err := h.db().First(&cat, "key = ?", key).Error; err != nil {
		notFound(c)
		return
	}
	var input upsertCategoryRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if strings.TrimSpace(input.Name) != "" {
		updates["name"] = clampRunes(strings.TrimSpace(input.Name), maxNameRunes)
	}
	if input.Description != "" || c.Request.ContentLength > 0 {
		// 允许显式清空 description
		if input.Name != "" || input.Schema != nil || input.Slot != "" || input.SortOrder != nil || input.Enabled != nil || input.Description != "" {
			updates["description"] = clampRunes(strings.TrimSpace(input.Description), maxDescRunes)
		}
	}
	if s := strings.TrimSpace(input.Slot); s != "" {
		updates["slot"] = s
	}
	if len(input.Schema) > 0 {
		schema, err := parseCategorySchema(string(input.Schema))
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_SCHEMA", err.Error())
			return
		}
		if err := validateCategorySchema(schema); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_SCHEMA", err.Error())
			return
		}
		updates["schema_json"] = mustJSON(schema)
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}
	if err := h.db().Model(&cat).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新品类失败")
		return
	}
	_ = h.db().First(&cat, "key = ?", key)
	h.publishCatalogUpdate(gin.H{"op": "category_update", "category": h.categoryView(cat)})
	c.JSON(http.StatusOK, h.categoryView(cat))
}
