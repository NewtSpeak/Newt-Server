package audit

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// StartRetention 按 AUDIT_RETENTION_DAYS 环境变量启动每小时一次的过期审计清理。
// 默认 0（或未配置 / 非法值）表示永久保留，不启动任务。
func StartRetention(db *gorm.DB) {
	days := 0
	if raw := os.Getenv("AUDIT_RETENTION_DAYS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if days == 0 {
		return
	}
	slog.Info("audit: 保留策略已启用", "retention_days", days)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			result := db.Where("created_at < ?", cutoff).Delete(&model.AuditLog{})
			if result.Error != nil {
				slog.Error("audit: 清理过期审计失败", "error", result.Error)
			} else if result.RowsAffected > 0 {
				slog.Info("audit: 清理过期审计", "deleted", result.RowsAffected, "cutoff", cutoff)
			}
			<-ticker.C
		}
	}()
}
