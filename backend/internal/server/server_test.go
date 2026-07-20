package server_test

// 装配冒烟测试：需要真实 PostgreSQL（TEST_DATABASE_URL，见 clientapi 集成测试头注释）。
// 目的：完整跑一遍 server.New 的生产装配（双认证平面全部模块），
// 锁定「新增/投影路由不得触发 gin 路由树冲突 panic」这一回归面。

import (
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNewAssemblesAllModules(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过装配冒烟测试")
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
		Environment: "production", JWTSecret: "server-smoke-secret-32-chars-long!!",
		AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour,
		DataDir: t.TempDir(), ControlAddress: "127.0.0.1:0", MediaTokenTTL: 3 * time.Minute,
		FrontendDistPath: t.TempDir(),
	}
	router, err := server.New(cfg, db, nil)
	if err != nil {
		t.Fatalf("server.New 装配失败: %v", err)
	}
	if router == nil {
		t.Fatal("server.New 返回空 router")
	}
}
