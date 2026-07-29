package cosmetics_test

// 商店与购买集成测试：可见性（状态/时间窗）、tag 筛选、claim/purchase、
// 积分扣减与流水、余额不足回滚、捆绑包展开与幂等。

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// shopHasItem 在商店响应中查找 item。
func shopHasItem(shop shopViewT, itemID string) (itemViewT, bool) {
	for _, it := range shop.Items {
		if it.ID == itemID {
			return it, true
		}
	}
	return itemViewT{}, false
}

func (env *testEnv) getShop(t *testing.T, token, query string) shopViewT {
	t.Helper()
	r := env.request(t, http.MethodGet, "/gapi/v1/cosmetics/shop"+query, token, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("读商店失败: %d %s", r.Code, r.Body.String())
	}
	return decode[shopViewT](t, r)
}

func TestShopVisibilityByStatusAndWindow(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())

	draft := env.createItem(t, admin.AccessToken, key, "草稿品", 0)
	published := env.createItem(t, admin.AccessToken, key, "上架品", 0)
	env.uploadItemAsset(t, admin.AccessToken, published.ID, "primary", "image/png", staticPNG(0x61))
	env.publishItem(t, admin.AccessToken, published.ID)

	// 时间窗未来 / 已过的两件
	future := env.createItem(t, admin.AccessToken, key, "未来品", 0)
	env.uploadItemAsset(t, admin.AccessToken, future.ID, "primary", "image/png", staticPNG(0x62))
	if r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/items/"+future.ID, admin.AccessToken,
		map[string]any{"status": "published", "available_from": time.Now().UTC().Add(24 * time.Hour)}); r.Code != http.StatusOK {
		t.Fatalf("设未来窗失败: %d %s", r.Code, r.Body.String())
	}
	expired := env.createItem(t, admin.AccessToken, key, "过期品", 0)
	env.uploadItemAsset(t, admin.AccessToken, expired.ID, "primary", "image/png", staticPNG(0x63))
	if r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/items/"+expired.ID, admin.AccessToken,
		map[string]any{"status": "published", "available_until": time.Now().UTC().Add(-time.Hour)}); r.Code != http.StatusOK {
		t.Fatalf("设过期窗失败: %d %s", r.Code, r.Body.String())
	}

	shop := env.getShop(t, user.AccessToken, "?category="+key)
	if _, ok := shopHasItem(shop, published.ID); !ok {
		t.Fatal("published 商品应出现在商店")
	}
	for _, hidden := range []string{draft.ID, future.ID, expired.ID} {
		if _, ok := shopHasItem(shop, hidden); ok {
			t.Fatalf("商品 %s 不应出现在商店（draft/时间窗外）", hidden)
		}
	}
	// 未拥有时草稿详情 → 404
	if r := env.request(t, http.MethodGet, "/gapi/v1/cosmetics/items/"+draft.ID, user.AccessToken, nil); r.Code != http.StatusNotFound {
		t.Fatalf("未上架商品详情应 404，实际 %d", r.Code)
	}
}

func TestShopTagFilter(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())

	tagged := env.createItem(t, admin.AccessToken, key, "打标品", 0)
	plain := env.createItem(t, admin.AccessToken, key, "无标品", 0)
	for _, id := range []string{tagged.ID, plain.ID} {
		env.uploadItemAsset(t, admin.AccessToken, id, "primary", "image/png", staticPNG(byte(0x71+len(id)%7)))
		env.publishItem(t, admin.AccessToken, id)
	}
	tagKey := newUniqueKey("tag")
	tag := decode[struct {
		ID string `json:"id"`
	}](t, env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/tags", admin.AccessToken,
		map[string]any{"key": tagKey, "name": "筛选主题"}))
	if r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/items/"+tagged.ID, admin.AccessToken,
		map[string]any{"tag_ids": []string{tag.ID}}); r.Code != http.StatusOK {
		t.Fatalf("打标失败: %d", r.Code)
	}

	shop := env.getShop(t, user.AccessToken, "?tag="+tagKey)
	if _, ok := shopHasItem(shop, tagged.ID); !ok {
		t.Fatal("tag 筛选应命中打标商品")
	}
	if _, ok := shopHasItem(shop, plain.ID); ok {
		t.Fatal("tag 筛选不应包含未打标商品")
	}
	// 未知 tag → 空列表
	empty := env.getShop(t, user.AccessToken, "?tag=no-such-tag")
	if len(empty.Items) != 0 || len(empty.Bundles) != 0 {
		t.Fatal("未知 tag 应返回空列表")
	}
}

func TestClaimFree(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	free := env.createItem(t, admin.AccessToken, key, "免费品", 0)
	env.uploadItemAsset(t, admin.AccessToken, free.ID, "primary", "image/png", staticPNG(0x81))
	env.publishItem(t, admin.AccessToken, free.ID)
	paid := env.createItem(t, admin.AccessToken, key, "付费品", 30)
	env.uploadItemAsset(t, admin.AccessToken, paid.ID, "primary", "image/png", staticPNG(0x82))
	env.publishItem(t, admin.AccessToken, paid.ID)

	// claim 成功 + inventory 落库 source=claim + 定向事件
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/claim", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": free.ID}); r.Code != http.StatusOK {
		t.Fatalf("claim 失败: %d %s", r.Code, r.Body.String())
	}
	env.events.wait(t, "claim 后 INVENTORY_UPDATE 定向事件", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventCosmeticInventoryUpdate &&
			len(e.UserIDs) == 1 && e.UserIDs[0] == user.User.ID
	})
	var inv model.UserCosmeticInventory
	if err := env.db.Where("user_id = ?", user.User.ID).First(&inv).Error; err != nil {
		t.Fatalf("库存未落库: %v", err)
	}
	if inv.Source != model.CosmeticSourceClaim {
		t.Fatalf("来源应为 claim，实际 %s", inv.Source)
	}
	// 重复 claim → 409
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/claim", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": free.ID}); r.Code != http.StatusConflict {
		t.Fatalf("重复 claim 应 409，实际 %d", r.Code)
	}
	// 付费商品 claim → NOT_FREE
	r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/claim", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": paid.ID})
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "NOT_FREE") {
		t.Fatalf("付费商品 claim 应 NOT_FREE，实际 %d %s", r.Code, r.Body.String())
	}
	// 免费商品 purchase → USE_CLAIM
	r = env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/purchase", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": free.ID})
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "USE_CLAIM") {
		t.Fatalf("免费商品 purchase 应 USE_CLAIM，实际 %d %s", r.Code, r.Body.String())
	}
	// 商店 owned 标记
	shop := env.getShop(t, user.AccessToken, "?category="+key)
	got, ok := shopHasItem(shop, free.ID)
	if !ok || got.Owned == nil || !*got.Owned {
		t.Fatal("已 claim 商品在商店应标记 owned=true")
	}
}

func TestPurchaseWithPointsAndRollback(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	item := env.createItem(t, admin.AccessToken, key, "积分品", 60)
	env.uploadItemAsset(t, admin.AccessToken, item.ID, "primary", "image/png", staticPNG(0x91))
	env.publishItem(t, admin.AccessToken, item.ID)

	// 余额不足：无任何变化（事务回滚断言）
	r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/purchase", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": item.ID})
	if r.Code != http.StatusPaymentRequired {
		t.Fatalf("余额不足应 402，实际 %d %s", r.Code, r.Body.String())
	}
	var invCnt, ledgerCnt int64
	env.db.Model(&model.UserCosmeticInventory{}).Where("user_id = ?", user.User.ID).Count(&invCnt)
	env.db.Model(&model.UserPointsLedger{}).Where("user_id = ?", user.User.ID).Count(&ledgerCnt)
	if invCnt != 0 || ledgerCnt != 0 {
		t.Fatalf("失败购买不应留下库存/流水: inv=%d ledger=%d", invCnt, ledgerCnt)
	}

	// 发 100 分买 60 分商品 → balance 40，流水 +100/-60
	env.givePoints(t, admin.AccessToken, user.User.ID, 100)
	r = env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/purchase", user.AccessToken,
		map[string]any{"target_type": "item", "target_id": item.ID})
	if r.Code != http.StatusOK {
		t.Fatalf("购买失败: %d %s", r.Code, r.Body.String())
	}
	result := decode[struct {
		Balance int64 `json:"balance"`
	}](t, r)
	if result.Balance != 40 {
		t.Fatalf("购买后余额应 40，实际 %d", result.Balance)
	}
	env.events.wait(t, "POINTS_UPDATE 事件", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventCosmeticPointsUpdate && len(e.UserIDs) == 1 && e.UserIDs[0] == user.User.ID
	})
	var ledger []model.UserPointsLedger
	env.db.Where("user_id = ?", user.User.ID).Order("created_at ASC").Find(&ledger)
	if len(ledger) != 2 || ledger[0].Delta != 100 || ledger[1].Delta != -60 || ledger[1].BalanceAfter != 40 {
		t.Fatalf("流水异常: %+v", ledger)
	}
	var order model.CosmeticOrder
	if err := env.db.Where("user_id = ?", user.User.ID).First(&order).Error; err != nil ||
		order.Status != model.CosmeticOrderCompleted {
		t.Fatalf("订单应 completed: err=%v status=%s", err, order.Status)
	}
	// points 端点
	points := decode[struct {
		Balance int64 `json:"balance"`
	}](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/cosmetics/points", user.AccessToken, nil))
	if points.Balance != 40 {
		t.Fatalf("points 端点余额应 40，实际 %d", points.Balance)
	}
}

func TestBundlePurchaseExpandsAndIdempotent(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())

	makePublished := func(name string, seed byte) itemViewT {
		it := env.createItem(t, admin.AccessToken, key, name, 20)
		env.uploadItemAsset(t, admin.AccessToken, it.ID, "primary", "image/png", staticPNG(seed))
		env.publishItem(t, admin.AccessToken, it.ID)
		return it
	}
	a := makePublished("包A", 0xA1)
	b := makePublished("包B", 0xA2)
	c := makePublished("包C", 0xA3)

	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/bundles", admin.AccessToken,
		map[string]any{"name": "三件套", "price_points": 45, "status": "published",
			"item_ids": []string{a.ID, b.ID, c.ID}})
	if r.Code != http.StatusCreated {
		t.Fatalf("建包失败: %d %s", r.Code, r.Body.String())
	}
	bundle := decode[bundleViewT](t, r)

	// 已拥有其中一件（admin grant）再买包 → 仍成功、只补缺
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/grant", admin.AccessToken,
		map[string]any{"user_id": user.User.ID.String(), "item_id": a.ID}); r.Code != http.StatusOK {
		t.Fatalf("预发放失败: %d %s", r.Code, r.Body.String())
	}
	env.givePoints(t, admin.AccessToken, user.User.ID, 45)
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/purchase", user.AccessToken,
		map[string]any{"target_type": "bundle", "target_id": bundle.ID}); r.Code != http.StatusOK {
		t.Fatalf("买包失败: %d %s", r.Code, r.Body.String())
	}
	var invCnt int64
	env.db.Model(&model.UserCosmeticInventory{}).Where("user_id = ?", user.User.ID).Count(&invCnt)
	if invCnt != 3 {
		t.Fatalf("买包后应拥有 3 件，实际 %d", invCnt)
	}
	var bundleSourced int64
	env.db.Model(&model.UserCosmeticInventory{}).
		Where("user_id = ? AND source = ?", user.User.ID, model.CosmeticSourceBundle).Count(&bundleSourced)
	if bundleSourced != 2 {
		t.Fatalf("bundle 来源应为补缺的 2 件，实际 %d", bundleSourced)
	}
	// 全拥有再买 → 409 ALREADY_OWNED
	env.givePoints(t, admin.AccessToken, user.User.ID, 45)
	if r := env.request(t, http.MethodPost, "/gapi/v1/users/@me/cosmetics/purchase", user.AccessToken,
		map[string]any{"target_type": "bundle", "target_id": bundle.ID}); r.Code != http.StatusConflict {
		t.Fatalf("全拥有买包应 409，实际 %d", r.Code)
	}
}

func TestAdminGrantPointsNegative(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	env.givePoints(t, admin.AccessToken, user.User.ID, 50)
	// 负数扣减超出余额 → INSUFFICIENT_POINTS
	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/points/grant", admin.AccessToken,
		map[string]any{"user_id": user.User.ID.String(), "amount": -80, "reason": "回收"})
	if r.Code == http.StatusOK {
		t.Fatalf("扣减超余额不应成功: %s", r.Body.String())
	}
	// 合法扣减
	r = env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/points/grant", admin.AccessToken,
		map[string]any{"user_id": user.User.ID.String(), "amount": -20, "reason": "回收"})
	if r.Code != http.StatusOK {
		t.Fatalf("合法扣减失败: %d %s", r.Code, r.Body.String())
	}
	got := decode[struct {
		Balance int64 `json:"balance"`
	}](t, r)
	if got.Balance != 30 {
		t.Fatalf("扣减后余额应 30，实际 %d", got.Balance)
	}
	_ = uuid.Nil
}
