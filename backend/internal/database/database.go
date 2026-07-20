package database

import (
	"fmt"

	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Open(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{TranslateError: true})
	if err != nil {
		return nil, fmt.Errorf("连接 PostgreSQL: %w", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		return nil, fmt.Errorf("执行 PostgreSQL 迁移: %w", err)
	}
	return db, nil
}
