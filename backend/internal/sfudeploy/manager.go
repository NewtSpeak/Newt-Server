package sfudeploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/secretstore"
	"github.com/newtspeak/newt-server/backend/internal/sfucontrol"
	"gorm.io/gorm"
)

var (
	// ErrDeployInProgress 同一台主机已有部署在跑。
	ErrDeployInProgress = errors.New("该服务器已有部署任务在进行中")
	// ErrNotRunning 目标部署不在运行中（无法取消）。
	ErrNotRunning = errors.New("该部署已结束")
)

// Manager 部署任务编排器：并发互斥、生命周期管理与凭据存取。
type Manager struct {
	db       *gorm.DB
	bus      *eventbus.Bus
	cfg      config.Config
	registry *sfucontrol.Registry
	cipher   *CredentialCipher

	mu     sync.Mutex
	active map[string]uuid.UUID  // host:port → deployment id
	cancel map[uuid.UUID]func()  // deployment id → cancel
}

// NewManager 构造编排器并把上次进程遗留的 RUNNING 记录标记为失败。
func NewManager(db *gorm.DB, bus *eventbus.Bus, cfg config.Config, registry *sfucontrol.Registry, secrets secretstore.Store) (*Manager, error) {
	cipher, err := LoadCredentialCipher(secrets)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		db: db, bus: bus, cfg: cfg, registry: registry, cipher: cipher,
		active: map[string]uuid.UUID{},
		cancel: map[uuid.UUID]func(){},
	}
	m.failStaleDeployments()
	return m, nil
}

// Cipher 暴露凭据加解密器给 API 层保存/读取已存服务器。
func (m *Manager) Cipher() *CredentialCipher { return m.cipher }

// DB 供 API 层复用同一连接。
func (m *Manager) DB() *gorm.DB { return m.db }

// failStaleDeployments 进程重启后无人推进的 RUNNING 记录置为失败。
func (m *Manager) failStaleDeployments() {
	now := time.Now().UTC()
	err := m.db.Model(&model.SfuNodeDeployment{}).
		Where("status = ?", model.SfuDeployRunning).
		Updates(map[string]any{
			"status":      model.SfuDeployFailed,
			"error_msg":   "Newt-Server 重启，部署中断；远端脚本幂等，可直接重试",
			"finished_at": now,
		}).Error
	if err != nil {
		slog.Error("sfudeploy: 清理遗留部署记录失败", "error", err)
	}
}

// PreflightCheck 单项环境检查结果。
type PreflightCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"` // ok | warn | error
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// PreflightReport 发起部署前的服务端环境体检。
type PreflightReport struct {
	OK     bool             `json:"ok"`
	Checks []PreflightCheck `json:"checks"`
}

// PreflightReport 逐项检查部署所需的服务端配置，供管理台展示。
// 与 Preflight 共用判定逻辑，后者只关心「能不能发起」。
func (m *Manager) PreflightReport() PreflightReport {
	report := PreflightReport{OK: true}

	baseURL := strings.TrimRight(m.cfg.PublicBaseURL, "/")
	download := PreflightCheck{Key: "public_base_url", Label: "二进制下载地址", Status: "ok", Detail: baseURL}
	switch {
	case baseURL == "":
		download.Status = "error"
		download.Detail = "未配置"
		download.Hint = "目标节点需要通过 PUBLIC_BASE_URL 下载 owl-sfu 二进制，请先设置该环境变量。"
	case strings.Contains(baseURL, "127.0.0.1"), strings.Contains(baseURL, "localhost"):
		download.Status = "error"
		download.Hint = "当前是回环地址，远程服务器无法访问；请改为本 Server 的公网地址。"
	}
	report.Checks = append(report.Checks, download)

	endpoint := strings.TrimSpace(m.cfg.SFUControlPublicEndpoint)
	host, _ := splitEndpoint(endpoint)
	control := PreflightCheck{Key: "sfu_control_endpoint", Label: "节点回连地址", Status: "ok", Detail: endpoint}
	switch host {
	case "":
		control.Status = "error"
		control.Detail = "未配置"
		control.Hint = "节点需要通过 SFU_CONTROL_PUBLIC_ENDPOINT 回连控制面，请先设置该环境变量。"
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		control.Status = "error"
		control.Hint = "当前是回环地址，远程节点无法回连；请改为本 Server 的公网地址或域名。"
	}
	report.Checks = append(report.Checks, control)

	releases := PreflightCheck{Key: "releases", Label: "可用发布工件", Status: "ok"}
	linux, arches := countLinuxReleases(m.cfg.SFUReleaseDir)
	if linux == 0 {
		releases.Status = "error"
		releases.Detail = "无"
		releases.Hint = fmt.Sprintf("发布目录 %s 中没有 linux 平台的 owl-sfu 工件，请先构建并放入（文件名如 owl-sfu-0.1.0-linux-amd64）。", m.cfg.SFUReleaseDir)
	} else {
		releases.Detail = fmt.Sprintf("%d 个（%s）", linux, strings.Join(arches, " / "))
	}
	report.Checks = append(report.Checks, releases)

	for _, check := range report.Checks {
		if check.Status == "error" {
			report.OK = false
		}
	}
	return report
}

// countLinuxReleases 统计发布目录中 linux 平台的工件数量与架构集合。
func countLinuxReleases(dir string) (int, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	count := 0
	seen := map[string]bool{}
	var arches []string
	for _, entry := range entries {
		if entry.IsDir() || !releasePattern.MatchString(entry.Name()) {
			continue
		}
		if !strings.Contains(entry.Name(), "-linux-") {
			continue
		}
		count++
		arch := entry.Name()[strings.LastIndex(entry.Name(), "-")+1:]
		if !seen[arch] {
			seen[arch] = true
			arches = append(arches, arch)
		}
	}
	return count, arches
}

// Preflight 发起部署前的服务端配置校验；返回第一个阻断性问题。
func (m *Manager) Preflight() error {
	for _, check := range m.PreflightReport().Checks {
		if check.Status != "error" {
			continue
		}
		if check.Hint != "" {
			return fmt.Errorf("%s：%s", check.Label, check.Hint)
		}
		return fmt.Errorf("%s 未就绪", check.Label)
	}
	return nil
}

// Start 创建部署记录并在后台执行；返回部署 ID。
func (m *Manager) Start(req Request) (uuid.UUID, error) {
	if err := m.Preflight(); err != nil {
		return uuid.Nil, err
	}
	if req.Target.Port <= 0 {
		req.Target.Port = 22
	}
	key := fmt.Sprintf("%s:%d", req.Target.Host, req.Target.Port)

	m.mu.Lock()
	if _, busy := m.active[key]; busy {
		m.mu.Unlock()
		return uuid.Nil, ErrDeployInProgress
	}
	deploymentID := uuid.New()
	m.active[key] = deploymentID
	m.mu.Unlock()

	record := model.SfuNodeDeployment{
		ID:        deploymentID,
		ServerID:  req.ServerID,
		Host:      req.Target.Host,
		Port:      req.Target.Port,
		Username:  req.Target.Username,
		Status:    model.SfuDeployRunning,
		Step:      model.SfuDeployStepConnecting,
		Params:    paramsSnapshot(req),
		CreatedBy: req.ActorID,
	}
	if err := m.db.Create(&record).Error; err != nil {
		m.mu.Lock()
		delete(m.active, key)
		m.mu.Unlock()
		return uuid.Nil, fmt.Errorf("创建部署记录失败: %w", err)
	}

	actor := req.ActorID
	audit.Log(m.db, audit.Entry{
		ActorID:    &actor,
		ActorType:  "system_admin",
		Action:     "sfu.deploy.start",
		TargetType: "sfu_deployment",
		TargetID:   deploymentID.String(),
		Detail: map[string]any{
			"host": req.Target.Host, "port": req.Target.Port, "username": req.Target.Username,
			"display_name": req.Node.DisplayName, "tls_mode": req.Node.TLSMode, "domain": req.Node.Domain,
		},
	})

	// 部署上限 30 分钟：apt + Caddy ACME + 等待上线的最坏情况仍有余量。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	m.mu.Lock()
	m.cancel[deploymentID] = cancel
	m.mu.Unlock()

	go func() {
		defer cancel()
		m.run(ctx, deploymentID, req)
	}()
	return deploymentID, nil
}

// finish 释放主机占用与取消句柄。
func (m *Manager) finish(deploymentID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancel, deploymentID)
	for key, id := range m.active {
		if id == deploymentID {
			delete(m.active, key)
		}
	}
}

// Cancel 取消进行中的部署。
func (m *Manager) Cancel(deploymentID uuid.UUID) error {
	m.mu.Lock()
	cancel, ok := m.cancel[deploymentID]
	m.mu.Unlock()
	if !ok {
		return ErrNotRunning
	}
	cancel()
	return nil
}

// paramsSnapshot 生成部署参数快照（供失败后重试预填）。绝不含凭据。
func paramsSnapshot(req Request) model.SfuDeployParamMap {
	return model.SfuDeployParamMap{
		"display_name":      req.Node.DisplayName,
		"labels":            req.Node.Labels,
		"tls_mode":          req.Node.TLSMode,
		"domain":            req.Node.Domain,
		"tls_cert_file":     req.Node.TLSCertFile,
		"tls_key_file":      req.Node.TLSKeyFile,
		"public_ip":         req.Node.PublicIP,
		"media_udp_port":    req.Node.MediaUDPPort,
		"max_users":         req.Node.MaxUsers,
		"enable_cascade":    req.Node.EnableCascade,
		"release":           req.Node.Release,
		"enable_scheduling": req.Node.EnableScheduling,
		"configure_ufw":     req.Options.ConfigureUFW,
		"force_reinstall":   req.Options.ForceReinstall,
	}
}
