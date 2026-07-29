package cosmetics_test

// 装扮系统集成测试基建：需要真实 PostgreSQL（与 userapi/guildapi 集成测试同约定）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/cosmetics/
//
// 未设置 TEST_DATABASE_URL 时自动跳过。装配复现生产：
// 后台 /api/v1（httpapi + cosmetics.Register）+ 用户端 /gapi/v1（clientapi 全量，
// 内含 cosmetics.RegisterClient）+ /public-assets（cosmetics.RegisterPublic）。

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/clientapi"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/cosmetics"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/httpapi"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testSecret = "cosmetics-integration-secret32ch"

type eventCollector struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (ec *eventCollector) handle(event eventbus.Event) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.events = append(ec.events, event)
}

func (ec *eventCollector) wait(t *testing.T, description string, match func(eventbus.Event) bool) eventbus.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ec.mu.Lock()
		for _, event := range ec.events {
			if match(event) {
				ec.mu.Unlock()
				return event
			}
		}
		ec.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待事件超时: %s", description)
	return eventbus.Event{}
}

type testEnv struct {
	router  *gin.Engine
	db      *gorm.DB
	events  *eventCollector
	dataDir string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	cfg := config.Config{JWTSecret: testSecret, AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, DataDir: dataDir}
	bus := eventbus.New()
	collector := &eventCollector{}
	bus.Subscribe(collector.handle)

	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	api.AttachEventBus(bus)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	deps := appdeps.Deps{DB: db, Bus: bus, Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := cosmetics.Register(v1, deps); err != nil {
		t.Fatalf("挂载后台 cosmetics 失败: %v", err)
	}
	if err := cosmetics.RegisterPublic(router.Group("/public-assets"), deps); err != nil {
		t.Fatalf("挂载公开资产失败: %v", err)
	}
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("挂载用户端 API 失败: %v", err)
	}
	return &testEnv{router: router, db: db, events: collector, dataDir: dataDir}
}

func (env *testEnv) request(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, req)
	return recorder
}

// uploadRaw 二进制上传（raw body PUT，与管理台上传路径一致）。
func (env *testEnv) uploadRaw(t *testing.T, method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, req)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v (%s)", err, recorder.Body.String())
	}
	return result
}

type tokenPair struct {
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	User         model.User `json:"user"`
}

func (env *testEnv) signup(t *testing.T) tokenPair {
	t.Helper()
	name := fmt.Sprintf("c%d%04d", time.Now().UnixNano()%1e10, rand.Intn(10000))
	recorder := env.request(t, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": name, "email": name + "@test.local", "password": "password-123",
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("注册失败: %d %s", recorder.Code, recorder.Body.String())
	}
	return decode[tokenPair](t, recorder)
}

// signupAdmin 注册普通用户后提升为 system_admin，再经后台 /api/v1/auth/login
// 换取 aud=admin 的 token（用户端 gapi 签发的 aud=client 不能打后台 API）。
func (env *testEnv) signupAdmin(t *testing.T) tokenPair {
	t.Helper()
	pair := env.signup(t)
	if err := env.db.Model(&model.User{}).Where("id = ?", pair.User.ID).Update("system_admin", true).Error; err != nil {
		t.Fatalf("提升管理员失败: %v", err)
	}
	recorder := env.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"identifier": pair.User.Username,
		"password":   "password-123",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("管理员后台登录失败: %d %s", recorder.Code, recorder.Body.String())
	}
	return decode[tokenPair](t, recorder)
}

// adminAudienceToken 为已有用户签发 aud=admin 的 access token（不提升 system_admin）。
// 用于断言「已认证但非管理员」路径返回 403（client token 打后台会先 401）。
func (env *testEnv) adminAudienceToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	tokens := security.NewTokenManager(testSecret, time.Minute)
	access, _, err := tokens.AccessTokenWithAudience(userID, security.AudienceAdmin)
	if err != nil {
		t.Fatalf("签发 admin 受众 token 失败: %v", err)
	}
	return access
}

// joinGuild 直接落库建 guild 与成员关系。
func (env *testEnv) joinGuild(t *testing.T, guildID uuid.UUID, userIDs ...uuid.UUID) {
	t.Helper()
	guild := model.Guild{ID: guildID, Name: "g-" + guildID.String()[:8], OwnerUserID: userIDs[0]}
	if err := env.db.Create(&guild).Error; err != nil {
		t.Fatalf("建服失败: %v", err)
	}
	for _, userID := range userIDs {
		member := model.Member{ID: uuid.New(), GuildID: guildID, UserID: userID}
		if err := env.db.Create(&member).Error; err != nil {
			t.Fatalf("加成员失败: %v", err)
		}
	}
}

// givePoints 走管理端发放积分（顺带覆盖该端点）。
func (env *testEnv) givePoints(t *testing.T, adminToken string, userID uuid.UUID, amount int64) {
	t.Helper()
	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/points/grant", adminToken, map[string]any{
		"user_id": userID.String(), "amount": amount, "reason": "test_grant",
	})
	if r.Code != http.StatusOK {
		t.Fatalf("发放积分失败: %d %s", r.Code, r.Body.String())
	}
}

// ---- 视图解码结构（与后端 views.go 对齐，只声明测试用到的字段）----

type assetViewT struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	MIME     string `json:"mime"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Animated bool   `json:"animated"`
}

type itemViewT struct {
	ID          string                `json:"id"`
	CategoryKey string                `json:"category_key"`
	Slot        string                `json:"slot"`
	Name        string                `json:"name"`
	PreviewURL  string                `json:"preview_url"`
	Assets      map[string]assetViewT `json:"assets"`
	PricePoints int                   `json:"price_points"`
	Status      string                `json:"status"`
	Owned       *bool                 `json:"owned"`
}

type bundleViewT struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	PricePoints int      `json:"price_points"`
	ItemIDs     []string `json:"item_ids"`
	OwnedAll    *bool    `json:"owned_all"`
}

type shopViewT struct {
	Items   []itemViewT   `json:"items"`
	Bundles []bundleViewT `json:"bundles"`
}

type loadoutViewT struct {
	Slots map[string]struct {
		ItemID      string                `json:"item_id"`
		CategoryKey string                `json:"category_key"`
		Slot        string                `json:"slot"`
		Assets      map[string]assetViewT `json:"assets"`
	} `json:"slots"`
}

// ---- 测试用最小媒体字节 ----

type pngChunkT struct {
	typ     string
	payload []byte
}

func buildPNGBytes(chunks ...pngChunkT) []byte {
	out := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	for _, ch := range chunks {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ch.payload)))
		out = append(out, lenBuf[:]...)
		out = append(out, ch.typ...)
		out = append(out, ch.payload...)
		out = append(out, 0, 0, 0, 0) // CRC 占位（嗅探不校验）
	}
	return out
}

// staticPNG 结构合法的最小静态 PNG（seed 保证内容 hash 各测试互不相同）。
func staticPNG(seed byte) []byte {
	ihdr := make([]byte, 13)
	ihdr[3] = 1 // width=1
	ihdr[7] = 1 // height=1
	return buildPNGBytes(pngChunkT{"IHDR", ihdr}, pngChunkT{"IDAT", []byte{seed, 0x01}}, pngChunkT{"IEND", nil})
}

// apng 含 acTL（IDAT 前）的动图 PNG。
func apng(seed byte) []byte {
	ihdr := make([]byte, 13)
	ihdr[3] = 1
	ihdr[7] = 1
	return buildPNGBytes(pngChunkT{"IHDR", ihdr}, pngChunkT{"acTL", make([]byte, 8)},
		pngChunkT{"IDAT", []byte{seed, 0x02}}, pngChunkT{"IEND", nil})
}

// trapPNG 静态 PNG，但 IDAT 像素流中含 "acTL" 字样（字节巧合误报回归）。
func trapPNG(seed byte) []byte {
	ihdr := make([]byte, 13)
	ihdr[3] = 1
	ihdr[7] = 1
	return buildPNGBytes(pngChunkT{"IHDR", ihdr}, pngChunkT{"IDAT", append([]byte("xxacTLxx"), seed)}, pngChunkT{"IEND", nil})
}

func gifBytes(seed byte) []byte {
	return append([]byte("GIF89a"), 0x01, 0x00, 0x01, 0x00, 0x00, seed)
}

func animatedWebP(seed byte) []byte {
	out := []byte("RIFF")
	out = append(out, 0x20, 0, 0, 0)
	out = append(out, "WEBP"...)
	out = append(out, "VP8X"...)
	out = append(out, 10, 0, 0, 0)
	out = append(out, 0x02)          // animation 位
	out = append(out, 0, 0, 0)       // 保留
	out = append(out, 0x1F, 0, 0)    // width-1
	out = append(out, 0x3F, 0, 0)    // height-1
	out = append(out, "ANIM"...)
	out = append(out, 6, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	return append(out, seed)
}

func staticWebP(seed byte) []byte {
	out := []byte("RIFF")
	out = append(out, 0x14, 0, 0, 0)
	out = append(out, "WEBP"...)
	out = append(out, "VP8 "...)
	out = append(out, 4, 0, 0, 0)
	out = append(out, 0, 0, 0, seed)
	return out
}

func mp4Bytes(seed byte) []byte {
	out := []byte{0, 0, 0, 0x18}
	out = append(out, "ftyp"...)
	out = append(out, "isom"...)
	return append(out, 0, 0, 0, 1, seed)
}

func oggBytes(seed byte) []byte {
	return append([]byte("OggS"), 0, 0, 0, 0, seed)
}

// ---- 管理端造数 helper ----

func (env *testEnv) createCategory(t *testing.T, adminToken, key string, schema map[string]any) {
	t.Helper()
	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/categories", adminToken, map[string]any{
		"key": key, "name": "测试品类-" + key, "schema": schema,
	})
	if r.Code != http.StatusCreated {
		t.Fatalf("建品类失败: %d %s", r.Code, r.Body.String())
	}
}

// simpleImageSchema 单个必填图片槽（primary，接受静图与动图）。
func simpleImageSchema() map[string]any {
	return map[string]any{
		"asset_slots": []map[string]any{
			{"key": "primary", "required": true, "mime_groups": []string{"image", "animated_image"}},
		},
		"render_hint": "avatar_frame",
	}
}

func (env *testEnv) createItem(t *testing.T, adminToken, categoryKey, name string, price int) itemViewT {
	t.Helper()
	r := env.request(t, http.MethodPost, "/api/v1/admin/cosmetics/items", adminToken, map[string]any{
		"category_key": categoryKey, "name": name, "price_points": price,
	})
	if r.Code != http.StatusCreated {
		t.Fatalf("建商品失败: %d %s", r.Code, r.Body.String())
	}
	return decode[itemViewT](t, r)
}

func (env *testEnv) uploadItemAsset(t *testing.T, adminToken, itemID, slot, contentType string, data []byte) itemViewT {
	t.Helper()
	r := env.uploadRaw(t, http.MethodPut,
		"/api/v1/admin/cosmetics/items/"+itemID+"/assets/"+slot, adminToken, contentType, data)
	if r.Code != http.StatusOK {
		t.Fatalf("上传资产失败: %d %s", r.Code, r.Body.String())
	}
	return decode[itemViewT](t, r)
}

func (env *testEnv) publishItem(t *testing.T, adminToken, itemID string) {
	t.Helper()
	r := env.request(t, http.MethodPatch, "/api/v1/admin/cosmetics/items/"+itemID, adminToken,
		map[string]any{"status": "published"})
	if r.Code != http.StatusOK {
		t.Fatalf("发布商品失败: %d %s", r.Code, r.Body.String())
	}
}

// newUniqueKey 品类/标签 key 隔离（不清库，靠随机 key 与本测 id 断言隔离）。
func newUniqueKey(prefix string) string {
	return fmt.Sprintf("%s-%d%04d", prefix, time.Now().UnixNano()%1e10, rand.Intn(10000))
}

// assetByURL 根据视图 URL 找资产行（断言 ref_count / animated 用）。
func (env *testEnv) assetByID(t *testing.T, id string) model.CosmeticAsset {
	t.Helper()
	var row model.CosmeticAsset
	if err := env.db.First(&row, "id = ?", id).Error; err != nil {
		t.Fatalf("查资产 %s 失败: %v", id, err)
	}
	return row
}
