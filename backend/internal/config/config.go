package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment      string
	Address          string
	DatabaseURL      string
	JWTSecret        string
	AccessTokenTTL   time.Duration
	RefreshTokenTTL  time.Duration
	FrontendDevURL   string
	FrontendDistPath string
	// ControlAddress SFU mTLS 控制通道监听地址（与业务 API 分离，docs 03 §5.4）。
	ControlAddress string
	// DataDir 本地数据目录：内置 CA、Media Token 密钥、附件存储等。
	DataDir string
	// SFUControlAddress SFU 控制面 gRPC（Enrollment + Control）TLS 监听地址。
	SFUControlAddress string
	// SFUControlPublicEndpoint Enroll 响应里下发给节点的控制面对外地址。
	SFUControlPublicEndpoint string
	// SFUControlTLSSANs 控制面服务端证书的 SAN 列表（逗号分隔）。
	SFUControlTLSSANs []string
	// MediaTokenTTL Media Token 有效期（docs 协议 §1：2–5 分钟）。
	MediaTokenTTL time.Duration
	// SFUHeartbeatIntervalMS RegisterAck 下发的心跳间隔（定稿 5000ms）。
	SFUHeartbeatIntervalMS int
	// SFUEarlyDeathEnabled BI.3 提前判死开关（docs 15 §5，默认开）。
	SFUEarlyDeathEnabled bool
	// SFUEarlyDeathMinSources 提前判死所需独立信号源下限（BI.3 定稿 2）。
	SFUEarlyDeathMinSources int
	// SFUEarlyDeathMinHeartbeatMisses 提前判死所需心跳丢失次数下限（BI.3 定稿 1）。
	SFUEarlyDeathMinHeartbeatMisses int
	// SFUEarlyDeathSignalTTL 提前信号有效窗口（过期不计入组合规则）。
	SFUEarlyDeathSignalTTL time.Duration
	// PublicBaseURL 对外可访问的服务根地址（如 https://chat.example.com），
	// 用于拼接邀请分享链接与深链参数；为空时按请求 Host 推导。
	PublicBaseURL string
	// AuditIngestToken SFU 节点上传审计录音（/audit-api）用的共享密钥（Bearer）。
	// 为空时审计上传端点关闭（返回 503），录音功能不可用。
	AuditIngestToken string

	// ---- 过载自动迁移（docs 09 I.3–I.5，默认关）----
	// VoiceOverloadAutoMigrate 自动迁移开关（I.3 定稿默认关，系统管/部署可开）。
	VoiceOverloadAutoMigrate bool
	// VoiceOverloadCPUPct CPU 占用阈值（%）。
	VoiceOverloadCPUPct float64
	// VoiceOverloadBandwidthMbps 出口带宽阈值（Mbps）；≤0 不启用该维度。
	VoiceOverloadBandwidthMbps float64
	// VoiceOverloadUserRatio 并发用户占 max_users 比例阈值。
	VoiceOverloadUserRatio float64
	// VoiceOverloadSustain 超阈值须持续的时长 T。
	VoiceOverloadSustain time.Duration
	// VoiceOverloadCooldown 每轮批量后的冷却时长。
	VoiceOverloadCooldown time.Duration

	// MetricsAddress Prometheus /metrics 监听地址（仅应绑定内网/本机，如
	// "127.0.0.1:9091"）；为空时不启动 metrics 端点。
	MetricsAddress string
}

func Load() (Config, error) {
	cfg := Config{
		Environment:      env("APP_ENV", "development"),
		Address:          env("APP_ADDRESS", ":8080"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		AccessTokenTTL:   durationEnv("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:  durationEnv("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		FrontendDevURL:   env("FRONTEND_DEV_URL", "http://127.0.0.1:5173"),
		FrontendDistPath: env("FRONTEND_DIST_PATH", "web/dist"),
		ControlAddress:   env("CONTROL_ADDRESS", ":8443"),
		DataDir:          env("DATA_DIR", "./data"),

		// SFU_GRPC_ADDRESS 为规范名，SFU_CONTROL_ADDRESS 为兼容别名；
		// 默认 :9443（与 Owl-SFU 默认 server_enroll_endpoint 对齐）。
		SFUControlAddress:        env("SFU_GRPC_ADDRESS", env("SFU_CONTROL_ADDRESS", ":9443")),
		SFUControlPublicEndpoint: env("SFU_CONTROL_PUBLIC_ENDPOINT", "127.0.0.1:9443"),
		SFUControlTLSSANs:        splitCSV(env("SFU_CONTROL_TLS_SANS", "localhost,127.0.0.1")),
		// docs 协议 §1：TTL 2–5 分钟，默认取 3 分钟。
		MediaTokenTTL:          durationEnv("MEDIA_TOKEN_TTL", 3*time.Minute),
		SFUHeartbeatIntervalMS: intEnv("SFU_HEARTBEAT_INTERVAL_MS", 5000),
		// BI.3 提前判死（docs 15 §5）：默认开启，阈值可调。
		SFUEarlyDeathEnabled:            boolEnv("SFU_EARLY_DEATH_ENABLED", true),
		SFUEarlyDeathMinSources:         intEnv("SFU_EARLY_DEATH_MIN_SOURCES", 2),
		SFUEarlyDeathMinHeartbeatMisses: intEnv("SFU_EARLY_DEATH_MIN_HEARTBEAT_MISSES", 1),
		SFUEarlyDeathSignalTTL:          durationEnv("SFU_EARLY_DEATH_SIGNAL_TTL", 30*time.Second),
		PublicBaseURL:                   strings.TrimRight(env("PUBLIC_BASE_URL", ""), "/"),
		AuditIngestToken:                os.Getenv("AUDIT_INGEST_TOKEN"),

		// 过载自动迁移（docs 09 I.3–I.5）：默认关，阈值/节奏可调。
		VoiceOverloadAutoMigrate:   boolEnv("VOICE_OVERLOAD_AUTO_MIGRATE", false),
		VoiceOverloadCPUPct:        floatEnv("VOICE_OVERLOAD_CPU_PCT", 85),
		VoiceOverloadBandwidthMbps: floatEnv("VOICE_OVERLOAD_BANDWIDTH_MBPS", 0),
		VoiceOverloadUserRatio:     floatEnv("VOICE_OVERLOAD_USER_RATIO", 0.90),
		VoiceOverloadSustain:       durationEnv("VOICE_OVERLOAD_SUSTAIN", 30*time.Second),
		VoiceOverloadCooldown:      durationEnv("VOICE_OVERLOAD_COOLDOWN", 60*time.Second),

		// Prometheus /metrics（docs 09 §11 迁移观测）：默认关闭；部署时应仅绑定内网。
		MetricsAddress: env("METRICS_ADDRESS", ""),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL 不能为空，Owl-Server 仅支持 PostgreSQL")
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET 至少需要 32 个字符")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func boolEnv(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func floatEnv(key string, fallback float64) float64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if value, err := time.ParseDuration(value); err == nil {
		return value
	}
	return fallback
}
