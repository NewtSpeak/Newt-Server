package cosmetics

import (
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// ResolveEquippedForUsers 批量解析用户装备投影。
// listMode=true 时仅返回 avatar_frame + nameplate（成员列表体积控制）。
func ResolveEquippedForUsers(db *gorm.DB, userIDs []uuid.UUID, listMode bool) map[uuid.UUID]map[string]EquippedSlotView {
	out := map[uuid.UUID]map[string]EquippedSlotView{}
	if db == nil || len(userIDs) == 0 {
		return out
	}
	now := time.Now().UTC()
	var rows []model.UserCosmeticLoadout
	_ = db.Where("user_id IN ?", userIDs).Find(&rows).Error
	if len(rows) == 0 {
		return out
	}

	itemIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		if listMode && r.Slot != "avatar_frame" && r.Slot != "nameplate" {
			continue
		}
		itemIDs = append(itemIDs, r.ItemID)
	}
	if len(itemIDs) == 0 {
		return out
	}

	var items []model.CosmeticItem
	_ = db.Where("id IN ?", itemIDs).Find(&items).Error
	itemMap := map[int64]model.CosmeticItem{}
	for _, it := range items {
		itemMap[it.ID] = it
	}

	var cats []model.CosmeticCategory
	_ = db.Find(&cats).Error
	schemaByKey := map[string]CategorySchema{}
	for _, c := range cats {
		s, _ := parseCategorySchema(c.SchemaJSON)
		schemaByKey[c.Key] = s
	}

	var invs []model.UserCosmeticInventory
	_ = db.Where("user_id IN ? AND item_id IN ?", userIDs, itemIDs).Find(&invs).Error
	own := map[string]bool{}
	for _, inv := range invs {
		if inventoryActive(inv, now) {
			own[inv.UserID.String()+"|"+strID(inv.ItemID)] = true
		}
	}

	assetIDSet := map[int64]struct{}{}
	for _, it := range items {
		m, _ := parseAssetsMap(it.AssetsJSON)
		for _, id := range m {
			assetIDSet[id] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(assetIDSet))
	for id := range assetIDSet {
		ids = append(ids, id)
	}
	var assets []model.CosmeticAsset
	if len(ids) > 0 {
		_ = db.Where("id IN ?", ids).Find(&assets).Error
	}
	assetMap := map[int64]model.CosmeticAsset{}
	for _, a := range assets {
		assetMap[a.ID] = a
	}

	for _, r := range rows {
		if listMode && r.Slot != "avatar_frame" && r.Slot != "nameplate" {
			continue
		}
		if !own[r.UserID.String()+"|"+strID(r.ItemID)] {
			continue
		}
		it, ok := itemMap[r.ItemID]
		if !ok {
			continue
		}
		am, _ := parseAssetsMap(it.AssetsJSON)
		resolved := map[string]assetView{}
		for k, id := range am {
			if a, ok := assetMap[id]; ok {
				resolved[k] = assetView{
					ID: strID(a.ID), URL: assetPublicURL(a), MIME: a.MIME,
					Width: a.Width, Height: a.Height, Animated: a.Animated, SizeBytes: a.SizeBytes,
				}
			}
		}
		if out[r.UserID] == nil {
			out[r.UserID] = map[string]EquippedSlotView{}
		}
		out[r.UserID][r.Slot] = EquippedSlotView{
			ItemID: strID(it.ID), CategoryKey: it.CategoryKey, Slot: r.Slot,
			Name: it.Name, Assets: resolved, Payload: jsonRaw(it.PayloadJSON),
			RenderHint: schemaByKey[it.CategoryKey].RenderHint,
		}
	}
	return out
}
