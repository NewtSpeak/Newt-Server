package clientapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func testDeps() appdeps.Deps {
	// DB 留空：交叉受众 token 在 JWT 解析阶段即被拒绝，不触达数据库。
	return appdeps.Deps{Cfg: config.Config{JWTSecret: testSecret, AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour}}
}

func newAuthTestRouter(t *testing.T) (*gin.Engine, *security.TokenManager) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := newAPI(testDeps(), true)
	router := gin.New()
	router.GET("/protected", h.requireAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })
	return router, h.tokens
}

// TestRequireAuthRejectsAdminToken 后台（aud=admin）token 打用户端 API 必须 401。
func TestRequireAuthRejectsAdminToken(t *testing.T) {
	router, tokens := newAuthTestRouter(t)
	adminToken, _, err := tokens.AccessToken(uuid.New())
	if err != nil {
		t.Fatalf("签发 admin token 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin token 打用户端 API 返回 %d，期待 401", rec.Code)
	}
	// 安全细节：用户端响应不得出现后台前缀路径字样。
	if strings.Contains(rec.Body.String(), "/api/v1") {
		t.Fatalf("用户端响应泄露了后台路径: %s", rec.Body.String())
	}
}

// TestRequireAuthRejectsMissingToken 无令牌请求 401。
func TestRequireAuthRejectsMissingToken(t *testing.T) {
	router, _ := newAuthTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌返回 %d，期待 401", rec.Code)
	}
}

// TestSignupDisabledSwitch 平台开关关闭时注册返回 403（不触达数据库，在 bind 之前短路）。
func TestSignupDisabledSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newAPI(testDeps(), false)
	router := gin.New()
	router.POST("/auth/signup", h.signup)

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(`{"username":"alice","email":"a@b.c","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("注册关闭时返回 %d，期待 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SIGNUP_DISABLED") {
		t.Fatalf("错误码异常: %s", rec.Body.String())
	}
}

// TestSignupEnabledEnvParsing CLIENT_SIGNUP_ENABLED 的解析语义：缺省/非法值为 true。
func TestSignupEnabledEnvParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true}, {"true", true}, {"1", true}, {"false", false}, {"0", false}, {"not-a-bool", true},
	}
	for _, tc := range cases {
		t.Setenv("CLIENT_SIGNUP_ENABLED", tc.value)
		if got := signupEnabled(); got != tc.want {
			t.Fatalf("CLIENT_SIGNUP_ENABLED=%q → %v，期待 %v", tc.value, got, tc.want)
		}
	}
}
