package cosmetics_test

// 目录管理集成测试：品类 schema 校验、item CRUD 与发布校验、标签、捆绑包、
// 目录变更事件（bus 层断言；gateway 全站广播另在 internal/gateway 单测覆盖）。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/newtspeak/newt-server/backend/internal/eventbus"
)

func TestCategoryCreateAndSchemaValidation(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	user := env.signup(t)
	// 后台平面鉴权要求 aud=admin；非 system_admin 的 admin 受众 token 才走到 403。
	userAdminToken := env.adminAudienceToken(t, user.User.ID)

	key := newUniqueKey("cat")
	// 非 admin 建品类 → 403
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/categories", userAdminToken,
		map[string]any{"key": key, "name": "x"}); r.Code != http.StatusForbidden {
		t.Fatalf("非管理员建品类应 403，实际 %d", r.Code)
	}
	// 非法 mime_group → 400 INVALID_SCHEMA
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/categories", admin.AccessToken,
		map[string]any{"key": key, "name": "x", "schema": map[string]any{
			"asset_slots": []map[string]any{{"key": "a", "mime_groups": []string{"bogus"}}},
		}}); r.Code != http.StatusBadRequest {
		t.Fatalf("非法 mime_group 应 400，实际 %d %s", r.Code, r.Body.String())
	}
	// 重复 slot key → 400
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/categories", admin.AccessToken,
		map[string]any{"key": key, "name": "x", "schema": map[string]any{
			"asset_slots": []map[string]any{
				{"key": "a", "mime_groups": []string{"image"}},
				{"key": "a", "mime_groups": []string{"image"}},
			},
		}}); r.Code != http.StatusBadRequest {
		t.Fatalf("重复 slot key 应 400，实际 %d", r.Code)
	}
	// 合法创建 → 201 且发布 category_create 目录事件
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	env.events.wait(t, "category_create 目录事件", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventCosmeticCatalogUpdate
	})
	// 重复 key → 409
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/categories", admin.AccessToken,
		map[string]any{"key": key, "name": "x"}); r.Code != http.StatusConflict {
		t.Fatalf("重复 key 应 409，实际 %d", r.Code)
	}
	// 用户端品类列表可见（enabled=true）
	list := decode[struct {
		Categories []struct {
			Key string `json:"key"`
		} `json:"categories"`
	}](t, env.request(t, http.MethodGet, "/gapi/v1/cosmetics/categories", user.AccessToken, nil))
	found := false
	for _, c := range list.Categories {
		if c.Key == key {
			found = true
		}
	}
	if !found {
		t.Fatal("新品类应出现在用户端品类列表")
	}
	// 停用后用户端不可见
	if r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/categories/"+key, admin.AccessToken,
		map[string]any{"enabled": false}); r.Code != http.StatusOK {
		t.Fatalf("停用品类失败: %d", r.Code)
	}
	list2 := decode[struct {
		Categories []struct {
			Key string `json:"key"`
		} `json:"categories"`
	}](t, env.request(t, http.MethodGet, "/gapi/v1/cosmetics/categories", user.AccessToken, nil))
	for _, c := range list2.Categories {
		if c.Key == key {
			t.Fatal("停用品类不应出现在用户端列表")
		}
	}
}

func TestItemCRUDAndPublishValidation(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())

	// 负价格 → 400
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/items", admin.AccessToken,
		map[string]any{"category_key": key, "name": "x", "price_points": -1}); r.Code != http.StatusBadRequest {
		t.Fatalf("负价格应 400，实际 %d", r.Code)
	}
	// 不存在的品类 → 400
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/items", admin.AccessToken,
		map[string]any{"category_key": "no-such-cat", "name": "x"}); r.Code != http.StatusBadRequest {
		t.Fatalf("未知品类应 400，实际 %d", r.Code)
	}
	// 创建默认 draft
	item := env.createItem(t, admin.AccessToken, key, "测试单品", 50)
	if item.Status != "draft" {
		t.Fatalf("新建商品应为 draft，实际 %s", item.Status)
	}
	// 必填资产缺失时发布 → 400 INCOMPLETE_ASSETS
	r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/items/"+item.ID, admin.AccessToken,
		map[string]any{"status": "published"})
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "INCOMPLETE_ASSETS") {
		t.Fatalf("缺资产发布应 INCOMPLETE_ASSETS，实际 %d %s", r.Code, r.Body.String())
	}
	// 传资产后发布成功
	env.uploadItemAsset(t, admin.AccessToken, item.ID, "primary", "image/png", staticPNG(0x11))
	env.publishItem(t, admin.AccessToken, item.ID)
	got := decode[itemViewT](t, env.request(t, http.MethodGet,
		"/api/v1/admin/cosmetics/items/"+item.ID, admin.AccessToken, nil))
	if got.Status != "published" {
		t.Fatalf("发布后应为 published，实际 %s", got.Status)
	}
}

func TestBundleCRUD(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	a := env.createItem(t, admin.AccessToken, key, "包内A", 10)
	b := env.createItem(t, admin.AccessToken, key, "包内B", 10)

	// 含不存在成员 → 400
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/bundles", admin.AccessToken,
		map[string]any{"name": "坏包", "item_ids": []string{"999999999999"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("不存在成员应 400，实际 %d %s", r.Code, r.Body.String())
	}
	// 创建
	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/bundles", admin.AccessToken,
		map[string]any{"name": "两件套", "price_points": 15, "item_ids": []string{a.ID, b.ID}})
	if r.Code != http.StatusCreated {
		t.Fatalf("建捆绑包失败: %d %s", r.Code, r.Body.String())
	}
	bundle := decode[bundleViewT](t, r)
	// PATCH item_ids 全量替换
	if r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/bundles/"+bundle.ID, admin.AccessToken,
		map[string]any{"item_ids": []string{a.ID}}); r.Code != http.StatusOK {
		t.Fatalf("改捆绑包失败: %d %s", r.Code, r.Body.String())
	}
	got := decode[bundleViewT](t, env.request(t, http.MethodGet,
		"/api/v1/admin/cosmetics/bundles/"+bundle.ID, admin.AccessToken, nil))
	if len(got.ItemIDs) != 1 || got.ItemIDs[0] != a.ID {
		t.Fatalf("捆绑成员应替换为 [%s]，实际 %v", a.ID, got.ItemIDs)
	}
}

func TestTagCRUDAndCascade(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	item := env.createItem(t, admin.AccessToken, key, "打标商品", 0)

	// 非法 key → 400
	if r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/tags", admin.AccessToken,
		map[string]any{"key": "Bad_Key!", "name": "x"}); r.Code != http.StatusBadRequest {
		t.Fatalf("非法 tag key 应 400，实际 %d", r.Code)
	}
	tagKey := newUniqueKey("tag")
	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/tags", admin.AccessToken,
		map[string]any{"key": tagKey, "name": "主题", "color": "#FF8800"})
	if r.Code != http.StatusCreated {
		t.Fatalf("建标签失败: %d %s", r.Code, r.Body.String())
	}
	tag := decode[struct {
		ID string `json:"id"`
	}](t, r)
	// 打标
	if r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/items/"+item.ID, admin.AccessToken,
		map[string]any{"tag_ids": []string{tag.ID}}); r.Code != http.StatusOK {
		t.Fatalf("打标失败: %d", r.Code)
	}
	// 删除标签级联清理关联
	if r := env.request(t, http.MethodDelete, "/api/v1/admin/cosmetics/tags/"+tag.ID, admin.AccessToken, nil); r.Code != http.StatusNoContent {
		t.Fatalf("删标签应 204，实际 %d", r.Code)
	}
	got := decode[struct {
		Tags []any `json:"tags"`
	}](t, env.request(t, http.MethodGet, "/api/v1/admin/cosmetics/items/"+item.ID, admin.AccessToken, nil))
	if len(got.Tags) != 0 {
		t.Fatalf("删除标签后商品不应再带该标签，实际 %d 个", len(got.Tags))
	}
}
