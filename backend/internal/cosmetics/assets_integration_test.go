package cosmetics_test

// 资产集成测试：MIME 嗅探与拒绝路径（ref_count 不泄漏）、内容去重、
// 槽位替换释放、同内容重传不虚增、preview 跟随、动图判定、资产回源。
// Bug 2 / Bug 3 的回归核心。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/owlspeak/owl-server/backend/internal/model"
)

// audioVisualSchema 视觉槽 + 音频槽（音频槽仅接受 audio 组）。
func audioVisualSchema() map[string]any {
	return map[string]any{
		"asset_slots": []map[string]any{
			{"key": "visual", "required": true, "mime_groups": []string{"image", "animated_image", "video"}},
			{"key": "audio", "required": false, "mime_groups": []string{"audio"}},
		},
		"render_hint": "profile_effect",
	}
}

func TestAssetUploadValidationNoRefLeak(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, audioVisualSchema())
	item := env.createItem(t, admin.AccessToken, key, "校验商品", 0)

	var before int64
	env.db.Model(&model.CosmeticAsset{}).Count(&before)

	// 未知槽 → 400
	if r := env.uploadRaw(t, http.MethodPut,
		"/api/v1/admin/cosmetics/items/"+item.ID+"/assets/nope", admin.AccessToken,
		"image/png", staticPNG(0x21)); r.Code != http.StatusBadRequest {
		t.Fatalf("未知槽应 400，实际 %d", r.Code)
	}
	// 音频槽传 PNG → 400 MIME_NOT_ALLOWED，且不产生任何资产行/引用（Bug 2 泄漏点回归）
	r := env.uploadRaw(t, http.MethodPut,
		"/api/v1/admin/cosmetics/items/"+item.ID+"/assets/audio", admin.AccessToken,
		"image/png", staticPNG(0x22))
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "MIME_NOT_ALLOWED") {
		t.Fatalf("音频槽传图应 MIME_NOT_ALLOWED，实际 %d %s", r.Code, r.Body.String())
	}
	var after int64
	env.db.Model(&model.CosmeticAsset{}).Count(&after)
	if after != before {
		t.Fatalf("被拒上传不应创建资产行: before=%d after=%d", before, after)
	}
	// 音频槽正常传 OGG
	v := env.uploadItemAsset(t, admin.AccessToken, item.ID, "audio", "audio/ogg", oggBytes(0x23))
	if v.Assets["audio"].MIME != "audio/ogg" {
		t.Fatalf("音频资产 MIME 应为 audio/ogg，实际 %s", v.Assets["audio"].MIME)
	}
}

func TestAssetDedupAndReleaseOnReplace(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	itemA := env.createItem(t, admin.AccessToken, key, "去重A", 0)
	itemB := env.createItem(t, admin.AccessToken, key, "去重B", 0)

	shared := staticPNG(0x31)
	vA := env.uploadItemAsset(t, admin.AccessToken, itemA.ID, "primary", "image/png", shared)
	vB := env.uploadItemAsset(t, admin.AccessToken, itemB.ID, "primary", "image/png", shared)
	if vA.Assets["primary"].ID != vB.Assets["primary"].ID {
		t.Fatal("同内容上传应命中同一资产（内容去重）")
	}
	sharedID := vA.Assets["primary"].ID
	if rc := env.assetByID(t, sharedID).RefCount; rc != 2 {
		t.Fatalf("两个引用者时 ref_count 应为 2，实际 %d", rc)
	}

	// 同内容重传同槽位：净引用不虚增
	env.uploadItemAsset(t, admin.AccessToken, itemA.ID, "primary", "image/png", shared)
	if rc := env.assetByID(t, sharedID).RefCount; rc != 2 {
		t.Fatalf("同内容重传后 ref_count 仍应为 2，实际 %d", rc)
	}

	// itemA 换新图：共享资产 ref_count 降为 1（itemB 仍引用）
	replacement := staticPNG(0x32)
	vA2 := env.uploadItemAsset(t, admin.AccessToken, itemA.ID, "primary", "image/png", replacement)
	if rc := env.assetByID(t, sharedID).RefCount; rc != 1 {
		t.Fatalf("替换后共享资产 ref_count 应为 1，实际 %d", rc)
	}
	newID := vA2.Assets["primary"].ID
	// preview 跟随：itemA 的 preview 原指旧资产，替换后应指向新资产
	if vA2.PreviewURL == "" {
		t.Fatal("替换后 preview 不应为空")
	}
	var row model.CosmeticItem
	if err := env.db.First(&row, "id = ?", itemA.ID).Error; err != nil {
		t.Fatalf("读商品失败: %v", err)
	}
	if row.PreviewAssetID == nil || vA2.Assets["primary"].ID != newID {
		t.Fatal("preview_asset_id 应存在")
	}

	// itemB 也换图：旧共享资产 ref_count 归零（延迟 GC：行与文件保留）
	env.uploadItemAsset(t, admin.AccessToken, itemB.ID, "primary", "image/png", staticPNG(0x33))
	old := env.assetByID(t, sharedID)
	if old.RefCount != 0 {
		t.Fatalf("无人引用后 ref_count 应为 0（延迟 GC），实际 %d", old.RefCount)
	}
}

func TestAssetAnimatedDetection(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())

	cases := []struct {
		name     string
		ct       string
		data     []byte
		mime     string
		animated bool
	}{
		{"APNG 判定为动图", "image/png", apng(0x41), "image/png", true},
		{"普通 PNG 静图", "image/png", staticPNG(0x42), "image/png", false},
		{"IDAT 含 acTL 字节不误报", "image/png", trapPNG(0x43), "image/png", false},
		{"动态 WebP（VP8X animation 位）", "image/webp", animatedWebP(0x44), "image/webp", true},
		{"静态 WebP", "image/webp", staticWebP(0x45), "image/webp", false},
		{"GIF 恒为动图", "image/gif", gifBytes(0x46), "image/gif", true},
	}
	for _, tc := range cases {
		item := env.createItem(t, admin.AccessToken, key, "动图判定-"+tc.name, 0)
		v := env.uploadItemAsset(t, admin.AccessToken, item.ID, "primary", tc.ct, tc.data)
		got := v.Assets["primary"]
		if got.MIME != tc.mime || got.Animated != tc.animated {
			t.Fatalf("%s: mime=%s animated=%v，期待 mime=%s animated=%v",
				tc.name, got.MIME, got.Animated, tc.mime, tc.animated)
		}
	}
}

func TestAssetServeAndPathTraversal(t *testing.T) {
	env := newEnv(t)
	admin := env.signupAdmin(t)
	key := newUniqueKey("cat")
	env.createCategory(t, admin.AccessToken, key, simpleImageSchema())
	item := env.createItem(t, admin.AccessToken, key, "回源商品", 0)
	v := env.uploadItemAsset(t, admin.AccessToken, item.ID, "primary", "image/png", staticPNG(0x51))

	url := v.Assets["primary"].URL // /public-assets/cosmetics/{hash}.png
	r := env.uploadRaw(t, http.MethodGet, url, "", "", nil)
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("资产回源失败: %d ct=%s", r.Code, r.Header().Get("Content-Type"))
	}
	if cc := r.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("资产应 immutable 缓存，实际 %s", cc)
	}
	// 路径穿越 → 404
	for _, bad := range []string{"/public-assets/cosmetics/..%2fsecret.png", "/public-assets/cosmetics/no-such.png", "/public-assets/cosmetics/x.exe"} {
		if r := env.uploadRaw(t, http.MethodGet, bad, "", "", nil); r.Code != http.StatusNotFound {
			t.Fatalf("%s 应 404，实际 %d", bad, r.Code)
		}
	}
}
