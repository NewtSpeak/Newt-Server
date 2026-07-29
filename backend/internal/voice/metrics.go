// metrics.go 迁移观测指标（docs 09 §11）：迁移次数 by reason、成功率（completed/
// failed 计数对）、各阶段耗时与总时长直方图、静音窗口（由 job 时间戳推导）、
// 重试与抢占计数。注册到 Prometheus 默认注册表，经 observability.StartMetricsServer
// 暴露 /metrics（仅内网/可配，METRICS_ADDRESS）。
package voice

import (
	"time"

	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 阶段耗时桶：迁移各段目标是秒级（docs 09 §5.2 段超时 2–8s），细分亚秒到十秒级。
var migrationSecondsBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 12, 20, 30, 60}

var (
	metricMigrationsCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "owl_voice_migrations_created_total",
		Help: "创建的迁移 job 数（by reason，docs 09 §11）。",
	}, []string{"reason"})
	metricMigrationsCompleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "owl_voice_migrations_completed_total",
		Help: "成功完成（DONE）的迁移数（by reason）；与 failed_attempts 组合得成功率。",
	}, []string{"reason"})
	metricMigrationsCanceled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "owl_voice_migrations_canceled_total",
		Help: "取消（用户离房/切频道/被抢占）的迁移数（by reason）。",
	}, []string{"reason"})
	metricMigrationFailedAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "owl_voice_migration_failed_attempts_total",
		Help: "迁移尝试失败次数（by reason；job 随后换目标重试，docs 09 K.3）。",
	}, []string{"reason"})
	metricMigrationRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "owl_voice_migration_retries_total",
		Help: "迁移重试（FAILED → QUEUED 归位）次数（by reason）。",
	}, []string{"reason"})
	metricMigrationPreemptions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "owl_voice_migration_preemptions_total",
		Help: "高优先级迁移抢占取消低优先级 job 的次数（docs 09 K.4/K.5）。",
	})
	metricMigrationPhaseSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "owl_voice_migration_phase_seconds",
		Help:    "迁移各阶段耗时（prepare/connect/cutover_cleanup，由 job 时间戳推导）。",
		Buckets: migrationSecondsBuckets,
	}, []string{"phase"})
	metricMigrationTotalSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "owl_voice_migration_total_seconds",
		Help:    "迁移总时长（job 创建 → DONE，by reason）。",
		Buckets: migrationSecondsBuckets,
	}, []string{"reason"})
	metricMigrationMuteGapSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "owl_voice_migration_mute_gap_seconds",
		Help: "静音窗口近似（job 创建（判死/触发）→ connected_at（客户端确认连上新节点），" +
			"by reason；客户端未 ack（超时自动推进）时无样本。P50/P99 由直方图导出。",
		Buckets: migrationSecondsBuckets,
	}, []string{"reason"})
)

// observeMigrationCompleted job 到达 DONE：总时长、阶段耗时与静音窗口打点。
// completedAt 为本次落库的完成时刻（job 行内 completed_at 同值）。
func observeMigrationCompleted(job model.VoiceMigrationJob, completedAt time.Time) {
	metricMigrationsCompleted.WithLabelValues(job.Reason).Inc()
	if !job.CreatedAt.IsZero() {
		metricMigrationTotalSeconds.WithLabelValues(job.Reason).
			Observe(completedAt.Sub(job.CreatedAt).Seconds())
	}
	if job.PreparedAt != nil && !job.CreatedAt.IsZero() {
		metricMigrationPhaseSeconds.WithLabelValues("prepare").
			Observe(job.PreparedAt.Sub(job.CreatedAt).Seconds())
	}
	if job.CutoverAt != nil && job.PreparedAt != nil {
		metricMigrationPhaseSeconds.WithLabelValues("connect").
			Observe(job.CutoverAt.Sub(*job.PreparedAt).Seconds())
	}
	if job.CutoverAt != nil {
		metricMigrationPhaseSeconds.WithLabelValues("cutover_cleanup").
			Observe(completedAt.Sub(*job.CutoverAt).Seconds())
	}
	if job.ConnectedAt != nil && !job.CreatedAt.IsZero() {
		metricMigrationMuteGapSeconds.WithLabelValues(job.Reason).
			Observe(job.ConnectedAt.Sub(job.CreatedAt).Seconds())
	}
}
