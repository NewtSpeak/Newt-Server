package config

import (
	"crypto/sha256"
	"encoding/hex"
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
	// 为空时：development 环境会从 JWT_SECRET 派生稳定密钥（开箱即用）；
	// production 为空则审计上传端点关闭（返回 503）。
	AuditIngestToken string
	// AuditIngestURL 完整上传地址（PublicBaseURL + /audit-api/records），
	// 经 RegisterAck 下发给 SFU；仅只读派生字段。
	AuditIngestURL string

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

	// ---- 内嵌本地 SFU（启动时自动拉起，减轻心智负担）----
	// EmbeddedSFU 是否在进程内自动创建占位并拉起本机 owl-sfu 子进程。
	// development 默认 true；production 默认 false（可用 EMBEDDED_SFU=true 显式开启）。
	EmbeddedSFU bool
	// EmbeddedSFUBin owl-sfu 可执行文件路径；空则自动搜索 / 按需编译 monorepo 中的 Owl-SFU。
	EmbeddedSFUBin string
	// EmbeddedSFUWSSListen 内嵌 SFU 信令监听（默认 :8445）。
	EmbeddedSFUWSSListen string
	// EmbeddedSFUMediaUDP 内嵌 SFU 媒体 UDP 端口（默认 3478）。
	EmbeddedSFUMediaUDP int
	// EmbeddedSFUPublicIP 上报给客户端的媒体 IP（默认 127.0.0.1）。
	EmbeddedSFUPublicIP string
	// EmbeddedSFUAdvertiseWSS 下发给客户端的 WSS URL；空则按 PublicIP + WSS 端口推导。
	EmbeddedSFUAdvertiseWSS string
	// EmbeddedSFUNoTLS 内嵌 SFU 是否禁用信令 TLS（开发默认 true）。
	EmbeddedSFUNoTLS bool

	// ---- 贴图 / 表情包（docs 17，每服务器实例独立配置）----
	// StickerMaxFileBytes 单条贴图/表情文件大小上限（字节）。
	// 默认 50 MiB；≤0 表示不限制（实际无上限，仍受进程内存与反向代理约束）。
	StickerMaxFileBytes int64
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

		// 内嵌本地 SFU：开发默认开；二进制/端口可用环境变量覆盖。
		EmbeddedSFUBin:          os.Getenv("EMBEDDED_SFU_BIN"),
		EmbeddedSFUWSSListen:    env("EMBEDDED_SFU_WSS_LISTEN", ":8445"),
		EmbeddedSFUMediaUDP:     intEnv("EMBEDDED_SFU_MEDIA_UDP", 3478),
		EmbeddedSFUPublicIP:     env("EMBEDDED_SFU_PUBLIC_IP", "127.0.0.1"),
		EmbeddedSFUAdvertiseWSS: os.Getenv("EMBEDDED_SFU_ADVERTISE_WSS"),

		// 贴图单文件上限：默认 50 MiB；STICKER_MAX_FILE_BYTES=0 不限制。
		// 支持纯数字字节，或带单位：50m / 50mb / 512k / 1g。
		StickerMaxFileBytes: bytesEnv("STICKER_MAX_FILE_BYTES", 50<<20),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL 不能为空，Owl-Server 仅支持 PostgreSQL")
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET 至少需要 32 个字符")
	}
	// 开发环境：未显式配置时自动就绪审计上传管线（派生 token + 本机 PublicBaseURL）。
	// 生产必须显式设置 AUDIT_INGEST_TOKEN（及 PUBLIC_BASE_URL）才会开启上传。
	isDev := cfg.Environment == "development" || cfg.Environment == "dev"
	if cfg.AuditIngestToken == "" && isDev {
		cfg.AuditIngestToken = deriveAuditIngestToken(cfg.JWTSecret)
	}
	if cfg.PublicBaseURL == "" && isDev {
		cfg.PublicBaseURL = deriveDevPublicBaseURL(cfg.Address)
	}
	if cfg.AuditIngestToken != "" && cfg.PublicBaseURL != "" {
		cfg.AuditIngestURL = strings.TrimRight(cfg.PublicBaseURL, "/") + "/audit-api/records"
	}
	// 内嵌 SFU：development 默认开启；production 默认关（EMBEDDED_SFU=true 可开）。
	cfg.EmbeddedSFU = boolEnv("EMBEDDED_SFU", isDev)
	cfg.EmbeddedSFUNoTLS = boolEnv("EMBEDDED_SFU_NO_TLS", isDev || cfg.EmbeddedSFU)
	if cfg.EmbeddedSFUAdvertiseWSS == "" {
		port := listenPort(cfg.EmbeddedSFUWSSListen, "8445")
		scheme := "wss"
		if cfg.EmbeddedSFUNoTLS {
			scheme = "ws"
		}
		cfg.EmbeddedSFUAdvertiseWSS = fmt.Sprintf("%s://%s:%s/ws", scheme, cfg.EmbeddedSFUPublicIP, port)
	}
	return cfg, nil
}

// listenPort 从 ":8445" / "0.0.0.0:8445" 取出端口号。
func listenPort(listen, fallback string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return fallback
	}
	if i := strings.LastIndex(listen, ":"); i >= 0 && i+1 < len(listen) {
		return listen[i+1:]
	}
	return fallback
}

// deriveAuditIngestToken 由 JWT_SECRET 派生稳定的 32 字符 hex 密钥（跨重启一致，
// 便于开发环境 SFU 经 RegisterAck 自动对齐，无需双端手写同一 secret）。
func deriveAuditIngestToken(jwtSecret string) string {
	sum := sha256.Sum256([]byte("owl-audit-ingest-v1\n" + jwtSecret))
	return hex.EncodeToString(sum[:16])
}

// deriveDevPublicBaseURL 从 APP_ADDRESS 推导本机 HTTP 根地址（仅 development）。
func deriveDevPublicBaseURL(address string) string {
	hostPort := strings.TrimSpace(address)
	if hostPort == "" {
		return "http://127.0.0.1:8080"
	}
	// ":8080" → "127.0.0.1:8080"；已含 host 则原样使用。
	if strings.HasPrefix(hostPort, ":") {
		hostPort = "127.0.0.1" + hostPort
	}
	return "http://" + hostPort
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

// bytesEnv 解析字节大小环境变量。
// 支持：纯数字（字节）、可选后缀 k/kb/m/mb/g/gb（十进制 1000 与二进制 KiB 均接受为 1024 倍数）。
// 未设置时用 fallback；解析失败时也回退 fallback。
func bytesEnv(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if n, err := parseByteSize(raw); err == nil {
		return n
	}
	return fallback
}

func parseByteSize(raw string) (int64, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	// 纯数字（允许 0 = 不限制；负数非法）
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("negative byte size")
		}
		return n, nil
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "kib"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "kib")
	case strings.HasSuffix(s, "mib"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "mib")
	case strings.HasSuffix(s, "gib"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "gib")
	case strings.HasSuffix(s, "kb"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "kb")
	case strings.HasSuffix(s, "mb"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "mb")
	case strings.HasSuffix(s, "gb"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "gb")
	case strings.HasSuffix(s, "k"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "g"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "g")
	}
	s = strings.TrimSpace(s)
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid byte size %q", raw)
	}
	return int64(n * float64(mult)), nil
}
