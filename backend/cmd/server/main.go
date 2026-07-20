// Package main Owl-Server 控制面 API。
// @title Owl-Server API
// @version 0.1.0
// @description OwlSpeak 账号、Guild RBAC 与权限管理 API。
// @BasePath /api/v1
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
package main

import (
	"log"

	_ "github.com/owlspeak/owl-server/backend/docs"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/database"
	"github.com/owlspeak/owl-server/backend/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	router, err := server.New(cfg, db)
	if err != nil {
		log.Fatal(err)
	}
	if err := router.Run(cfg.Address); err != nil {
		log.Fatal(err)
	}
}
