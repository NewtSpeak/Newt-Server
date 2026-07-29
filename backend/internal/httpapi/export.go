package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// AuthMiddleware 暴露登录校验中间件，供其他领域模块（voice/message/...）复用。
func (a *API) AuthMiddleware() gin.HandlerFunc { return a.requireAuth() }

// CurrentUser 暴露当前登录用户读取，供其他领域模块复用（必须位于 AuthMiddleware 之后）。
func CurrentUser(c *gin.Context) model.User { return currentUser(c) }
