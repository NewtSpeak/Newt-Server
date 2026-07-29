package cosmetics

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/newtspeak/newt-server/backend/internal/model"
)

type assetView struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	MIME      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Animated  bool   `json:"animated"`
	SizeBytes int64  `json:"size_bytes"`
}

type tagView struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type categoryView struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Slot        string          `json:"slot"`
	Schema      json.RawMessage `json:"schema"`
	SortOrder   int             `json:"sort_order"`
	Enabled     bool            `json:"enabled"`
}

type itemView struct {
	ID             string            `json:"id"`
	CategoryKey    string            `json:"category_key"`
	Slot           string            `json:"slot,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	PreviewURL     string            `json:"preview_url,omitempty"`
	Assets         map[string]assetView `json:"assets"`
	Payload        json.RawMessage   `json:"payload"`
	PricePoints    int               `json:"price_points"`
	Status         string            `json:"status,omitempty"`
	SortOrder      int               `json:"sort_order"`
	Tags           []tagView         `json:"tags,omitempty"`
	Owned          *bool             `json:"owned,omitempty"`
	AvailableFrom  *time.Time        `json:"available_from,omitempty"`
	AvailableUntil *time.Time        `json:"available_until,omitempty"`
}

type bundleView struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	PreviewURL     string     `json:"preview_url,omitempty"`
	PricePoints    int        `json:"price_points"`
	Status         string     `json:"status,omitempty"`
	SortOrder      int        `json:"sort_order"`
	Tags           []tagView  `json:"tags,omitempty"`
	ItemIDs        []string   `json:"item_ids,omitempty"`
	Items          []itemView `json:"items,omitempty"`
	OwnedAll       *bool      `json:"owned_all,omitempty"`
	AvailableFrom  *time.Time `json:"available_from,omitempty"`
	AvailableUntil *time.Time `json:"available_until,omitempty"`
}

// EquippedSlotView 装备投影（下发给成员列表/资料卡）。
type EquippedSlotView struct {
	ItemID      string               `json:"item_id"`
	CategoryKey string               `json:"category_key"`
	Slot        string               `json:"slot"`
	Name        string               `json:"name"`
	Assets      map[string]assetView `json:"assets"`
	Payload     json.RawMessage      `json:"payload"`
	RenderHint  string               `json:"render_hint,omitempty"`
}

func (h *api) categoryView(c model.CosmeticCategory) categoryView {
	schema := json.RawMessage(c.SchemaJSON)
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	return categoryView{
		Key: c.Key, Name: c.Name, Description: c.Description, Slot: c.Slot,
		Schema: schema, SortOrder: c.SortOrder, Enabled: c.Enabled,
	}
}

func (h *api) itemView(item model.CosmeticItem, assets map[int64]model.CosmeticAsset, tags []tagView, slot string) itemView {
	assetMap, _ := parseAssetsMap(item.AssetsJSON)
	resolved := make(map[string]assetView, len(assetMap))
	for k, id := range assetMap {
		if a, ok := assets[id]; ok {
			resolved[k] = assetView{
				ID: strID(a.ID), URL: assetPublicURL(a), MIME: a.MIME,
				Width: a.Width, Height: a.Height, Animated: a.Animated, SizeBytes: a.SizeBytes,
			}
		}
	}
	preview := ""
	if item.PreviewAssetID != nil {
		if a, ok := assets[*item.PreviewAssetID]; ok {
			preview = assetPublicURL(a)
		}
	} else {
		// 回退：第一个资产
		for _, v := range resolved {
			preview = v.URL
			break
		}
	}
	payload := json.RawMessage(item.PayloadJSON)
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if tags == nil {
		tags = []tagView{}
	}
	return itemView{
		ID: strID(item.ID), CategoryKey: item.CategoryKey, Slot: slot,
		Name: item.Name, Description: item.Description, PreviewURL: preview,
		Assets: resolved, Payload: payload, PricePoints: item.PricePoints,
		Status: string(item.Status), SortOrder: item.SortOrder, Tags: tags,
		AvailableFrom: item.AvailableFrom, AvailableUntil: item.AvailableUntil,
	}
}

func (h *api) collectAssetIDsFromItem(item model.CosmeticItem) []int64 {
	ids := []int64{}
	m, _ := parseAssetsMap(item.AssetsJSON)
	for _, id := range m {
		ids = append(ids, id)
	}
	if item.PreviewAssetID != nil {
		ids = append(ids, *item.PreviewAssetID)
	}
	return ids
}

func itemAvailableNow(item model.CosmeticItem, now time.Time) bool {
	if item.Status != model.CosmeticStatusPublished {
		return false
	}
	if item.AvailableFrom != nil && now.Before(*item.AvailableFrom) {
		return false
	}
	if item.AvailableUntil != nil && !now.Before(*item.AvailableUntil) {
		return false
	}
	return true
}

func bundleAvailableNow(b model.CosmeticBundle, now time.Time) bool {
	if b.Status != model.CosmeticStatusPublished {
		return false
	}
	if b.AvailableFrom != nil && now.Before(*b.AvailableFrom) {
		return false
	}
	if b.AvailableUntil != nil && !now.Before(*b.AvailableUntil) {
		return false
	}
	return true
}

func inventoryActive(inv model.UserCosmeticInventory, now time.Time) bool {
	if inv.ExpiresAt == nil {
		return true
	}
	return inv.ExpiresAt.After(now)
}

func normalizePayloadJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if !json.Valid([]byte(raw)) {
		return "{}"
	}
	return raw
}
