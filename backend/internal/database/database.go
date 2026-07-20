package database

import (
	"fmt"

	"github.com/owlspeak/owl-server/backend/internal/guildseed"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/plugin/opentelemetry/tracing"
)

func Open(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{TranslateError: true})
	if err != nil {
		return nil, fmt.Errorf("连接 PostgreSQL: %w", err)
	}
	// GORM OTel 追踪：SQL 语句挂到请求 span 下；未启用 OTLP 时全局 provider 为 no-op。
	// 指标由 http 层与运行时统一收集，这里只开 tracing，避免与 SigNoz 默认面板重复。
	if err := db.Use(tracing.NewPlugin(tracing.WithoutMetrics())); err != nil {
		return nil, fmt.Errorf("启用 GORM OTel 插件: %w", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		return nil, fmt.Errorf("执行 PostgreSQL 迁移: %w", err)
	}
	if err := ensureFirstUserSystemAdmin(db); err != nil {
		return nil, fmt.Errorf("初始化系统管理员: %w", err)
	}
	// 存量回填：给内置管理员角色上线前创建的 guild 补建 managed 管理员角色（幂等）。
	if err := guildseed.EnsureManagedAdminRoles(db); err != nil {
		return nil, fmt.Errorf("回填内置管理员角色: %w", err)
	}
	return db, nil
}

func ensureFirstUserSystemAdmin(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "owl:first-system-admin").Error; err != nil {
			return err
		}
		var adminCount int64
		if err := tx.Model(&model.User{}).Where("system_admin = true").Count(&adminCount).Error; err != nil || adminCount > 0 {
			return err
		}
		var first model.User
		if err := tx.Order("created_at ASC, id ASC").First(&first).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		return tx.Model(&first).Update("system_admin", true).Error
	})
}
