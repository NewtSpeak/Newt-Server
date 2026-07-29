package guildapi_test

// 集成测试：需要真实 PostgreSQL（运行方式见 clientapi/integration_test.go 头注释）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/guildapi/
//
// 覆盖重点（任务验收项）：
//  1. client / 后台双平面均保留 SystemAdmin 短路（docs 04 FR-32，系统所有者可管全服）；
//  2. 角色/成员治理的层级校验与防提权；
//  3. 所有者保护（不可踢/不可退/删服与转让仅所有者）；
//  4. 结构变更的 Gateway 事件发布（GUILD_UPDATE/DELETE、CHANNEL_*、GUILD_ROLE_DELETE、
//     PERMISSIONS_UPDATE、GUILD_MEMBER_*）；
//  5. 邀请预览/次数上限、节点池与审计日志投影。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/auditapi"
	"github.com/newtspeak/newt-server/backend/internal/clientapi"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/guildapi"
	"github.com/newtspeak/newt-server/backend/internal/httpapi"
	"github.com/newtspeak/newt-server/backend/internal/mediatoken"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/moderation"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"github.com/newtspeak/newt-server/backend/internal/restriction"
	"github.com/newtspeak/newt-server/backend/internal/secretstore"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"github.com/newtspeak/newt-server/backend/internal/sfunode"
	"github.com/newtspeak/newt-server/backend/internal/snapshot"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testSecret = "guildapi-integration-secret-32ch"

// eventCollector 收集事件总线上的全部事件供断言。
type eventCollector struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (ec *eventCollector) handle(event eventbus.Event) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.events = append(ec.events, event)
}

// wait 轮询等待出现满足条件的事件（总线异步分发）。
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
	router *gin.Engine
	db     *gorm.DB
	tokens *security.TokenManager
	events *eventCollector
}

// newEnv 复现生产装配拓扑：后台 /api/v1（httpapi + 各模块）+ 用户端 /gapi/v1（clientapi 投影）。
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
	cfg := config.Config{
		JWTSecret: testSecret, AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour,
		DataDir: t.TempDir(), ControlAddress: "127.0.0.1:0", MediaTokenTTL: 3 * time.Minute,
	}
	bus := eventbus.New()
	collector := &eventCollector{}
	bus.Subscribe(collector.handle)

	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	api.AttachEventBus(bus)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	// MediaTokens 使 voice 模块（含语音管理端点投影）完成装配。
	mediaTokens, err := mediatoken.Load(secretstore.GormStore{DB: db}, cfg.MediaTokenTTL)
	if err != nil {
		t.Fatalf("加载 Media Token 签发器失败: %v", err)
	}
	deps := appdeps.Deps{DB: db, Bus: bus, Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser, MediaTokens: mediaTokens}
	for _, register := range []func(*gin.RouterGroup, appdeps.Deps) error{
		restriction.Register, sfunode.Register, moderation.Register, guildapi.Register, auditapi.Register,
	} {
		if err := register(v1, deps); err != nil {
			t.Fatalf("后台模块装配失败: %v", err)
		}
	}
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("用户端装配失败: %v", err)
	}
	return &testEnv{router: router, db: db, tokens: tokens, events: collector}
}

func (env *testEnv) do(t *testing.T, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
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
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	parsed := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec, parsed
}

// account 一个用户端账号（token 为 aud=client）。
type account struct {
	ID    uuid.UUID
	Token string
}

func (env *testEnv) signup(t *testing.T) account {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	rec, body := env.do(t, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": "ga_" + suffix, "email": "ga_" + suffix + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	id, err := uuid.Parse(body["user"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatalf("解析用户 ID 失败: %v", err)
	}
	return account{ID: id, Token: body["access_token"].(string)}
}

// createGuild 用户端建服，返回 guildID。
func (env *testEnv) createGuild(t *testing.T, owner account) uuid.UUID {
	t.Helper()
	rec, body := env.do(t, http.MethodPost, "/gapi/v1/guilds", owner.Token, map[string]string{
		"name": "测试服 " + fmt.Sprintf("%08x", rand.Uint32()),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建服返回 %d: %s", rec.Code, rec.Body.String())
	}
	id, _ := uuid.Parse(body["id"].(string))
	return id
}

// join 直接落库加成员（绕开邀请，简化前置铺设）。
func (env *testEnv) join(t *testing.T, guildID uuid.UUID, user account) uuid.UUID {
	t.Helper()
	member := model.Member{ID: uuid.New(), GuildID: guildID, UserID: user.ID}
	if err := env.db.Create(&member).Error; err != nil {
		t.Fatalf("落库加成员失败: %v", err)
	}
	return member.ID
}

// createRole 用 owner 的 client token 建角色并（可选）绑定给成员。
func (env *testEnv) createRole(t *testing.T, owner account, guildID uuid.UUID, name string, permissions rbac.Permission, position int) uuid.UUID {
	t.Helper()
	rec, body := env.do(t, http.MethodPost, fmt.Sprintf("/gapi/v1/guilds/%s/roles", guildID), owner.Token, map[string]any{
		"name": name, "permissions": int64(uint64(permissions)), "position": position,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建角色返回 %d: %s", rec.Code, rec.Body.String())
	}
	id, _ := uuid.Parse(body["id"].(string))
	return id
}

func (env *testEnv) assignRole(t *testing.T, owner account, guildID, memberID, roleID uuid.UUID) {
	t.Helper()
	rec, _ := env.do(t, http.MethodPut, fmt.Sprintf("/gapi/v1/guilds/%s/members/%s/roles/%s", guildID, memberID, roleID), owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("绑定角色返回 %d", rec.Code)
	}
}

func errCode(body map[string]any) string {
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// ---------------------------------------------------------------------------
// 1. client 平面系统所有者（system_admin）全服短路（docs 04 FR-32）
// ---------------------------------------------------------------------------

func TestClientPlaneSystemOwnerShortcut(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	guildID := env.createGuild(t, owner)

	sysadmin := env.signup(t)
	if err := env.db.Model(&model.User{}).Where("id = ?", sysadmin.ID).Update("system_admin", true).Error; err != nil {
		t.Fatalf("提升系统所有者失败: %v", err)
	}

	// client 平面：系统所有者即使非成员也可读/管理任意服。
	rec, _ := env.do(t, http.MethodGet, fmt.Sprintf("/gapi/v1/guilds/%s/roles", guildID), sysadmin.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("client 平面系统所有者 GET roles 返回 %d，期待 200", rec.Code)
	}
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("/gapi/v1/guilds/%s", guildID), sysadmin.Token, map[string]any{"name": "系统管改名"})
	if rec.Code != http.StatusOK {
		t.Fatalf("client 平面系统所有者 PATCH guild 返回 %d，期待 200: %s", rec.Code, rec.Body.String())
	}
	rec, _ = env.do(t, http.MethodGet, fmt.Sprintf("/gapi/v1/guilds/%s/bans", guildID), sysadmin.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("client 平面系统所有者 GET bans 返回 %d，期待 200", rec.Code)
	}

	// 后台平面短路保持。
	adminToken, _, err := env.tokens.AccessToken(sysadmin.ID)
	if err != nil {
		t.Fatalf("签发后台 token 失败: %v", err)
	}
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/guilds/%s", guildID), adminToken, map[string]any{"name": "后台改名OK"})
	if rec.Code != http.StatusOK {
		t.Fatalf("后台平面系统管理员 PATCH guild 返回 %d，期待 200: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 2. 角色层级校验与防提权（client 平面）
// ---------------------------------------------------------------------------

func TestRoleHierarchyAndAntiEscalation(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	manager := env.signup(t)
	peer := env.signup(t)
	guildID := env.createGuild(t, owner)
	managerMemberID := env.join(t, guildID, manager)
	peerMemberID := env.join(t, guildID, peer)

	modRoleID := env.createRole(t, owner, guildID, "mod", rbac.ManageRoles|rbac.KickMembers, 5)
	env.assignRole(t, owner, guildID, managerMemberID, modRoleID)

	base := fmt.Sprintf("/gapi/v1/guilds/%s", guildID)

	// 层级之下 + 权限子集 → 允许。
	rec, body := env.do(t, http.MethodPost, base+"/roles", manager.Token, map[string]any{
		"name": "helper", "permissions": int64(uint64(rbac.KickMembers)), "position": 4,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("管理员建下级角色返回 %d: %s", rec.Code, rec.Body.String())
	}
	helperRoleID, _ := uuid.Parse(body["id"].(string))

	// 层级不低于自身 → 403。
	rec, _ = env.do(t, http.MethodPost, base+"/roles", manager.Token, map[string]any{
		"name": "same-pos", "permissions": int64(uint64(rbac.KickMembers)), "position": 5,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("同层级建角色返回 %d，期待 403", rec.Code)
	}
	// 授予超过自身的权限位 → 403（防提权）。
	rec, _ = env.do(t, http.MethodPost, base+"/roles", manager.Token, map[string]any{
		"name": "esc", "permissions": int64(uint64(rbac.Administrator)), "position": 1,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("授予 Administrator 返回 %d，期待 403", rec.Code)
	}
	// 管理自己所在层级的角色 → 403。
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("%s/roles/%s", base, modRoleID), manager.Token, map[string]any{
		"name": "mod2", "permissions": int64(uint64(rbac.KickMembers)), "position": 4,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("管理同层角色返回 %d，期待 403", rec.Code)
	}
	// 把高层角色绑给别人 → 403。
	rec, _ = env.do(t, http.MethodPut, fmt.Sprintf("%s/members/%s/roles/%s", base, peerMemberID, modRoleID), manager.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("绑定同层角色返回 %d，期待 403", rec.Code)
	}
	// 绑定下级角色 → 允许，并发 GUILD_MEMBER_UPDATE。
	rec, _ = env.do(t, http.MethodPut, fmt.Sprintf("%s/members/%s/roles/%s", base, peerMemberID, helperRoleID), manager.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("绑定下级角色返回 %d", rec.Code)
	}
	env.events.wait(t, "GUILD_MEMBER_UPDATE", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventGuildMemberUpdate && e.GuildID != nil && *e.GuildID == guildID
	})

	// 角色删除：管理者删同层 → 403；owner 删下级 → 204 + GUILD_ROLE_DELETE + 绑定清理。
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/roles/%s", base, modRoleID), manager.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("管理者删同层角色返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/roles/%s", base, helperRoleID), owner.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner 删角色返回 %d", rec.Code)
	}
	env.events.wait(t, "GUILD_ROLE_DELETE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildRoleDeletePayload)
		return e.Type == eventbus.EventGuildRoleDelete && ok && payload.RoleID == helperRoleID
	})
	var bindings int64
	env.db.Model(&model.MemberRole{}).Where("role_id = ?", helperRoleID).Count(&bindings)
	if bindings != 0 {
		t.Fatalf("角色删除后仍残留 %d 条成员绑定", bindings)
	}

	// @everyone 不可删。
	var everyone model.Role
	if err := env.db.First(&everyone, "guild_id = ? AND is_everyone = true", guildID).Error; err != nil {
		t.Fatalf("找不到 @everyone: %v", err)
	}
	rec, body = env.do(t, http.MethodDelete, fmt.Sprintf("%s/roles/%s", base, everyone.ID), owner.Token, nil)
	if rec.Code != http.StatusBadRequest || errCode(body) != "EVERYONE_UNDELETABLE" {
		t.Fatalf("删 @everyone 返回 %d/%s，期待 400/EVERYONE_UNDELETABLE", rec.Code, errCode(body))
	}
}

// ---------------------------------------------------------------------------
// 3. 服务器生命周期：PATCH / 转让 / 退出 / 删除（client 平面）
// ---------------------------------------------------------------------------

func TestGuildLifecycle(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	member := env.signup(t)
	guildID := env.createGuild(t, owner)
	env.join(t, guildID, member)
	base := fmt.Sprintf("/gapi/v1/guilds/%s", guildID)

	// PATCH：owner OK + GUILD_UPDATE；普通成员 403。
	rec, body := env.do(t, http.MethodPatch, base, owner.Token, map[string]any{"name": "新名字OK", "description": "描述"})
	if rec.Code != http.StatusOK || body["name"] != "新名字OK" || body["description"] != "描述" {
		t.Fatalf("owner PATCH guild 返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "GUILD_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildPayload)
		return e.Type == eventbus.EventGuildUpdate && ok && payload.Guild.Name == "新名字OK"
	})
	rec, _ = env.do(t, http.MethodPatch, base, member.Token, map[string]any{"name": "成员改名"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员 PATCH guild 返回 %d，期待 403", rec.Code)
	}

	// 转让：非所有者 403；owner → 200；所有者退出保护随转让切换。
	rec, _ = env.do(t, http.MethodPost, base+"/transfer-ownership", member.Token, map[string]any{"new_owner_user_id": member.ID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非所有者转让返回 %d，期待 403", rec.Code)
	}
	rec, body = env.do(t, http.MethodDelete, base+"/members/@me", owner.Token, nil)
	if rec.Code != http.StatusConflict || errCode(body) != "OWNER_CANNOT_LEAVE" {
		t.Fatalf("所有者退出返回 %d/%s，期待 409/OWNER_CANNOT_LEAVE", rec.Code, errCode(body))
	}
	rec, _ = env.do(t, http.MethodPost, base+"/transfer-ownership", owner.Token, map[string]any{"new_owner_user_id": member.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("所有者转让返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "转让后 GUILD_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildPayload)
		return e.Type == eventbus.EventGuildUpdate && ok && payload.Guild.OwnerUserID == member.ID
	})

	// 原所有者退出 → 204 + GUILD_MEMBER_REMOVE(reason=leave)。
	rec, _ = env.do(t, http.MethodDelete, base+"/members/@me", owner.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("原所有者退出返回 %d", rec.Code)
	}
	env.events.wait(t, "GUILD_MEMBER_REMOVE(leave)", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildMemberRemovePayload)
		return e.Type == eventbus.EventGuildMemberRemove && ok && payload.UserID == owner.ID && payload.Reason == "leave"
	})

	// 删除：非所有者（已退出的 owner）404；confirm_name 不匹配 400；
	// 新所有者带正确确认 204 + GUILD_DELETE + 数据清理。
	rec, _ = env.do(t, http.MethodDelete, base, owner.Token, map[string]any{"confirm_name": "新名字OK"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员删服返回 %d，期待 404", rec.Code)
	}
	rec, body = env.do(t, http.MethodDelete, base, member.Token, map[string]any{"confirm_name": "名字不对"})
	if rec.Code != http.StatusBadRequest || errCode(body) != "CONFIRM_NAME_MISMATCH" {
		t.Fatalf("确认名不匹配删服返回 %d/%s，期待 400/CONFIRM_NAME_MISMATCH", rec.Code, errCode(body))
	}
	rec, _ = env.do(t, http.MethodDelete, base, member.Token, map[string]any{"confirm_name": "新名字OK"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("所有者删服返回 %d: %s", rec.Code, rec.Body.String())
	}
	deleteEvent := env.events.wait(t, "GUILD_DELETE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildDeletePayload)
		return e.Type == eventbus.EventGuildDelete && ok && payload.GuildID == guildID
	})
	if len(deleteEvent.UserIDs) == 0 {
		t.Fatalf("GUILD_DELETE 未定向到成员: %+v", deleteEvent)
	}
	var count int64
	env.db.Model(&model.Member{}).Where("guild_id = ?", guildID).Count(&count)
	if count != 0 {
		t.Fatalf("删服后残留 %d 个成员", count)
	}
	env.db.Model(&model.Role{}).Where("guild_id = ?", guildID).Count(&count)
	if count != 0 {
		t.Fatalf("删服后残留 %d 个角色", count)
	}
}

// ---------------------------------------------------------------------------
// 4. 频道生命周期 + 权限覆盖（client 平面）
// ---------------------------------------------------------------------------

func TestChannelLifecycleAndOverwrites(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	member := env.signup(t)
	guildID := env.createGuild(t, owner)
	env.join(t, guildID, member)
	base := fmt.Sprintf("/gapi/v1/guilds/%s", guildID)

	// 创建：成员无 MANAGE_CHANNELS → 403；owner → 201 + CHANNEL_CREATE。
	rec, _ := env.do(t, http.MethodPost, base+"/channels", member.Token, map[string]any{"name": "越权频道", "type": "TEXT"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员建频道返回 %d，期待 403", rec.Code)
	}
	rec, body := env.do(t, http.MethodPost, base+"/channels", owner.Token, map[string]any{"name": "公告", "type": "TEXT", "topic": "看板"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner 建频道返回 %d: %s", rec.Code, rec.Body.String())
	}
	channelID, _ := uuid.Parse(body["id"].(string))
	env.events.wait(t, "CHANNEL_CREATE", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventChannelCreate && e.ChannelID != nil && *e.ChannelID == channelID
	})

	// 权限投影：guild 级与频道级（顶级 /channels/{cid} 入口）均返回十进制字符串掩码
	//（扩展位超出 JS Number 精度，客户端用 BigInt 解析）。
	rec, body = env.do(t, http.MethodGet, base+"/permissions/@me", member.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("guild permissions/@me 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if raw, ok := body["permissions"].(string); !ok {
		t.Fatalf("guild permissions 掩码不是字符串: %v", body["permissions"])
	} else if _, err := strconv.ParseUint(raw, 10, 64); err != nil {
		t.Fatalf("guild permissions 掩码不是十进制字符串: %v", raw)
	}
	channelPermsPath := fmt.Sprintf("/gapi/v1/channels/%s/permissions/@me", channelID)
	rec, body = env.do(t, http.MethodGet, channelPermsPath, member.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("channel permissions/@me 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if raw, ok := body["permissions"].(string); !ok {
		t.Fatalf("channel permissions 掩码不是字符串: %v", body["permissions"])
	} else if _, err := strconv.ParseUint(raw, 10, 64); err != nil {
		t.Fatalf("channel permissions 掩码不是十进制字符串: %v", raw)
	}

	// PATCH /channels/{cid}：成员 403；owner 200 + CHANNEL_UPDATE；不存在 404。
	channelPath := fmt.Sprintf("/gapi/v1/channels/%s", channelID)
	rec, _ = env.do(t, http.MethodPatch, channelPath, member.Token, map[string]any{"name": "成员改"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员 PATCH 频道返回 %d，期待 403", rec.Code)
	}
	rec, body = env.do(t, http.MethodPatch, channelPath, owner.Token, map[string]any{"name": "公告栏", "topic": "新看板"})
	if rec.Code != http.StatusOK || body["topic"] != "新看板" {
		t.Fatalf("owner PATCH 频道返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "CHANNEL_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(snapshot.ChannelPayload)
		return e.Type == eventbus.EventChannelUpdate && ok && payload.Channel.Channel.ID == channelID && payload.Topic == "新看板"
	})
	rec, _ = env.do(t, http.MethodPatch, "/gapi/v1/channels/"+uuid.NewString(), owner.Token, map[string]any{"name": "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH 不存在频道返回 %d，期待 404", rec.Code)
	}

	// 批量排序：owner PATCH /guilds/{gid}/channels → 204，列表顺序生效。
	rec, body2 := env.do(t, http.MethodPost, base+"/channels", owner.Token, map[string]any{"name": "闲聊", "type": "TEXT"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建第二个频道返回 %d", rec.Code)
	}
	secondID, _ := uuid.Parse(body2["id"].(string))
	rec, _ = env.do(t, http.MethodPatch, base+"/channels", owner.Token, []map[string]any{
		{"id": secondID, "position": 1}, {"id": channelID, "position": 2},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("批量排序返回 %d: %s", rec.Code, rec.Body.String())
	}
	var ordered []model.Channel
	env.db.Where("guild_id = ?", guildID).Order("position ASC, created_at ASC").Find(&ordered)
	if len(ordered) < 2 || ordered[0].ID != secondID {
		t.Fatalf("排序未生效: %+v", ordered)
	}

	// 权限覆盖（顶级 /channels/{cid}/overwrites/{target} 入口）：对 @everyone deny
	// VIEW_CHANNEL → 成员失去可见性，事件：PERMISSIONS_UPDATE 广播 + 定向 CHANNEL_DELETE。
	var everyone model.Role
	if err := env.db.First(&everyone, "guild_id = ? AND is_everyone = true", guildID).Error; err != nil {
		t.Fatalf("找不到 @everyone: %v", err)
	}
	overwritePath := fmt.Sprintf("/gapi/v1/channels/%s/overwrites/%s", channelID, everyone.ID)
	// 无 MANAGE_ROLES 的成员经顶级入口 upsert → 403（可见但权限不足）。
	rec, _ = env.do(t, http.MethodPut, overwritePath, member.Token, map[string]any{
		"type": "ROLE", "allow": 0, "deny": int64(uint64(rbac.ViewChannel)),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员顶级入口覆盖 upsert 返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodPut, overwritePath, owner.Token, map[string]any{
		"type": "ROLE", "allow": 0, "deny": int64(uint64(rbac.ViewChannel)),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("覆盖 upsert 返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "PERMISSIONS_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.PermissionsUpdatePayload)
		return e.Type == eventbus.EventPermissionsUpdate && ok && payload.ChannelID == channelID
	})
	env.events.wait(t, "失去可见性的定向 CHANNEL_DELETE", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventChannelDelete || len(e.UserIDs) == 0 {
			return false
		}
		for _, id := range e.UserIDs {
			if id == member.ID {
				return true
			}
		}
		return false
	})
	// 失去 VIEW 的成员访问频道 → 404（不可见即不存在，顶级权限投影同理）。
	rec, _ = env.do(t, http.MethodPatch, channelPath, member.Token, map[string]any{"name": "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("失去可见性后成员访问频道返回 %d，期待 404", rec.Code)
	}
	rec, _ = env.do(t, http.MethodGet, channelPermsPath, member.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("失去可见性后 channel permissions/@me 返回 %d，期待 404", rec.Code)
	}

	// 删除覆盖（guild 前缀入口）→ 可见性恢复，定向 CHANNEL_CREATE。
	guildOverwritePath := fmt.Sprintf("%s/channels/%s/overwrites/%s", base, channelID, everyone.ID)
	rec, _ = env.do(t, http.MethodDelete, guildOverwritePath+"?type=ROLE", owner.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除覆盖返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "恢复可见性的定向 CHANNEL_CREATE", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventChannelCreate || len(e.UserIDs) == 0 {
			return false
		}
		for _, id := range e.UserIDs {
			if id == member.ID {
				return true
			}
		}
		return false
	})

	// 删除频道：owner → 204 + 定向 CHANNEL_DELETE；覆盖记录清理。
	rec, _ = env.do(t, http.MethodDelete, channelPath, owner.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除频道返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "删除频道 CHANNEL_DELETE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.ChannelDeletePayload)
		return e.Type == eventbus.EventChannelDelete && ok && payload.ChannelID == channelID && len(e.UserIDs) > 0
	})
	var count int64
	env.db.Model(&model.Channel{}).Where("id = ?", channelID).Count(&count)
	if count != 0 {
		t.Fatalf("频道删除后仍存在")
	}
}

// ---------------------------------------------------------------------------
// 5. 治理投影：踢/封/昵称/Restriction（client 平面）
// ---------------------------------------------------------------------------

func TestModerationProjection(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	moderatorAcc := env.signup(t)
	peer := env.signup(t)
	guildID := env.createGuild(t, owner)
	moderatorMemberID := env.join(t, guildID, moderatorAcc)
	peerMemberID := env.join(t, guildID, peer)

	modRoleID := env.createRole(t, owner, guildID, "治安官",
		rbac.KickMembers|rbac.BanMembers|rbac.ModerateMembers|rbac.ManageNicknames, 5)
	env.assignRole(t, owner, guildID, moderatorMemberID, modRoleID)
	base := fmt.Sprintf("/gapi/v1/guilds/%s", guildID)

	// 昵称：本人（默认 CHANGE_NICKNAME）→ 200 + GUILD_MEMBER_UPDATE。
	rec, _ := env.do(t, http.MethodPatch, fmt.Sprintf("%s/members/@me", base), peer.Token, map[string]any{"nickname": "小P"})
	if rec.Code != http.StatusOK {
		t.Fatalf("本人改昵称返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "昵称 GUILD_MEMBER_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildMemberUpdatePayload)
		return e.Type == eventbus.EventGuildMemberUpdate && ok && payload.Member.Nickname == "小P"
	})
	// 他人：普通成员无 MANAGE_NICKNAMES → 403；治安官 → 200；所有者目标 → 403。
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("%s/members/%s", base, moderatorMemberID), peer.Token, map[string]any{"nickname": "越权"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通成员改他人昵称返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("%s/members/%s", base, peerMemberID), moderatorAcc.Token, map[string]any{"nickname": "P同学"})
	if rec.Code != http.StatusOK {
		t.Fatalf("治安官改下级昵称返回 %d: %s", rec.Code, rec.Body.String())
	}
	var ownerMember model.Member
	if err := env.db.First(&ownerMember, "guild_id = ? AND user_id = ?", guildID, owner.ID).Error; err != nil {
		t.Fatalf("找不到 owner 成员: %v", err)
	}
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("%s/members/%s", base, ownerMember.ID), moderatorAcc.Token, map[string]any{"nickname": "动老板"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("修改所有者昵称返回 %d，期待 403", rec.Code)
	}

	// Restriction：治安官创建 → 201 + RESTRICTION_CREATE；普通成员创建 → 403；列表需权限。
	// SANCTION 必须限时（长期限制仅 CHANNEL_BAN 或系统管理员，docs 12）。
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	rec, body := env.do(t, http.MethodPost, base+"/restrictions", moderatorAcc.Token, map[string]any{
		"target_user_id": peer.ID, "scope": "GUILD_ALL_TEXT", "kind": "SANCTION",
		"deny": map[string]bool{"send_text": true}, "reason": "测试禁言", "expires_at": expiresAt,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("治安官创建限制返回 %d: %s", rec.Code, rec.Body.String())
	}
	restrictionID := body["id"].(string)
	env.events.wait(t, "RESTRICTION_CREATE", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventRestrictionCreate
	})
	rec, _ = env.do(t, http.MethodPost, base+"/restrictions", peer.Token, map[string]any{
		"target_user_id": moderatorAcc.ID, "scope": "GUILD_ALL_TEXT", "kind": "SANCTION",
		"deny": map[string]bool{"send_text": true}, "reason": "反向禁言", "expires_at": expiresAt,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通成员创建限制返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodGet, base+"/restrictions", peer.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通成员看限制列表返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/restrictions/%s", base, restrictionID), moderatorAcc.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("解除限制返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 踢出：踢所有者 → 403；踢自己 → 400；治安官踢下级 → 204 + GUILD_MEMBER_REMOVE(kick)。
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/members/%s", base, ownerMember.ID), moderatorAcc.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("踢所有者返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/members/%s", base, moderatorMemberID), moderatorAcc.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("踢自己返回 %d，期待 400", rec.Code)
	}
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/members/%s", base, peerMemberID), moderatorAcc.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("踢下级返回 %d: %s", rec.Code, rec.Body.String())
	}
	env.events.wait(t, "GUILD_MEMBER_REMOVE(kick)", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildMemberRemovePayload)
		return e.Type == eventbus.EventGuildMemberRemove && ok && payload.UserID == peer.ID && payload.Reason == "kick"
	})

	// 封禁：治安官封 peer → 200；GET bans 权限（peer 已被踢，普通成员无 BAN_MEMBERS → 403）；解封 → 204。
	rec, _ = env.do(t, http.MethodPut, fmt.Sprintf("%s/bans/%s", base, peer.ID), moderatorAcc.Token, map[string]any{"reason": "测试封禁"})
	if rec.Code != http.StatusOK {
		t.Fatalf("封禁返回 %d: %s", rec.Code, rec.Body.String())
	}
	stranger := env.signup(t)
	env.join(t, guildID, stranger)
	rec, _ = env.do(t, http.MethodGet, base+"/bans", stranger.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通成员看封禁列表返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodGet, base+"/bans", moderatorAcc.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("治安官看封禁列表返回 %d", rec.Code)
	}
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/bans/%s", base, peer.ID), moderatorAcc.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("解封返回 %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 6. 邀请：创建（含 max_uses）/ 预览 / 超次 404+410
// ---------------------------------------------------------------------------

func TestInvitePreviewAndMaxUses(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	guildID := env.createGuild(t, owner)

	rec, body := env.do(t, http.MethodPost, fmt.Sprintf("/gapi/v1/guilds/%s/invites", guildID), owner.Token, map[string]any{"max_uses": 1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("client 平面创建邀请返回 %d: %s", rec.Code, rec.Body.String())
	}
	code := body["code"].(string)

	// 邀请列表投影：owner（CREATE_INSTANT_INVITE 由所有者短路满足）可列出，
	// 条目附 share_url/deep_link；非成员 404（不可见即不存在）。
	stranger := env.signup(t)
	listPath := fmt.Sprintf("/gapi/v1/guilds/%s/invites", guildID)
	rec, _ = env.do(t, http.MethodGet, listPath, owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner 列邀请返回 %d: %s", rec.Code, rec.Body.String())
	}
	var invites []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &invites); err != nil || len(invites) != 1 {
		t.Fatalf("邀请列表解析失败(%v): %s", err, rec.Body.String())
	}
	if invites[0]["code"] != code || invites[0]["share_url"] == "" {
		t.Fatalf("邀请列表缺少 code/share_url: %v", invites[0])
	}
	rec, _ = env.do(t, http.MethodGet, listPath, stranger.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员列邀请返回 %d，期待 404", rec.Code)
	}

	// 预览：登录用户可见服务器名称与成员数。
	rec, body = env.do(t, http.MethodGet, "/gapi/v1/invites/"+code, stranger.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("邀请预览返回 %d: %s", rec.Code, rec.Body.String())
	}
	guildInfo := body["guild"].(map[string]any)
	if guildInfo["name"] == "" || guildInfo["member_count"].(float64) != 1 {
		t.Fatalf("预览缺少服务器信息: %v", body)
	}

	// 第一次使用 → 加入成功；第二人 → 404；预览 → 410。
	rec, _ = env.do(t, http.MethodPost, "/gapi/v1/invites/"+code+"/join", stranger.Token, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("第一次凭码加入返回 %d: %s", rec.Code, rec.Body.String())
	}
	second := env.signup(t)
	rec, _ = env.do(t, http.MethodPost, "/gapi/v1/invites/"+code+"/join", second.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("超次加入返回 %d，期待 404", rec.Code)
	}
	rec, _ = env.do(t, http.MethodGet, "/gapi/v1/invites/"+code, second.Token, nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("超次预览返回 %d，期待 410", rec.Code)
	}
	// 不存在的码 → 404。
	rec, _ = env.do(t, http.MethodGet, "/gapi/v1/invites/nonexistent1", second.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知邀请码返回 %d，期待 404", rec.Code)
	}

	// 按码撤销（顶级 DELETE /invites/{code}）：普通成员（stranger 已加入，
	// 无 CREATE_INSTANT_INVITE/MANAGE_GUILD）→ 403；owner → 204；重复撤销 → 404。
	rec, body = env.do(t, http.MethodPost, fmt.Sprintf("/gapi/v1/guilds/%s/invites", guildID), owner.Token, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建第二个邀请返回 %d: %s", rec.Code, rec.Body.String())
	}
	code2 := body["code"].(string)
	rec, _ = env.do(t, http.MethodDelete, "/gapi/v1/invites/"+code2, stranger.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通成员撤销邀请返回 %d，期待 403", rec.Code)
	}
	rec, _ = env.do(t, http.MethodDelete, "/gapi/v1/invites/"+code2, owner.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner 撤销邀请返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = env.do(t, http.MethodDelete, "/gapi/v1/invites/"+code2, owner.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("重复撤销返回 %d，期待 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 7. 节点池 / 审计日志 / 语音管理投影（client 平面）
// ---------------------------------------------------------------------------

func TestNodePoolAuditAndVoiceProjection(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	member := env.signup(t)
	guildID := env.createGuild(t, owner)
	env.join(t, guildID, member)
	base := fmt.Sprintf("/gapi/v1/guilds/%s", guildID)

	// 节点池：owner（MANAGE_GUILD）可读；普通成员 404；候选外节点 PUT → 400。
	rec, body := env.do(t, http.MethodGet, base+"/node-pool", owner.Token, nil)
	if rec.Code != http.StatusOK || body["fallback_to_default"] != true {
		t.Fatalf("owner 读节点池返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = env.do(t, http.MethodGet, base+"/node-pool", member.Token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("普通成员读节点池返回 %d，期待 404", rec.Code)
	}
	rec, _ = env.do(t, http.MethodPut, base+"/node-pool", owner.Token, map[string]any{"node_ids": []string{uuid.NewString()}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("勾选候选外节点返回 %d，期待 400", rec.Code)
	}

	// 审计日志：owner（VIEW_AUDIT_LOG 由所有者短路满足）→ 200 且包含此前动作；普通成员 → 403。
	env.createRole(t, owner, guildID, "留痕角色", rbac.KickMembers, 3)
	rec, body = env.do(t, http.MethodGet, base+"/audit-logs", owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner 读审计返回 %d: %s", rec.Code, rec.Body.String())
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("审计日志为空: %s", rec.Body.String())
	}
	rec, _ = env.do(t, http.MethodGet, base+"/audit-logs", member.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通成员读审计返回 %d，期待 403", rec.Code)
	}

	// 语音管理端点已投影：无权限成员 → 403；owner 对不在语音的目标 → 404 NOT_IN_VOICE。
	rec, _ = env.do(t, http.MethodPost, base+"/voice/disconnect", member.Token, map[string]any{"user_id": owner.ID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员踢语音返回 %d，期待 403", rec.Code)
	}
	rec, body = env.do(t, http.MethodPost, base+"/voice/disconnect", owner.Token, map[string]any{"user_id": member.ID})
	if rec.Code != http.StatusNotFound || errCode(body) != "NOT_IN_VOICE" {
		t.Fatalf("owner 踢不在语音的目标返回 %d/%s，期待 404/NOT_IN_VOICE", rec.Code, errCode(body))
	}
	rec, _ = env.do(t, http.MethodPatch, fmt.Sprintf("%s/voice/states/%s", base, member.ID), member.Token, map[string]any{"server_mute": true})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("成员服务器静音返回 %d，期待 403", rec.Code)
	}
}
