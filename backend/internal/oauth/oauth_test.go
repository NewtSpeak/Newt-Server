package oauth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestFilterPlatformScope(t *testing.T) {
	got := FilterScopesForUser("profile platform.admin gapi.full", false)
	if security.ScopeContains(got, ScopePlatformAdmin) {
		t.Fatalf("non-admin should not get platform: %s", got)
	}
	got = FilterScopesForUser("profile platform.admin gapi.full", true)
	if !security.ScopeContains(got, ScopePlatformAdmin) {
		t.Fatalf("admin should keep platform: %s", got)
	}
}

func TestValidateRequestedScopes(t *testing.T) {
	if _, ok := ValidateRequestedScopes([]string{"gapi.full", "not-a-scope"}); ok {
		t.Fatal("unknown scope should fail")
	}
	norm, ok := ValidateRequestedScopes([]string{"profile", "gapi.full"})
	if !ok || !security.ScopeContains(norm, ScopeGapiFull) {
		t.Fatalf("normalize failed: %s ok=%v", norm, ok)
	}
}

func TestDeviceCodeFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.RefreshToken{}, &model.OAuthDeviceCode{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := model.User{
		ID: uuid.New(), Username: "oauth_alice_" + uuid.NewString()[:8],
		Email: "oauth_" + uuid.NewString()[:8] + "@example.com",
		PasswordHash: "x", SystemAdmin: false,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		db.Where("user_id = ?", user.ID).Delete(&model.RefreshToken{})
		db.Where("user_id = ?", user.ID).Delete(&model.OAuthDeviceCode{})
		db.Delete(&user)
	})

	cfg := config.Config{
		JWTSecret:       "test-secret-for-oauth-at-least-32b!!",
		AccessTokenTTL:  time.Minute,
		RefreshTokenTTL: time.Hour,
	}
	deps := appdeps.Deps{DB: db, Cfg: cfg}
	router := gin.New()
	if err := Register(router.Group(""), deps); err != nil {
		t.Fatalf("register: %v", err)
	}
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)

	body, _ := json.Marshal(map[string]string{
		"client_id": ClientOwlCLI,
		"scope":     "profile gapi.full offline_access",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/v1/device/code", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("device/code status %d: %s", w.Code, w.Body.String())
	}
	var dc deviceCodeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &dc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		t.Fatalf("empty codes: %+v", dc)
	}

	tokBody, _ := json.Marshal(map[string]string{
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": dc.DeviceCode,
		"client_id":   ClientOwlCLI,
	})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/oauth/v1/token", bytes.NewReader(tokBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	var errResp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["error"] != "authorization_pending" {
		t.Fatalf("expected authorization_pending, got %v body=%s", errResp, w.Body.String())
	}

	clientAccess, _, err := tokens.AccessTokenWithAudience(user.ID, security.AudienceClient)
	if err != nil {
		t.Fatalf("client token: %v", err)
	}
	approveBody, _ := json.Marshal(map[string]string{"user_code": dc.UserCode})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/oauth/v1/device/approve", bytes.NewReader(approveBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+clientAccess)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status %d: %s", w.Code, w.Body.String())
	}

	// 避开 slow_down：直接重置 last_poll_at
	db.Model(&model.OAuthDeviceCode{}).Where("user_code = ?", dc.UserCode).Update("last_poll_at", nil)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/oauth/v1/token", bytes.NewReader(tokBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("token status %d: %s", w.Code, w.Body.String())
	}
	var tr tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil {
		t.Fatalf("token decode: %v", err)
	}
	if tr.AccessToken == "" || tr.RefreshToken == "" {
		t.Fatalf("missing tokens: %+v", tr)
	}
	parsed, err := tokens.ParseAccess(tr.AccessToken)
	if err != nil {
		t.Fatalf("parse agent: %v", err)
	}
	if parsed.Audience != security.AudienceAgent || parsed.UserID != user.ID {
		t.Fatalf("bad claims: %+v", parsed)
	}
	if !security.ScopeContains(parsed.Scope, ScopeGapiFull) {
		t.Fatalf("scope missing gapi.full: %s", parsed.Scope)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/oauth/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("userinfo %d: %s", w.Code, w.Body.String())
	}
}
