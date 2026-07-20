package moderation

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/restriction"
)

// TestRoutesCompatibleWithExistingTree 冒烟：Restriction / 治理路由与既有 httpapi
// 路由形状共存时，注册期不能触发 gin 路由树冲突 panic（server.New 装配即此组合）。
func TestRoutesCompatibleWithExistingTree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	noop := func(c *gin.Context) {}
	// 模拟 httpapi 中已注册的同前缀路由形状。
	v1.GET("/guilds/:guildID/channels", noop)
	v1.PUT("/guilds/:guildID/members/:memberID/roles/:roleID", noop)
	v1.DELETE("/guilds/:guildID/members/:memberID/roles/:roleID", noop)
	v1.GET("/guilds/:guildID/permissions/@me", noop)

	deps := appdeps.Deps{
		Bus:         eventbus.New(),
		Auth:        noop,
		CurrentUser: func(*gin.Context) model.User { return model.User{} },
	}
	if err := restriction.Register(v1, deps); err != nil {
		t.Fatalf("restriction.Register 失败: %v", err)
	}
	if err := Register(v1, deps); err != nil {
		t.Fatalf("moderation.Register 失败: %v", err)
	}
}
