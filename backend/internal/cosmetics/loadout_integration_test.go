package cosmetics_test

// 库存/装备/投影集成测试：库存过期、过期重购续期（grantItem 修复回归）、
// 装备/卸下/槽位互斥/未拥有拒绝、装备事件广播范围、equipped listMode 差异、
// publicProfile 装扮投影。

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// setupPublishedItem 建品类 + 已发布单品（返回品类 key 与 item）。
func setupPublishedItem(t *testing.T, env *testEnv, adminToken string, price int, seed byte) (string, itemViewT) {
	t.Helper()
	key := newUniqueKey("cat")
	env.createCategory(t, adminToken, key, simpleImageSchema())
	item := env.createItem(t, adminToken, key, "装备品-"+key, price)
	env.uploadItemAsset(t, adminToken, item.ID, "primary", "image/png", staticPNG(seed))
	env.publishItem(t, adminToken, item.ID)
	return key, item
}

func TestInventoryExpiryAndRepurchaseRevive(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	_, item := setupPublishedItem(t, env, admin.AccessToken, 60, 0xB1)

	// 直接落一条已过期库存行
	past := time.Now().UTC().Add(-time.Hour)
	itemID := mustParseInt64(t, item.ID)
	row := model.UserCosmeticInventory{
		ID: time.Now().UnixNano(), UserID: user.User.ID, ItemID: itemID,
		Source: model.CosmeticSourcePoints, ExpiresAt: &past, AcquiredAt: past.Add(-time.Hour),
	}
	if err := env.db.Create(&row).Error; err != nil {
		t.Fatalf("造过期库存失败: %v", err)
	}
	// 过期项不出现在 inventory 列表
	inv := decode[struct {
		Inventory []struct {
			ItemID string `json:"item_id"`
		} `json:"inventory"`
	}](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/cosmetics/inventory", user.AccessToken, nil))
	for _, e := range inv.Inventory {
		if e.ItemID == item.ID {
			t.Fatal("过期库存不应出现在列表")
		}
	}
	// 商店 owned=false
	shop := env.getShop(t, user.AccessToken, "")
	if got, ok := shopHasItem(shop, item.ID); ok && got.Owned != nil && *got.Owned {
		t.Fatal("过期库存商品在商店应 owned=false")
	}
	// 重购（永久）→ 库存复活为永久（grantItem 续期修复回归）
	env.givePoints(t, admin.AccessToken, user.User.ID, 60)
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/purchase", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": item.ID}); r.Code != http.StatusOK {
		t.Fatalf("过期后重购失败: %d %s", r.Code, r.Body.String())
	}
	var fresh model.UserCosmeticInventory
	if err := env.db.Where("user_id = ? AND item_id = ?", user.User.ID, itemID).First(&fresh).Error; err != nil {
		t.Fatalf("读库存失败: %v", err)
	}
	if fresh.ExpiresAt != nil {
		t.Fatalf("重购后应升级为永久，实际 expires_at=%v（扣了积分库存仍过期的缺陷回归）", fresh.ExpiresAt)
	}
}

func TestLoadoutEquipUnequipAndEvents(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	other := env.signup(t)
	guildID := uuid.New()
	env.joinGuild(t, guildID, user.User.ID, other.User.ID)

	catKey, item := setupPublishedItem(t, env, admin.AccessToken, 0, 0xC1)
	_, itemB := setupPublishedItem(t, env, admin.AccessToken, 0, 0xC2) // 另一品类（不同 slot）

	// 未拥有装备 → 403 NOT_OWNED
	if r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/cosmetics/loadout/"+catKey, user.AccessToken,
		map[string]any{"item_id": item.ID}); r.Code != http.StatusForbidden {
		t.Fatalf("未拥有装备应 403，实际 %d %s", r.Code, r.Body.String())
	}
	// claim 后装备成功
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/claim", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": item.ID}); r.Code != http.StatusOK {
		t.Fatalf("claim 失败: %d %s", r.Code, r.Body.String())
	}
	r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/cosmetics/loadout/"+catKey, user.AccessToken,
		map[string]any{"item_id": item.ID})
	if r.Code != http.StatusOK {
		t.Fatalf("装备失败: %d %s", r.Code, r.Body.String())
	}
	loadout := decode[loadoutViewT](t, r)
	if loadout.Slots[catKey].ItemID != item.ID {
		t.Fatalf("装备后 loadout 槽位应为 %s，实际 %+v", item.ID, loadout.Slots)
	}
	// 事件广播范围：本人定向一条 + 所在 guild 广播一条
	env.events.wait(t, "LOADOUT_UPDATE 定向本人", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventCosmeticLoadoutUpdate && len(e.UserIDs) == 1 && e.UserIDs[0] == user.User.ID
	})
	env.events.wait(t, "LOADOUT_UPDATE guild 广播", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventCosmeticLoadoutUpdate && e.GuildID != nil && *e.GuildID == guildID
	})
	// 槽位不匹配：把 itemB（其品类 slot 不同）装到 catKey 槽 → SLOT_MISMATCH
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/claim", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": itemB.ID}); r.Code != http.StatusOK {
		t.Fatalf("claim B 失败: %d", r.Code)
	}
	if r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/cosmetics/loadout/"+catKey, user.AccessToken,
		map[string]any{"item_id": itemB.ID}); r.Code != http.StatusBadRequest {
		t.Fatalf("槽位不匹配应 400，实际 %d %s", r.Code, r.Body.String())
	}
	// 卸下
	r = env.request(t, http.MethodDelete, "/gapi/v1/users/@me/cosmetics/loadout/"+catKey, user.AccessToken, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("卸下失败: %d", r.Code)
	}
	if after := decode[loadoutViewT](t, r); len(after.Slots) != 0 {
		if _, still := after.Slots[catKey]; still {
			t.Fatal("卸下后槽位应移除")
		}
	}
}

func TestEquippedProjectionListModeAndExpiry(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	viewer := env.signup(t)
	guildID := uuid.New()
	env.joinGuild(t, guildID, user.User.ID, viewer.User.ID)

	// 建一个非 listMode 槽位的品类（profile_border 之外的自定义 slot）并装备
	catKey, item := setupPublishedItem(t, env, admin.AccessToken, 0, 0xD1)
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/claim", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": item.ID}); r.Code != http.StatusOK {
		t.Fatalf("claim 失败: %d", r.Code)
	}
	if r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/cosmetics/loadout/"+catKey, user.AccessToken,
		map[string]any{"item_id": item.ID}); r.Code != http.StatusOK {
		t.Fatalf("装备失败: %d", r.Code)
	}
	// listMode（默认）：自定义槽不返回（仅 avatar_frame/nameplate）
	listView := decode[loadoutViewT](t, env.request(t, http.MethodGet,
		"/gapi/v1/users/"+user.User.ID.String()+"/cosmetics/equipped", viewer.AccessToken, nil))
	if _, ok := listView.Slots[catKey]; ok {
		t.Fatal("listMode 不应返回非 avatar_frame/nameplate 槽位")
	}
	// full=1：返回全部槽位
	fullView := decode[loadoutViewT](t, env.request(t, http.MethodGet,
		"/gapi/v1/users/"+user.User.ID.String()+"/cosmetics/equipped?full=1", viewer.AccessToken, nil))
	if fullView.Slots[catKey].ItemID != item.ID {
		t.Fatalf("full 模式应返回自定义槽位，实际 %+v", fullView.Slots)
	}

	// publicProfile 携带装扮投影（B1 回归）
	profile := decode[struct {
		Cosmetics map[string]struct {
			ItemID string `json:"item_id"`
		} `json:"cosmetics"`
	}](t, env.request(t, http.MethodGet, "/gapi/v1/users/"+user.User.ID.String(), viewer.AccessToken, nil))
	if profile.Cosmetics[catKey].ItemID != item.ID {
		t.Fatalf("publicProfile 应含 full 模式装扮投影，实际 %+v", profile.Cosmetics)
	}

	// 库存过期后投影剔除该槽
	itemID := mustParseInt64(t, item.ID)
	past := time.Now().UTC().Add(-time.Minute)
	if err := env.db.Model(&model.UserCosmeticInventory{}).
		Where("user_id = ? AND item_id = ?", user.User.ID, itemID).
		Update("expires_at", &past).Error; err != nil {
		t.Fatalf("改过期失败: %v", err)
	}
	expiredView := decode[loadoutViewT](t, env.request(t, http.MethodGet,
		"/gapi/v1/users/"+user.User.ID.String()+"/cosmetics/equipped?full=1", viewer.AccessToken, nil))
	if _, ok := expiredView.Slots[catKey]; ok {
		t.Fatal("库存过期后 equipped 投影应剔除该槽")
	}
}

func TestAdminGrantIdempotentAndExpiry(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	_, item := setupPublishedItem(t, env, admin.AccessToken, 0, 0xE1)

	grant := func(expiresAt any) *struct {
		OK      bool `json:"ok"`
		Created bool `json:"created"`
	} {
		body := map[string]any{"user_id": user.User.ID.String(), "item_id": item.ID}
		if expiresAt != nil {
			body["expires_at"] = expiresAt
		}
		r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/grant", admin.AccessToken, body)
		if r.Code != http.StatusOK {
			t.Fatalf("grant 失败: %d %s", r.Code, r.Body.String())
		}
		out := decode[struct {
			OK      bool `json:"ok"`
			Created bool `json:"created"`
		}](t, r)
		return &out
	}
	if first := grant(nil); !first.Created {
		t.Fatal("首次发放 created 应为 true")
	}
	if second := grant(nil); second.Created {
		t.Fatal("重复发放 created 应为 false")
	}
	// 非 admin 调用 → 403
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/grant", user.AccessToken,
		map[string]any{"user_id": user.User.ID.String(), "item_id": item.ID}); r.Code != http.StatusForbidden {
		t.Fatalf("非管理员 grant 应 403，实际 %d", r.Code)
	}
	// 永久库存不被限时授予降级
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	grant(future)
	var row model.UserCosmeticInventory
	itemID := mustParseInt64(t, item.ID)
	if err := env.db.Where("user_id = ? AND item_id = ?", user.User.ID, itemID).First(&row).Error; err != nil {
		t.Fatalf("读库存失败: %v", err)
	}
	if row.ExpiresAt != nil {
		t.Fatalf("永久库存不应被限时授予降级: %v", row.ExpiresAt)
	}
}

func mustParseInt64(t *testing.T, s string) int64 {
	t.Helper()
	var out int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			t.Fatalf("非法 int64 字符串: %s", s)
		}
		out = out*10 + int64(ch-'0')
	}
	return out
}
