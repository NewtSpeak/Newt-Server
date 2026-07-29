package guildapi_test

// 服务器多 banner 集成测试（服务器外观专项）：权限（MANAGE_GUILD / 系统管 /
// 非成员 404）、增删排序与连续编号、数量上限、类型与大小校验、跨服归属校验、
// 双平面挂载与公开资产 URL 前缀、GUILD_UPDATE 事件载荷。
// 需要真实 PostgreSQL（运行方式见 integration_test.go 头注释）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// pngBytes 携带 PNG 魔数的最小图片内容（http.DetectContentType 嗅探为 image/png）。
func pngBytes(payload int) []byte {
	head := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	return append(head, bytes.Repeat([]byte{0xAB}, payload)...)
}

// uploadImage multipart 上传 file 字段到指定路径，返回响应。
func (env *testEnv) uploadImage(t *testing.T, method, path, token string, content []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "banner.png")
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入 multipart 内容失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	parsed := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec, parsed
}

// bannersOf 从响应体解析 banners 数组的 (id, url, position) 三元组。
func bannersOf(t *testing.T, body map[string]any) []struct {
	ID       string
	URL      string
	Position int
} {
	t.Helper()
	raw, ok := body["banners"].([]any)
	if !ok {
		t.Fatalf("响应缺少 banners 数组: %v", body)
	}
	result := make([]struct {
		ID       string
		URL      string
		Position int
	}, 0, len(raw))
	for _, item := range raw {
		entry := item.(map[string]any)
		result = append(result, struct {
			ID       string
			URL      string
			Position int
		}{
			ID:       entry["id"].(string),
			URL:      entry["url"].(string),
			Position: int(entry["position"].(float64)),
		})
	}
	return result
}

// TestGuildBannersLifecycle 多 banner 主流程：上传两张 → 排序校验 → 重排序 →
// 删除一张 → 连续编号；GET 详情与 /banners 列表一致；GUILD_UPDATE 事件带 banners。
func TestGuildBannersLifecycle(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	guildID := env.createGuild(t, owner)
	base := fmt.Sprintf("/gapi/v1/guilds/%s/banners", guildID)

	// 上传两张（client 平面）。
	rec, body := env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(64))
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传 banner1 返回 %d: %s", rec.Code, rec.Body.String())
	}
	first := bannersOf(t, body)
	if len(first) != 1 || first[0].Position != 0 {
		t.Fatalf("首张 banner 期待 position=0，实得 %+v", first)
	}
	if !strings.HasPrefix(first[0].URL, "/public-assets/profile/") {
		t.Fatalf("banner URL 前缀错误: %s", first[0].URL)
	}
	rec, body = env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(128))
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传 banner2 返回 %d: %s", rec.Code, rec.Body.String())
	}
	banners := bannersOf(t, body)
	if len(banners) != 2 || banners[1].Position != 1 {
		t.Fatalf("第二张 banner 期待 position=1，实得 %+v", banners)
	}
	env.events.wait(t, "banner 新增 GUILD_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildPayload)
		return e.Type == eventbus.EventGuildUpdate && ok && payload.Guild.ID == guildID && len(payload.Banners) == 2
	})

	// GET /banners 与 guild 详情一致带 banners。
	rec, body = env.do(t, http.MethodGet, base, owner.Token, nil)
	if rec.Code != http.StatusOK || len(bannersOf(t, body)) != 2 {
		t.Fatalf("GET banners 返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, body = env.do(t, http.MethodGet, fmt.Sprintf("/gapi/v1/guilds/%s", guildID), owner.Token, nil)
	if rec.Code != http.StatusOK || len(bannersOf(t, body)) != 2 {
		t.Fatalf("guild 详情返回 %d 或缺 banners: %s", rec.Code, rec.Body.String())
	}
	// 我的服务器列表带 banners。
	rec, _ = env.do(t, http.MethodGet, "/gapi/v1/users/@me/guilds", owner.Token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"banners"`) {
		t.Fatalf("myGuilds 返回 %d 或缺 banners: %s", rec.Code, rec.Body.String())
	}

	// 重排序：倒序全量数组 → position 重排。
	rec, body = env.do(t, http.MethodPatch, base, owner.Token, map[string]any{
		"banner_ids": []string{banners[1].ID, banners[0].ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("重排序返回 %d: %s", rec.Code, rec.Body.String())
	}
	reordered := bannersOf(t, body)
	if reordered[0].ID != banners[1].ID || reordered[0].Position != 0 || reordered[1].Position != 1 {
		t.Fatalf("重排序结果错误: %+v", reordered)
	}

	// 非法重排序：缺一张 / 含外服 ID / 重复 → 400 BANNER_IDS_MISMATCH。
	for _, ids := range [][]string{
		{banners[0].ID},
		{banners[0].ID, uuid.NewString()},
		{banners[0].ID, banners[0].ID},
	} {
		rec, body = env.do(t, http.MethodPatch, base, owner.Token, map[string]any{"banner_ids": ids})
		if rec.Code != http.StatusBadRequest || errCode(body) != "BANNER_IDS_MISMATCH" {
			t.Fatalf("非法重排序 %v 返回 %d/%s，期待 400/BANNER_IDS_MISMATCH", ids, rec.Code, errCode(body))
		}
	}

	// 删除第一张（当前 position=0）→ 剩余重新编号为 0。
	rec, body = env.do(t, http.MethodDelete, base+"/"+reordered[0].ID, owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除 banner 返回 %d: %s", rec.Code, rec.Body.String())
	}
	remaining := bannersOf(t, body)
	if len(remaining) != 1 || remaining[0].Position != 0 || remaining[0].ID != reordered[1].ID {
		t.Fatalf("删除后剩余编号错误: %+v", remaining)
	}
	env.events.wait(t, "banner 删除 GUILD_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildPayload)
		return e.Type == eventbus.EventGuildUpdate && ok && payload.Guild.ID == guildID && len(payload.Banners) == 1
	})
}

// TestGuildBannersPermissions 权限矩阵：普通成员读 OK / 写 403；非成员 404；
// 系统管理员 client 平面无短路（404），后台平面短路可管理。
func TestGuildBannersPermissions(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	member := env.signup(t)
	outsider := env.signup(t)
	guildID := env.createGuild(t, owner)
	memberID := env.join(t, guildID, member)
	base := fmt.Sprintf("/gapi/v1/guilds/%s/banners", guildID)

	rec, _ := env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(32))
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner 上传 banner 返回 %d", rec.Code)
	}

	// 普通成员：读 200，写 403 MISSING_PERMISSION。
	rec, body := env.do(t, http.MethodGet, base, member.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("成员读 banners 返回 %d", rec.Code)
	}
	bannerID := bannersOf(t, body)[0].ID
	rec, body = env.uploadImage(t, http.MethodPost, base, member.Token, pngBytes(32))
	if rec.Code != http.StatusForbidden || errCode(body) != "MISSING_PERMISSION" {
		t.Fatalf("成员上传返回 %d/%s，期待 403/MISSING_PERMISSION", rec.Code, errCode(body))
	}
	rec, _ = env.do(t, http.MethodDelete, base+"/"+bannerID, member.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员删除返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodPatch, base, member.Token, map[string]any{"banner_ids": []string{bannerID}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员重排序返回 %d，期待 403", rec.Code)
	}

	// 拥有 MANAGE_GUILD 角色的成员可写。
	managerRoleID := env.createRole(t, owner, guildID, "外观管理", rbac.ManageGuild, 5)
	env.assignRole(t, owner, guildID, memberID, managerRoleID)
	rec, _ = env.uploadImage(t, http.MethodPost, base, member.Token, pngBytes(32))
	if rec.Code != http.StatusCreated {
		t.Fatalf("MANAGE_GUILD 成员上传返回 %d", rec.Code)
	}

	// 非成员：读写一律 404（防扫频）。
	rec, _ = env.do(t, http.MethodGet, base, outsider.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员读返回 %d，期待 404", rec.Code)
	}
	rec, _ = env.uploadImage(t, http.MethodPost, base, outsider.Token, pngBytes(32))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员上传返回 %d，期待 404", rec.Code)
	}

	// 系统管理员：client 平面同样保留 SystemAdmin 短路（docs 04 FR-32 / register.go），
	// 非成员也可管理；后台平面（aud=admin）同理。
	sysadmin := env.signup(t)
	if err := env.db.Model(&model.User{}).Where("id = ?", sysadmin.ID).Update("system_admin", true).Error; err != nil {
		t.Fatalf("提升系统管理员失败: %v", err)
	}
	rec, _ = env.uploadImage(t, http.MethodPost, base, sysadmin.Token, pngBytes(32))
	if rec.Code != http.StatusCreated {
		t.Fatalf("client 平面系统管上传返回 %d，期待 201", rec.Code)
	}
	adminToken, _, err := env.tokens.AccessToken(sysadmin.ID)
	if err != nil {
		t.Fatalf("签发后台 token 失败: %v", err)
	}
	adminBase := fmt.Sprintf("/api/v1/guilds/%s/banners", guildID)
	rec, body = env.uploadImage(t, http.MethodPost, adminBase, adminToken, pngBytes(32))
	if rec.Code != http.StatusCreated {
		t.Fatalf("后台平面系统管上传返回 %d: %s", rec.Code, rec.Body.String())
	}
	// 双平面响应中的图片 URL 均为平面中立的公开资产前缀。
	for _, entry := range bannersOf(t, body) {
		if !strings.HasPrefix(entry.URL, "/public-assets/profile/") {
			t.Fatalf("后台平面 banner URL 前缀错误: %s", entry.URL)
		}
	}
}

// TestGuildBannersValidationAndLimit 校验与上限：非图片 400、超大 400、
// 数量上限 400；删除他服 banner 404（归属校验）。
func TestGuildBannersValidationAndLimit(t *testing.T) {
	t.Setenv("GUILD_BANNER_MAX_COUNT", "2")
	env := newEnv(t)
	owner := env.signup(t)
	guildID := env.createGuild(t, owner)
	base := fmt.Sprintf("/gapi/v1/guilds/%s/banners", guildID)

	// 非图片内容（魔数嗅探不过）→ 400 UNSUPPORTED_TYPE。
	rec, body := env.uploadImage(t, http.MethodPost, base, owner.Token, []byte("plain text, not an image at all"))
	if rec.Code != http.StatusBadRequest || errCode(body) != "UNSUPPORTED_TYPE" {
		t.Fatalf("非图片上传返回 %d/%s，期待 400/UNSUPPORTED_TYPE", rec.Code, errCode(body))
	}
	// 超过 8MiB → 400 FILE_TOO_LARGE。
	rec, body = env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(8<<20))
	if rec.Code != http.StatusBadRequest || errCode(body) != "FILE_TOO_LARGE" {
		t.Fatalf("超大上传返回 %d/%s，期待 400/FILE_TOO_LARGE", rec.Code, errCode(body))
	}

	// 数量上限（env 覆盖为 2）：第三张 → 400 BANNER_LIMIT_REACHED。
	for i := 0; i < 2; i++ {
		rec, _ = env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(32))
		if rec.Code != http.StatusCreated {
			t.Fatalf("第 %d 张 banner 上传返回 %d", i+1, rec.Code)
		}
	}
	rec, body = env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(32))
	if rec.Code != http.StatusBadRequest || errCode(body) != "BANNER_LIMIT_REACHED" {
		t.Fatalf("超上限上传返回 %d/%s，期待 400/BANNER_LIMIT_REACHED", rec.Code, errCode(body))
	}

	// 跨服归属校验：B 服服主拿 A 服 banner ID 在自己服删 → 404。
	other := env.signup(t)
	otherGuildID := env.createGuild(t, other)
	rec, body = env.do(t, http.MethodGet, base, owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("读 banners 返回 %d", rec.Code)
	}
	victimBannerID := bannersOf(t, body)[0].ID
	rec, _ = env.do(t, http.MethodDelete,
		fmt.Sprintf("/gapi/v1/guilds/%s/banners/%s", otherGuildID, victimBannerID), other.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨服删除返回 %d，期待 404", rec.Code)
	}
	// A 服 banner 仍在。
	rec, body = env.do(t, http.MethodGet, base, owner.Token, nil)
	if rec.Code != http.StatusOK || len(bannersOf(t, body)) != 2 {
		t.Fatalf("跨服删除后 A 服 banner 丢失: %s", rec.Body.String())
	}
}

// TestGuildIconEndpoints 图标设置/清除冒烟（复用既有端点，确认与 banners 并存）。
func TestGuildIconEndpoints(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	guildID := env.createGuild(t, owner)
	base := fmt.Sprintf("/gapi/v1/guilds/%s/icon", guildID)

	rec, body := env.uploadImage(t, http.MethodPost, base, owner.Token, pngBytes(32))
	if rec.Code != http.StatusOK {
		t.Fatalf("上传图标返回 %d: %s", rec.Code, rec.Body.String())
	}
	url, _ := body["url"].(string)
	if !strings.HasPrefix(url, "/public-assets/profile/") {
		t.Fatalf("图标 URL 前缀错误: %s", url)
	}
	rec, body = env.do(t, http.MethodGet, fmt.Sprintf("/gapi/v1/guilds/%s", guildID), owner.Token, nil)
	guild := body["guild"].(map[string]any)
	if rec.Code != http.StatusOK || guild["icon_url"] != url {
		t.Fatalf("详情 icon_url 不一致: %v", guild["icon_url"])
	}
	rec, _ = env.do(t, http.MethodDelete, base, owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("清除图标返回 %d", rec.Code)
	}
	rec, body = env.do(t, http.MethodGet, fmt.Sprintf("/gapi/v1/guilds/%s", guildID), owner.Token, nil)
	guild = body["guild"].(map[string]any)
	if rec.Code != http.StatusOK || guild["icon_url"] != "" {
		t.Fatalf("清除后 icon_url 未清空: %v", guild["icon_url"])
	}
}
