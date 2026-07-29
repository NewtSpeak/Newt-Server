package guildapi

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// TestRegisterCompatibleWithExistingTree 冒烟：本包顶级 /channels/{cid} 路由
//（PATCH/DELETE、overwrites、permissions/@me）与 message/stage 等模块已有的
// 顶级频道路由形状共存时，注册期不能触发 gin 路由树冲突 panic。
func TestRegisterCompatibleWithExistingTree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	root := router.Group("/gapi/v1")
	noop := func(c *gin.Context) {}
	// 模拟同平面已注册的顶级频道路由形状（message / stage / voice 模块）。
	root.POST("/channels/:channelID/typing", noop)
	root.POST("/channels/:channelID/messages", noop)
	root.PATCH("/channels/:channelID/voice-stage", noop)
	root.POST("/channels/:channelID/stage/apply", noop)

	deps := appdeps.Deps{
		Auth:        noop,
		CurrentUser: func(*gin.Context) model.User { return model.User{} },
	}
	if err := Register(root, deps); err != nil {
		t.Fatalf("guildapi.Register 失败: %v", err)
	}
}
