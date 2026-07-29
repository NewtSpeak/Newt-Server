package stage

// 用户端双前缀挂载与 service 单例语义测试（无需数据库）。
//
// 说明性测试策略：service 的真实构造伴随一次性副作用（权威裁决钩子赋值、
// 总线订阅、后台扫描 goroutine），这里预置 sharedService 存根走「复用路径」，
// 验证 ensureService 的单例守卫：后台 Register 与用户端 RegisterClient 反复
// 调用均不再触发第二次构造（serviceInitCount 不增长 ⇒ 不会重复订阅/双跑扫描，
// 因为副作用只在 ensureService 的构造分支内发生）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// seedStubService 预置一个未启动后台副作用的 service 存根，测试结束后恢复现场。
func seedStubService(t *testing.T) *service {
	t.Helper()
	stub := &service{}
	prevSvc, prevCount := sharedService, serviceInitCount
	sharedService = stub
	t.Cleanup(func() { sharedService, serviceInitCount = prevSvc, prevCount })
	return stub
}

// stubAuth 模拟认证中间件：无 Bearer 头一律 401（与 clientapi.requireAuth 同语义）。
func stubAuth(c *gin.Context) {
	if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "缺少访问令牌"}})
		return
	}
	c.Next()
}

func stubDeps() appdeps.Deps {
	return appdeps.Deps{Auth: stubAuth, CurrentUser: func(*gin.Context) model.User { return model.User{} }}
}

// TestEnsureServiceSingleton 单例守卫：实例已存在时，任何前缀的注册都不再构造。
func TestEnsureServiceSingleton(t *testing.T) {
	stub := seedStubService(t)
	base := serviceInitCount

	if got := ensureService(stubDeps()); got != stub {
		t.Fatal("ensureService 应复用已存在的包级单例")
	}
	if got := ensureService(stubDeps()); got != stub {
		t.Fatal("ensureService 第二次调用也应复用同一单例")
	}
	if serviceInitCount != base {
		t.Fatalf("构造计数不应增长：期望 %d，实际 %d（构造分支被重复执行，会产生重复总线订阅与双跑后台扫描）", base, serviceInitCount)
	}
}

// TestDualPrefixRegistration 后台 /api/v1 与用户端 /gapi/v1 共存注册不 panic，
// 用户端未认证 401、认证后可达 handler，系统管配额端点在用户端不可达（404）。
func TestDualPrefixRegistration(t *testing.T) {
	seedStubService(t)
	base := serviceInitCount
	gin.SetMode(gin.TestMode)
	router := gin.New()
	deps := stubDeps()

	// 后台平面（server.New 中先行）。
	if err := Register(router.Group("/api/v1"), deps); err != nil {
		t.Fatalf("后台 Register 失败: %v", err)
	}
	// 用户端平面：clientapi.Register 传入的是已带用户端认证的组。
	clientAuthed := router.Group("/gapi/v1", deps.Auth)
	RegisterClient(clientAuthed, deps)

	if serviceInitCount != base {
		t.Fatalf("双前缀注册后构造计数不应增长：期望 %d，实际 %d", base, serviceInitCount)
	}

	serve := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// 用户端未认证 → 401。
	unauthenticated := []struct{ method, path string }{
		{http.MethodPost, "/gapi/v1/channels/abc/stage/apply"},
		{http.MethodGet, "/gapi/v1/channels/abc/stage/queue"},
		{http.MethodPost, "/gapi/v1/channels/abc/voice/screen/start"},
		{http.MethodGet, "/gapi/v1/guilds/abc/screen-quota"},
	}
	for _, ep := range unauthenticated {
		if rec := serve(ep.method, ep.path, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("未认证 %s %s 期望 401，实际 %d", ep.method, ep.path, rec.Code)
		}
	}
	// 认证后路由可达：非法 channelID 在 handler 内解析失败 → 404「频道不存在」
	//（未触碰数据库，证明认证链与 handler 均已正确挂载）。
	if rec := serve(http.MethodPost, "/gapi/v1/channels/not-a-uuid/stage/apply", "fake-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("认证后非法频道 ID 期望 404，实际 %d", rec.Code)
	}
	// 后台平面同路径也正常存在（双前缀共存）。
	if rec := serve(http.MethodPost, "/api/v1/channels/not-a-uuid/stage/apply", "fake-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("后台平面非法频道 ID 期望 404，实际 %d", rec.Code)
	}

	// 系统管配额治理端点保持仅后台：用户端前缀不可达（404）。
	adminOnly := []struct{ method, path string }{
		{http.MethodPatch, "/gapi/v1/admin/guilds/00000000-0000-0000-0000-000000000001/screen-quota"},
		{http.MethodPatch, "/gapi/v1/admin/screen-quota/settings"},
	}
	for _, ep := range adminOnly {
		if rec := serve(ep.method, ep.path, "fake-token"); rec.Code != http.StatusNotFound {
			t.Fatalf("系统管端点 %s %s 不应挂到用户端：期望 404，实际 %d", ep.method, ep.path, rec.Code)
		}
	}
}
