package message_test

// 语音包完整模型 HTTP 集成测试（docs 12）：CRUD 权限、音频上传（大小/魔数）、
// 用户选包授权（STANDARD/RARE）、公开资产访问。需要真实 PostgreSQL，默认跳过
//（运行方式见 client_integration_test.go 文件头）。

import (
	"bytes"
	"fmt"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/clientapi"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/httpapi"
	"github.com/newtspeak/newt-server/backend/internal/message"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newVoicePackRouter 与 newTextRouter 同拓扑，另挂 /public-assets/voicepacks 公开资产路由。
func newVoicePackRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
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
	cfg := config.Config{
		JWTSecret:       "integration-secret-integration-32",
		AccessTokenTTL:  time.Minute,
		RefreshTokenTTL: time.Hour,
		DataDir:         t.TempDir(),
	}
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	deps := appdeps.Deps{DB: db, Bus: eventbus.New(), Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := message.Register(v1, deps); err != nil {
		t.Fatalf("挂载后台消息 API 失败: %v", err)
	}
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("挂载用户端 API 失败: %v", err)
	}
	if err := message.RegisterPublicAssets(router.Group("/public-assets"), deps); err != nil {
		t.Fatalf("挂载公开资产路由失败: %v", err)
	}
	return router, db
}

// signupUser 注册一个用户，返回 access token 与用户 ID。
func signupUser(t *testing.T, router *gin.Engine, db *gorm.DB, prefix string) (string, uuid.UUID) {
	t.Helper()
	username := fmt.Sprintf("%s_%08x", prefix, rand.Uint32())
	rec, body := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": username, "email": username + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	var user model.User
	if err := db.First(&user, "username = ?", username).Error; err != nil {
		t.Fatalf("查注册用户失败: %v", err)
	}
	return body["access_token"].(string), user.ID
}

// voicePackFixture 服主 + 普通成员 + guild 的标准夹具。
type voicePackFixture struct {
	router      *gin.Engine
	db          *gorm.DB
	guildID     uuid.UUID
	ownerToken  string
	ownerID     uuid.UUID
	memberToken string
	memberID    uuid.UUID
}

func newVoicePackFixture(t *testing.T) *voicePackFixture {
	t.Helper()
	router, db := newVoicePackRouter(t)
	f := &voicePackFixture{router: router, db: db}
	f.ownerToken, f.ownerID = signupUser(t, router, db, "vp_owner")
	rec, guild := doJSONReq(t, router, http.MethodPost, "/gapi/v1/guilds", f.ownerToken, map[string]string{
		"name": "语音包集成服 " + uuid.NewString()[:8],
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建服返回 %d: %s", rec.Code, rec.Body.String())
	}
	var err error
	if f.guildID, err = uuid.Parse(guild["id"].(string)); err != nil {
		t.Fatalf("解析 guild id 失败: %v", err)
	}
	f.memberToken, f.memberID = signupUser(t, router, db, "vp_member")
	if err := db.Create(&model.Member{ID: uuid.New(), GuildID: f.guildID, UserID: f.memberID}).Error; err != nil {
		t.Fatalf("插入成员失败: %v", err)
	}
	return f
}

func (f *voicePackFixture) packsBase() string {
	return "/gapi/v1/guilds/" + f.guildID.String() + "/voice-packs"
}

// createPack 服主建包，返回 pack id。
func (f *voicePackFixture) createPack(t *testing.T, body map[string]any) string {
	t.Helper()
	rec, pack := doJSONReq(t, f.router, http.MethodPost, f.packsBase(), f.ownerToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建包返回 %d: %s", rec.Code, rec.Body.String())
	}
	return pack["id"].(string)
}

// uploadAudio 上传音频（multipart file 字段）。
func uploadAudio(t *testing.T, router *gin.Engine, path, token string, content []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "pack.bin")
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写 multipart 失败: %v", err)
	}
	_ = writer.WriteField("duration_ms", "2300")
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	return doReq(t, router, http.MethodPost, path, token, buf.Bytes(), writer.FormDataContentType())
}

func fakeOgg(size int) []byte {
	content := append([]byte("OggS"), make([]byte, size-4)...)
	return content
}

// TestVoicePackCRUDAndPermissions 包 CRUD：服管专属写操作、成员只读列表、角色校验。
func TestVoicePackCRUDAndPermissions(t *testing.T) {
	f := newVoicePackFixture(t)

	// 普通成员建包：403（需 MANAGE_GUILD）。
	rec, _ := doJSONReq(t, f.router, http.MethodPost, f.packsBase(), f.memberToken, map[string]any{"name": "越权包"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员建包返回 %d，期望 403", rec.Code)
	}
	// 跨服角色 ID：400。
	rec, _ = doJSONReq(t, f.router, http.MethodPost, f.packsBase(), f.ownerToken, map[string]any{
		"name": "坏角色包", "kind": "RARE", "allowed_role_ids": []string{uuid.NewString()},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("跨服角色建包返回 %d，期望 400", rec.Code)
	}
	// 非法 kind：400。
	rec, _ = doJSONReq(t, f.router, http.MethodPost, f.packsBase(), f.ownerToken, map[string]any{
		"name": "坏类型", "kind": "LEGENDARY",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 kind 返回 %d，期望 400", rec.Code)
	}

	packID := f.createPack(t, map[string]any{"name": "普通包"})

	// PATCH：成员 403，服主 200。
	rec, _ = doJSONReq(t, f.router, http.MethodPatch, f.packsBase()+"/"+packID, f.memberToken, map[string]any{"name": "改名"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员改包返回 %d，期望 403", rec.Code)
	}
	rec, updated := doJSONReq(t, f.router, http.MethodPatch, f.packsBase()+"/"+packID, f.ownerToken, map[string]any{
		"name": "改名后", "enabled": false,
	})
	if rec.Code != http.StatusOK || updated["name"] != "改名后" || updated["enabled"] != false {
		t.Fatalf("服主改包异常: %d %s", rec.Code, rec.Body.String())
	}

	// 列表：成员看不到停用包，服主可见。
	rec, list := doJSONReq(t, f.router, http.MethodGet, f.packsBase(), f.memberToken, nil)
	if rec.Code != http.StatusOK || len(list["voice_packs"].([]any)) != 0 {
		t.Fatalf("成员应看不到停用包: %d %s", rec.Code, rec.Body.String())
	}
	rec, list = doJSONReq(t, f.router, http.MethodGet, f.packsBase(), f.ownerToken, nil)
	if rec.Code != http.StatusOK || len(list["voice_packs"].([]any)) != 1 {
		t.Fatalf("服主应看到停用包: %d %s", rec.Code, rec.Body.String())
	}

	// DELETE：成员 403，服主 200，二次删除 404。
	rec, _ = doJSONReq(t, f.router, http.MethodDelete, f.packsBase()+"/"+packID, f.memberToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员删包返回 %d，期望 403", rec.Code)
	}
	rec, _ = doJSONReq(t, f.router, http.MethodDelete, f.packsBase()+"/"+packID, f.ownerToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("服主删包返回 %d", rec.Code)
	}
	rec, _ = doJSONReq(t, f.router, http.MethodDelete, f.packsBase()+"/"+packID, f.ownerToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("重复删包返回 %d，期望 404", rec.Code)
	}
}

// TestVoicePackAudioUpload 音频上传：魔数嗅探（OGG/MP3 放行、PNG 拒收）、500KB 上限、
// 公开资产 URL 可无鉴权访问。
func TestVoicePackAudioUpload(t *testing.T) {
	f := newVoicePackFixture(t)
	packID := f.createPack(t, map[string]any{"name": "上传测试包"})
	audioPath := f.packsBase() + "/" + packID + "/audio"

	// 成员上传：403。
	rec, _ := uploadAudio(t, f.router, audioPath, f.memberToken, fakeOgg(64))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员上传返回 %d，期望 403", rec.Code)
	}
	// PNG 伪装：400。
	rec, _ = uploadAudio(t, f.router, audioPath, f.ownerToken, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PNG 伪装上传返回 %d，期望 400", rec.Code)
	}
	// 超 500KB：400。
	rec, _ = uploadAudio(t, f.router, audioPath, f.ownerToken, fakeOgg(int(500<<10)+1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超限上传返回 %d，期望 400", rec.Code)
	}
	// 合法 OGG：200，audio_url 落公开资产路径，duration_ms 记录客户端自报值。
	rec, pack := uploadAudio(t, f.router, audioPath, f.ownerToken, fakeOgg(1024))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法上传返回 %d: %s", rec.Code, rec.Body.String())
	}
	audioURL, _ := pack["audio_url"].(string)
	if !strings.HasPrefix(audioURL, "/public-assets/voicepacks/") || !strings.HasSuffix(audioURL, ".ogg") {
		t.Fatalf("audio_url 异常: %q", audioURL)
	}
	if pack["duration_ms"].(float64) != 2300 || pack["size_bytes"].(float64) != 1024 {
		t.Fatalf("duration/size 异常: %s", rec.Body.String())
	}
	// 公开资产无鉴权可拉取（docs 12 §5.1 客户端直拉）。
	assetRec := httptest.NewRecorder()
	f.router.ServeHTTP(assetRec, httptest.NewRequest(http.MethodGet, audioURL, nil))
	if assetRec.Code != http.StatusOK || assetRec.Header().Get("Content-Type") != "audio/ogg" {
		t.Fatalf("公开资产访问异常: %d %s", assetRec.Code, assetRec.Header().Get("Content-Type"))
	}
	// 裸 MP3 帧同步字也放行且换 URL（旧 URL 失效由文件删除保证，这里只验证扩展名）。
	rec, pack = uploadAudio(t, f.router, audioPath, f.ownerToken, []byte{0xFF, 0xFB, 0x90, 0x00, 0x00, 0x00})
	if rec.Code != http.StatusOK || !strings.HasSuffix(pack["audio_url"].(string), ".mp3") {
		t.Fatalf("MP3 上传异常: %d %s", rec.Code, rec.Body.String())
	}
}

// TestVoicePackSelection 用户选包：STANDARD 任何成员可选；RARE 需授权身份组；
// 失去身份组后 @me 的 available=false；取消选择与停用包拒选。
func TestVoicePackSelection(t *testing.T) {
	f := newVoicePackFixture(t)
	meBase := f.packsBase() + "/@me"

	role := model.Role{ID: uuid.New(), GuildID: f.guildID, Name: "传说 " + uuid.NewString()[:8]}
	if err := f.db.Create(&role).Error; err != nil {
		t.Fatalf("建角色失败: %v", err)
	}
	standardID := f.createPack(t, map[string]any{"name": "普通包"})
	rareID := f.createPack(t, map[string]any{
		"name": "稀有包", "kind": "RARE", "allowed_role_ids": []string{role.ID.String()},
	})

	// 成员列表可见性标注：RARE 无身份组 → available=false。
	rec, list := doJSONReq(t, f.router, http.MethodGet, f.packsBase(), f.memberToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表返回 %d", rec.Code)
	}
	for _, raw := range list["voice_packs"].([]any) {
		pack := raw.(map[string]any)
		wantAvailable := pack["id"].(string) == standardID
		if pack["available"].(bool) != wantAvailable {
			t.Fatalf("包 %s available=%v，期望 %v", pack["name"], pack["available"], wantAvailable)
		}
	}

	// STANDARD：任何成员可选。
	rec, _ = doJSONReq(t, f.router, http.MethodPut, f.packsBase()+"/"+standardID+"/select", f.memberToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("选普通包返回 %d: %s", rec.Code, rec.Body.String())
	}
	// RARE 无身份组：403。
	rec, _ = doJSONReq(t, f.router, http.MethodPut, f.packsBase()+"/"+rareID+"/select", f.memberToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无身份组选稀有包返回 %d，期望 403", rec.Code)
	}
	// 授予身份组后可选。
	var member model.Member
	if err := f.db.First(&member, "guild_id = ? AND user_id = ?", f.guildID, f.memberID).Error; err != nil {
		t.Fatalf("查成员失败: %v", err)
	}
	if err := f.db.Create(&model.MemberRole{MemberID: member.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("授予角色失败: %v", err)
	}
	rec, _ = doJSONReq(t, f.router, http.MethodPut, f.packsBase()+"/"+rareID+"/select", f.memberToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("持身份组选稀有包返回 %d: %s", rec.Code, rec.Body.String())
	}
	// @me：选中 RARE 且 available=true。
	rec, me := doJSONReq(t, f.router, http.MethodGet, meBase, f.memberToken, nil)
	selection := me["selection"].(map[string]any)
	if rec.Code != http.StatusOK || selection["id"].(string) != rareID || selection["available"].(bool) != true {
		t.Fatalf("@me 异常: %d %s", rec.Code, rec.Body.String())
	}
	// 失去身份组：@me 仍返回选中包但 available=false（客户端据此提示回退，FR-12）。
	if err := f.db.Where("member_id = ? AND role_id = ?", member.ID, role.ID).Delete(&model.MemberRole{}).Error; err != nil {
		t.Fatalf("移除角色失败: %v", err)
	}
	rec, me = doJSONReq(t, f.router, http.MethodGet, meBase, f.memberToken, nil)
	selection = me["selection"].(map[string]any)
	if rec.Code != http.StatusOK || selection["available"].(bool) != false {
		t.Fatalf("失去身份组后 available 应为 false: %s", rec.Body.String())
	}
	// 取消选择。
	rec, _ = doJSONReq(t, f.router, http.MethodDelete, meBase, f.memberToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("取消选择返回 %d", rec.Code)
	}
	rec, me = doJSONReq(t, f.router, http.MethodGet, meBase, f.memberToken, nil)
	if rec.Code != http.StatusOK || me["selection"] != nil {
		t.Fatalf("取消后 @me 应为空: %s", rec.Body.String())
	}
	// 停用包拒选。
	rec, _ = doJSONReq(t, f.router, http.MethodPatch, f.packsBase()+"/"+standardID, f.ownerToken, map[string]any{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("停用包返回 %d", rec.Code)
	}
	rec, _ = doJSONReq(t, f.router, http.MethodPut, f.packsBase()+"/"+standardID+"/select", f.memberToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("选停用包返回 %d，期望 403", rec.Code)
	}
	// 非成员（第三个用户）：404 防扫频。
	strangerToken, _ := signupUser(t, f.router, f.db, "vp_stranger")
	rec, _ = doJSONReq(t, f.router, http.MethodGet, f.packsBase(), strangerToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员列表返回 %d，期望 404", rec.Code)
	}
}
