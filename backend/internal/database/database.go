package database

import (
	"fmt"

	"github.com/owlspeak/owl-server/backend/internal/guildseed"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
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
	// 贴图条目：早期 schema 误建 (pack_id, sort_order) UNIQUE，与「同包内 sort 可重复」冲突，
	// 导致第二张起上传全部 DATABASE_ERROR。GORM AutoMigrate 不会把 unique 降级为普通索引。
	if err := ensureStickerItemSortIndex(db); err != nil {
		return nil, fmt.Errorf("修正 sticker_items 排序索引: %w", err)
	}
	if err := ensureFirstUserSystemAdmin(db); err != nil {
		return nil, fmt.Errorf("初始化系统管理员: %w", err)
	}
	// 存量回填：给内置管理员角色上线前创建的 guild 补建 managed 管理员角色（幂等）。
	if err := guildseed.EnsureManagedAdminRoles(db); err != nil {
		return nil, fmt.Errorf("回填内置管理员角色: %w", err)
	}
	// 存量回填：everyone 角色补 USE_APPLICATION_COMMANDS（bot 交互按钮，
	// 设计文档 2026-07-26）。该位此前无任何使用点，不存在管理员刻意关闭的情形，
	// 幂等 OR 回填安全。
	if err := ensureEveryoneInteractionPermission(db); err != nil {
		return nil, fmt.Errorf("回填交互组件权限: %w", err)
	}
	return db, nil
}

func ensureEveryoneInteractionPermission(db *gorm.DB) error {
	bit := int64(rbac.UseApplicationCommands)
	return db.Exec(
		"UPDATE roles SET permissions = permissions | ? WHERE is_everyone = true AND permissions & ? = 0",
		bit, bit,
	).Error
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

// ensureStickerItemSortIndex 将 sticker_items(pack_id, sort_order) 从 UNIQUE 降为普通索引（幂等）。
func ensureStickerItemSortIndex(db *gorm.DB) error {
	// 探测是否仍为唯一索引（pg_index.indisunique）
	var isUnique bool
	err := db.Raw(`
		SELECT COALESCE(i.indisunique, false)
		FROM pg_class t
		JOIN pg_index i ON i.indrelid = t.oid
		JOIN pg_class ix ON ix.oid = i.indexrelid
		WHERE t.relname = 'sticker_items' AND ix.relname = 'idx_sticker_item_pack_sort'
		LIMIT 1
	`).Scan(&isUnique).Error
	if err != nil {
		// 表/索引尚未存在时忽略；AutoMigrate 之后通常已有表
		return nil
	}
	if !isUnique {
		// 可能索引不存在或已是非唯一：保证普通索引在
		return db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_sticker_item_pack_sort
			ON sticker_items (pack_id, sort_order)
		`).Error
	}
	// 唯一 → 删除后重建为非唯一
	if err := db.Exec(`DROP INDEX IF EXISTS idx_sticker_item_pack_sort`).Error; err != nil {
		return err
	}
	return db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sticker_item_pack_sort
		ON sticker_items (pack_id, sort_order)
	`).Error
}
