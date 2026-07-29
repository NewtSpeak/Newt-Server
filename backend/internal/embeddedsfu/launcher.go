// Package embeddedsfu 在 Newt-Server 启动时自动创建本机 SFU 占位并拉起 owl-sfu 子进程，
// 纳入平台默认调度池，使用户无需单独启动/接入 SFU 即可语音通话。
package embeddedsfu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfucontrol"
	"gorm.io/gorm"
)

const (
	// labelKey / labelVal 标记由本包管理的内嵌节点（重启可复用同一 node_id）。
	labelKey = "owl.role"
	labelVal = "embedded-local"

	displayName = "本地内嵌 SFU"

	// enrollment 有效期：足够首次 enroll；重启若已有证书则不再需要 token。
	enrollTTL = 2 * time.Hour

	// 监督：子进程异常退出后指数退避重启。
	restartBase = 1 * time.Second
	restartMax  = 30 * time.Second
)

// Options 启动内嵌 SFU 所需参数（由 config 映射）。
type Options struct {
	BinPath           string
	WorkDir           string // DataDir/embedded-sfu
	EnrollEndpoint    string // 如 127.0.0.1:9443
	WSSListen         string
	MediaUDPPort      int
	PublicIP          string
	AdvertiseWSSURL   string
	NoTLS             bool
	EnrollInsecure    bool
	MaxUsers          int
	AuditIngestURL    string // 可选：本地覆盖；通常靠 RegisterAck
	AuditIngestToken  string
}

// Process 已拉起的内嵌 SFU 生命周期句柄。
type Process struct {
	log    *slog.Logger
	db     *gorm.DB
	opts   Options
	nodeID uuid.UUID

	mu       sync.Mutex
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// Start 确保节点占位 + 调度开关，解析/编译 owl-sfu，拉起并后台监督。
// 失败时返回 error（调用方应记日志但不宜阻断主 API 启动）。
func Start(parent context.Context, db *gorm.DB, cfg config.Config) (*Process, error) {
	if !cfg.EmbeddedSFU {
		return nil, nil
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		dataDir = cfg.DataDir
	}
	opts := Options{
		BinPath:          cfg.EmbeddedSFUBin,
		WorkDir:          filepath.Join(dataDir, "embedded-sfu"),
		EnrollEndpoint:   cfg.SFUControlPublicEndpoint,
		WSSListen:        cfg.EmbeddedSFUWSSListen,
		MediaUDPPort:     cfg.EmbeddedSFUMediaUDP,
		PublicIP:         cfg.EmbeddedSFUPublicIP,
		AdvertiseWSSURL:  cfg.EmbeddedSFUAdvertiseWSS,
		NoTLS:            cfg.EmbeddedSFUNoTLS,
		EnrollInsecure:   true, // 本机回环 enroll 默认跳过 TLS 校验
		MaxUsers:         500,
		AuditIngestURL:   cfg.AuditIngestURL,
		AuditIngestToken: cfg.AuditIngestToken,
	}
	log := slog.Default().With("component", "embeddedsfu")
	if err := os.MkdirAll(opts.WorkDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建内嵌 SFU 工作目录失败: %w", err)
	}

	// 先清掉上一轮残留进程（air 热重载 / 异常退出后 PID 文件仍在）。
	killStalePID(log, filepath.Join(opts.WorkDir, "newt-sfu.pid"))

	bin, err := resolveBinary(log, opts.BinPath, opts.WorkDir)
	if err != nil {
		return nil, err
	}
	opts.BinPath = bin

	node, enrollToken, err := ensureNode(db, opts.WorkDir)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	p := &Process{
		log:    log,
		db:     db,
		opts:   opts,
		nodeID: node.ID,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go p.supervise(ctx, enrollToken)
	log.Info("内嵌 SFU 已开始拉起",
		"node_id", node.ID,
		"bin", opts.BinPath,
		"wss", opts.WSSListen,
		"advertise", opts.AdvertiseWSSURL,
	)
	return p, nil
}

// Stop 优雅停止子进程（SIGTERM → 等待 → SIGKILL）。幂等。
func (p *Process) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.cancel()
		<-p.done
	})
}

// NodeID 返回内嵌节点 ID（未启动则为 Nil）。
func (p *Process) NodeID() uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return p.nodeID
}

// ---------------------------------------------------------------------------
// 节点占位
// ---------------------------------------------------------------------------

func ensureNode(db *gorm.DB, workDir string) (model.SfuNode, string, error) {
	sfuData := filepath.Join(workDir, "sfu-data")
	hasCerts := fileExists(filepath.Join(sfuData, "node.crt")) && fileExists(filepath.Join(sfuData, "node.key"))

	var node model.SfuNode
	err := db.Where("labels ->> ? = ?", labelKey, labelVal).First(&node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 库中无记录时磁盘证书与新 node_id 必然不匹配，清掉强制 enroll。
		_ = os.RemoveAll(sfuData)
		return createNode(db)
	}
	if err != nil {
		return model.SfuNode{}, "", fmt.Errorf("查询内嵌 SFU 节点失败: %w", err)
	}

	// 确保持续可调度 + 平台默认池。
	_ = db.Model(&node).Updates(map[string]any{
		"enabled_for_scheduling": true,
		"platform_default":       true,
		"display_name":           displayName,
	}).Error

	// 已有证书且节点已完成接入（ENROLLED/ONLINE/DRAINING）→ 直接启动。
	// PENDING 不允许控制面连接，即使本地有证书也必须重新 enroll。
	switch node.Status {
	case model.SfuNodeEnrolled, model.SfuNodeOnline, model.SfuNodeDraining:
		if hasCerts {
			return node, "", nil
		}
	}

	// 证书缺失 / 待接入 / 已吊销 / 已禁用 → 清证书 + 重签 enrollment。
	_ = os.RemoveAll(sfuData)
	token, err := reissueEnrollment(db, &node)
	if err != nil {
		return model.SfuNode{}, "", err
	}
	return node, token, nil
}

func createNode(db *gorm.DB) (model.SfuNode, string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return model.SfuNode{}, "", fmt.Errorf("生成 enrollment token 失败: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	expires := time.Now().UTC().Add(enrollTTL)
	node := model.SfuNode{
		ID:                       uuid.New(),
		DisplayName:              displayName,
		Status:                   model.SfuNodePendingEnrollment,
		Labels:                   model.SfuLabelMap{labelKey: labelVal},
		EnabledForScheduling:     true,
		PlatformDefault:          true,
		EnrollmentTokenHash:      sfucontrol.HashEnrollmentToken(token),
		EnrollmentTokenExpiresAt: &expires,
	}
	if err := db.Create(&node).Error; err != nil {
		return model.SfuNode{}, "", fmt.Errorf("创建内嵌 SFU 节点失败: %w", err)
	}
	return node, token, nil
}

func reissueEnrollment(db *gorm.DB, node *model.SfuNode) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("生成 enrollment token 失败: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	expires := time.Now().UTC().Add(enrollTTL)
	if err := db.Model(node).Updates(map[string]any{
		"status":                      model.SfuNodePendingEnrollment,
		"enrollment_token_hash":       sfucontrol.HashEnrollmentToken(token),
		"enrollment_token_expires_at": expires,
		"cert_fingerprint":            "",
		"prev_cert_fingerprint":       "",
		"cert_not_after":              nil,
		"enabled_for_scheduling":      true,
		"platform_default":            true,
	}).Error; err != nil {
		return "", fmt.Errorf("重签发 enrollment 失败: %w", err)
	}
	node.Status = model.SfuNodePendingEnrollment
	return token, nil
}

// ---------------------------------------------------------------------------
// 监督循环
// ---------------------------------------------------------------------------

func (p *Process) supervise(ctx context.Context, firstEnrollToken string) {
	defer close(p.done)
	backoff := restartBase
	enrollToken := firstEnrollToken
	for {
		if ctx.Err() != nil {
			p.stopChild()
			return
		}
		err := p.runOnce(ctx, enrollToken)
		// 首次 enroll 成功后，后续重启用本地证书，不再传 token。
		enrollToken = ""
		if ctx.Err() != nil {
			p.stopChild()
			return
		}
		if err != nil {
			p.log.Warn("内嵌 SFU 退出，准备重启", "error", err, "backoff", backoff.String())
		} else {
			p.log.Warn("内嵌 SFU 退出（code=0），准备重启", "backoff", backoff.String())
		}
		select {
		case <-ctx.Done():
			p.stopChild()
			return
		case <-time.After(backoff):
		}
		if backoff < restartMax {
			backoff *= 2
			if backoff > restartMax {
				backoff = restartMax
			}
		}
	}
}

func (p *Process) runOnce(ctx context.Context, enrollToken string) error {
	sfuData := filepath.Join(p.opts.WorkDir, "sfu-data")
	if err := os.MkdirAll(sfuData, 0o700); err != nil {
		return err
	}
	// 若需要 enroll 但证书残留且 node_id 可能不匹配，在有 token 时清证书强制 enroll。
	if enrollToken != "" {
		// 已有同 node 证书则可复用；否则删掉以免 CSR 身份冲突。
		if !fileExists(filepath.Join(sfuData, "node.crt")) {
			_ = os.RemoveAll(sfuData)
			_ = os.MkdirAll(sfuData, 0o700)
		}
	}

	logPath := filepath.Join(p.opts.WorkDir, "newt-sfu.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("打开 SFU 日志失败: %w", err)
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, p.opts.BinPath)
	cmd.Dir = p.opts.WorkDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = p.childEnv(enrollToken)
	// 独立进程组（Unix）：服务器退出时可一并信号；Windows 走平台实现。
	setChildProcGroup(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 owl-sfu 失败: %w", err)
	}
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	_ = os.WriteFile(filepath.Join(p.opts.WorkDir, "newt-sfu.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	p.log.Info("内嵌 SFU 进程已启动", "pid", cmd.Process.Pid, "node_id", p.nodeID)

	waitErr := cmd.Wait()
	p.mu.Lock()
	p.cmd = nil
	p.mu.Unlock()
	_ = os.Remove(filepath.Join(p.opts.WorkDir, "newt-sfu.pid"))
	return waitErr
}

func (p *Process) childEnv(enrollToken string) []string {
	env := os.Environ()
	set := func(k, v string) {
		env = append(env, k+"="+v)
	}
	set("NEWTSFU_NODE_ID", p.nodeID.String())
	set("NEWTSFU_SERVER_ENROLL_ENDPOINT", p.opts.EnrollEndpoint)
	set("NEWTSFU_ENROLL_INSECURE", strconv.FormatBool(p.opts.EnrollInsecure))
	set("NEWTSFU_DATA_DIR", filepath.Join(p.opts.WorkDir, "sfu-data"))
	set("NEWTSFU_WSS_LISTEN", p.opts.WSSListen)
	set("NEWTSFU_NO_TLS", strconv.FormatBool(p.opts.NoTLS))
	set("NEWTSFU_MEDIA_UDP_PORT", strconv.Itoa(p.opts.MediaUDPPort))
	set("NEWTSFU_PUBLIC_IP", p.opts.PublicIP)
	set("NEWTSFU_ADVERTISE_WSS_URL", p.opts.AdvertiseWSSURL)
	set("NEWTSFU_MAX_USERS", strconv.Itoa(p.opts.MaxUsers))
	// 与常见手工 SFU 的 :8843 错开，降低本机端口冲突。
	set("NEWTSFU_CASCADE_LISTEN", ":8844")
	if enrollToken != "" {
		set("NEWTSFU_ENROLL_TOKEN", enrollToken)
	}
	// 审计：显式传入作为兜底（RegisterAck 也会下发）。
	if p.opts.AuditIngestURL != "" {
		set("NEWTSFU_AUDIT_INGEST_URL", p.opts.AuditIngestURL)
	}
	if p.opts.AuditIngestToken != "" {
		set("NEWTSFU_AUDIT_INGEST_TOKEN", p.opts.AuditIngestToken)
	}
	return env
}

func (p *Process) stopChild() {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	stopProcess(cmd, 8*time.Second)
	p.log.Info("内嵌 SFU 已停止")
}

// ---------------------------------------------------------------------------
// 二进制解析
// ---------------------------------------------------------------------------

func resolveBinary(log *slog.Logger, configured, workDir string) (string, error) {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	if v := os.Getenv("NEWTSFU_BIN"); v != "" {
		candidates = append(candidates, v)
	}
	// 固定落盘位置：优先使用已编译缓存。
	cached := filepath.Join(workDir, "bin", "owl-sfu")
	candidates = append(candidates, cached)
	// PATH
	if p, err := exec.LookPath("owl-sfu"); err == nil {
		candidates = append(candidates, p)
	}
	// monorepo 相对路径（从 cwd / 可执行文件旁推断）。
	candidates = append(candidates, monorepoSFUBinCandidates()...)

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode().IsRegular() {
			return c, nil
		}
	}

	// 尝试从 monorepo 源码编译到 workDir/bin/newt-sfu。
	src := findSFUModuleRoot()
	if src == "" {
		return "", fmt.Errorf("未找到 owl-sfu 可执行文件；请设置 EMBEDDED_SFU_BIN 或将 Newt-SFU 放在 monorepo 中")
	}
	out := cached
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	log.Info("正在编译内嵌 owl-sfu", "module", src, "out", out)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/owl-sfu")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("编译 owl-sfu 失败: %w\n%s", err, truncate(string(output), 2000))
	}
	return out, nil
}

func monorepoSFUBinCandidates() []string {
	var out []string
	// cwd 及上级
	wd, _ := os.Getwd()
	roots := []string{wd}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	for _, root := range roots {
		cur := root
		for i := 0; i < 6; i++ {
			out = append(out,
				filepath.Join(cur, "Newt-SFU", "owl-sfu"),
				filepath.Join(cur, "Newt-SFU", "bin", "owl-sfu"),
				filepath.Join(cur, "..", "Newt-SFU", "owl-sfu"),
				filepath.Join(cur, "..", "Newt-SFU", "bin", "owl-sfu"),
			)
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return out
}

func findSFUModuleRoot() string {
	wd, _ := os.Getwd()
	roots := []string{wd}
	if ex, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(ex))
	}
	for _, root := range roots {
		cur := root
		for i := 0; i < 6; i++ {
			candidate := filepath.Join(cur, "Newt-SFU")
			if fileExists(filepath.Join(candidate, "go.mod")) && fileExists(filepath.Join(candidate, "cmd", "owl-sfu", "main.go")) {
				return candidate
			}
			candidate = filepath.Join(cur, "..", "Newt-SFU")
			if abs, err := filepath.Abs(candidate); err == nil {
				if fileExists(filepath.Join(abs, "go.mod")) && fileExists(filepath.Join(abs, "cmd", "owl-sfu", "main.go")) {
					return abs
				}
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func killStalePID(log *slog.Logger, pidFile string) {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		_ = os.Remove(pidFile)
		return
	}
	if !processAlive(pid) {
		_ = os.Remove(pidFile)
		return
	}
	log.Info("终止残留内嵌 SFU 进程", "pid", pid)
	killPID(pid, 3*time.Second)
	_ = os.Remove(pidFile)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// 保证 logFile 满足 io.Writer（避免未使用 import）。
var _ io.Writer = (*os.File)(nil)
