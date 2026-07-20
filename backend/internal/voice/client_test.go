package voice

// 用户端双前缀挂载与 Service 单例语义测试（无需数据库）。
//
// 说明性测试策略：Service 的真实构造（迁移引擎启动、总线订阅）需要数据库，
// 这里预置 sharedService 存根走「复用路径」，验证 ensureService 的单例守卫：
// 后台 Register 与用户端 RegisterClient 反复调用均不再触发第二次构造
//（serviceInitCount 不增长 ⇒ 不会出现第二个迁移引擎 / 重复总线订阅，
// 因为二者只在 ensureService 的构造分支内发生）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// seedStubService 预置一个未启动后台副作用的 Service 存根，测试结束后恢复现场。
func seedStubService(t *testing.T) *Service {
	t.Helper()
	sched := defaultSchedConfig()
	stub := &Service{rtt: newRTTStore(sched), resv: newReservationStore(), sched: sched}
	stub.engine = newMigrationEngine(stub)
	prevSvc, prevCount := sharedService, serviceInitCount
	sharedService = stub
	t.Cleanup(func() { sharedService, serviceInitCount = prevSvc, prevCount })
	return stub
}

// stubAuth 模拟认证中间件：无 Bearer 头一律 401（与 clientapi.requireAuth 同语义），
// 有头则放行（本测试不校验 token 内容）。
func stubAuth(c *gin.Context) {
	if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "缺少访问令牌"}})
		return
	}
	c.Next()
}

func stubCurrentUser(*gin.Context) model.User { return model.User{} }

func stubDeps() appdeps.Deps {
	return appdeps.Deps{Auth: stubAuth, CurrentUser: stubCurrentUser}
}

// TestEnsureServiceSingleton 单例守卫：实例已存在时，任何前缀的注册都不再构造。
func TestEnsureServiceSingleton(t *testing.T) {
	stub := seedStubService(t)
	base := serviceInitCount

	first, err := ensureService(stubDeps())
	if err != nil {
		t.Fatalf("ensureService 复用路径不应报错: %v", err)
	}
	second, err := ensureService(stubDeps())
	if err != nil {
		t.Fatalf("ensureService 复用路径不应报错: %v", err)
	}
	if first != stub || second != stub {
		t.Fatal("ensureService 应复用已存在的包级单例")
	}
	if first.engine != second.engine {
		t.Fatal("两次获取的迁移引擎应为同一实例")
	}
	if serviceInitCount != base {
		t.Fatalf("构造计数不应增长：期望 %d，实际 %d（构造分支被重复执行，会产生第二个迁移引擎与重复总线订阅）", base, serviceInitCount)
	}
}

// TestDualPrefixRegistration 后台 /api/v1 与用户端 /gapi/v1 共存注册不 panic，
// 用户端未认证 401、认证后可达 handler，管理端点在用户端不可达（404）。
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

	// 用户端未认证 → 401（认证中间件先于业务 handler）。
	for _, path := range []string{"/gapi/v1/voice/join", "/gapi/v1/voice/leave", "/gapi/v1/voice/rtt"} {
		if rec := serve(http.MethodPost, path, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("未认证 POST %s 期望 401，实际 %d", path, rec.Code)
		}
	}
	// 认证后路由可达：空请求体在 handler 内参数绑定失败 → 400（未触碰数据库，
	// 证明中间件链（认证 + 当前用户注入）与 handler 均已正确挂载）。
	if rec := serve(http.MethodPost, "/gapi/v1/voice/join", "fake-token"); rec.Code != http.StatusBadRequest {
		t.Fatalf("认证后 POST /gapi/v1/voice/join 空体期望 400，实际 %d", rec.Code)
	}
	// 后台平面同路径也正常存在（双前缀共存）。
	if rec := serve(http.MethodPost, "/api/v1/voice/join", "fake-token"); rec.Code != http.StatusBadRequest {
		t.Fatalf("认证后 POST /api/v1/voice/join 空体期望 400，实际 %d", rec.Code)
	}

	// 管理端点保持仅后台：用户端前缀不可达（404）。
	adminOnly := []struct{ method, path string }{
		{http.MethodPost, "/gapi/v1/admin/voice/migrations"},
		{http.MethodPost, "/gapi/v1/guilds/00000000-0000-0000-0000-000000000001/voice/disconnect"},
		{http.MethodPatch, "/gapi/v1/guilds/00000000-0000-0000-0000-000000000001/voice/states/00000000-0000-0000-0000-000000000002"},
	}
	for _, ep := range adminOnly {
		if rec := serve(ep.method, ep.path, "fake-token"); rec.Code != http.StatusNotFound {
			t.Fatalf("管理端点 %s %s 不应挂到用户端：期望 404，实际 %d", ep.method, ep.path, rec.Code)
		}
	}
}
