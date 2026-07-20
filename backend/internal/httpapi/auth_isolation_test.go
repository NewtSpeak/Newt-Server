package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

// newAuthTestRouter 只挂 requireAuth 的最小路由。db 传 nil：交叉受众的 token 在
// JWT 解析阶段即被拒绝，不会触达数据库（这正是本测试要锁定的行为）。
func newAuthTestRouter(tokens *security.TokenManager) *gin.Engine {
	gin.SetMode(gin.TestMode)
	api := New(nil, tokens, time.Hour)
	router := gin.New()
	router.GET("/protected", api.requireAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })
	return router
}

// TestRequireAuthRejectsClientToken 用户端（aud=client）token 打后台 API 必须 401。
func TestRequireAuthRejectsClientToken(t *testing.T) {
	tokens := security.NewTokenManager("0123456789abcdef0123456789abcdef", time.Minute)
	router := newAuthTestRouter(tokens)

	clientToken, _, err := tokens.AccessTokenWithAudience(uuid.New(), security.AudienceClient)
	if err != nil {
		t.Fatalf("签发 client token 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("client token 打后台 API 返回 %d，期待 401", rec.Code)
	}
}

// TestRequireAuthRejectsMissingToken 无令牌请求 401。
func TestRequireAuthRejectsMissingToken(t *testing.T) {
	tokens := security.NewTokenManager("0123456789abcdef0123456789abcdef", time.Minute)
	router := newAuthTestRouter(tokens)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌返回 %d，期待 401", rec.Code)
	}
}
