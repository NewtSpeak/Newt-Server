package server

import (
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"github.com/owlspeak/owl-server/backend/internal/web"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"
)

func New(cfg config.Config, db *gorm.DB) (*gin.Engine, error) {
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	router.Use(gin.Logger(), gin.Recovery(), cors.New(cors.Config{
		AllowOrigins: []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Authorization", "Content-Type"},
	}))
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	httpapi.New(db, tokens, cfg.RefreshTokenTTL).RegisterRoutes(router.Group("/api/v1"))
	if err := web.RegisterFallback(router, cfg.Environment, cfg.FrontendDevURL); err != nil {
		return nil, err
	}
	return router, nil
}
