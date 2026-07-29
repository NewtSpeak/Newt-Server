package sfudeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfucontrol"
	"gorm.io/gorm"
)

// NodeSpec 目标节点的期望配置（来自部署表单）。
type NodeSpec struct {
	DisplayName   string
	Labels        map[string]string
	TLSMode       string
	Domain        string
	TLSCertFile   string
	TLSKeyFile    string
	PublicIP      string
	MediaUDPPort  int
	MaxUsers      int
	EnableCascade bool
	Release       string
	// EnableScheduling 部署成功后立即开启调度。
	EnableScheduling bool
}

// Options 部署可选行为。
type Options struct {
	ConfigureUFW    bool
	ForceReinstall  bool
	TrustNewHostKey bool
}

// Request 一次部署请求（凭据不落库，仅在内存中传递）。
type Request struct {
	Target     Target
	Credential Credential
	Node       NodeSpec
	Options    Options
	ServerID   *uuid.UUID
	ActorID    uuid.UUID
}

// waitOnlineTimeout 等待节点 enroll 并建立控制通道的上限。
const waitOnlineTimeout = 3 * time.Minute

// phase1OKPattern 解析 phase1 的哨兵行（回传架构与探测到的公网 IP）。
var phase1OKPattern = regexp.MustCompile(`^PHASE1_OK arch=(\S+) public_ip=(\S*)$`)

// run 执行完整部署状态机。任一步失败即写入 FAILED 并返回。
func (m *Manager) run(ctx context.Context, deploymentID uuid.UUID, req Request) {
	buf := newLogBuffer()
	state := &runState{
		manager:      m,
		deploymentID: deploymentID,
		actorID:      req.ActorID,
		buf:          buf,
	}
	defer m.finish(deploymentID)

	err := state.execute(ctx, req)
	if err == nil {
		state.setStep(model.SfuDeployStepDone)
		state.complete(model.SfuDeploySucceeded, "")
		return
	}
	if ctx.Err() != nil {
		state.logf("部署已取消")
		state.complete(model.SfuDeployCanceled, "部署已被取消")
		return
	}
	state.logf("部署失败：%v", err)
	state.cleanupPendingNode()
	state.complete(model.SfuDeployFailed, err.Error())
}

// runState 单次部署的可变状态与日志/事件出口。
type runState struct {
	manager      *Manager
	deploymentID uuid.UUID
	actorID      uuid.UUID
	buf          *logBuffer
	step         string
	nodeID       *uuid.UUID
	// nodeEnrolled 节点是否已完成 enroll（失败时不再自动删除）。
	nodeEnrolled bool
	lastFlush    time.Time
	pendingBytes int
}

func (s *runState) db() *gorm.DB { return s.manager.db }

// logf 追加一行日志：写入缓冲、按节流批量落库并推送 Gateway 事件。
func (s *runState) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	offset := s.buf.Append(line)
	s.pendingBytes += len(line) + 1
	// 每 500ms 或 4KB flush 一次，避免逐行写库/发事件。
	if time.Since(s.lastFlush) < 500*time.Millisecond && s.pendingBytes < 4096 {
		return
	}
	s.flush(offset)
}

func (s *runState) flush(offset int) {
	s.lastFlush = time.Now()
	s.pendingBytes = 0
	content, total := s.buf.Snapshot()
	if offset == 0 {
		offset = total
	}
	s.db().Model(&model.SfuNodeDeployment{}).Where("id = ?", s.deploymentID).Update("log", content)
	s.publish(model.SfuDeployRunning, "", offset)
}

// setStep 切换步骤：立即落库并推送（步骤条要即时反映）。
func (s *runState) setStep(step string) {
	s.step = step
	s.db().Model(&model.SfuNodeDeployment{}).Where("id = ?", s.deploymentID).Update("step", step)
	_, offset := s.buf.Snapshot()
	s.publish(model.SfuDeployRunning, "", offset)
}

// publish 推送部署进度事件（仅发起该部署的管理员可见）。
func (s *runState) publish(status, errMsg string, offset int) {
	if s.manager.bus == nil {
		return
	}
	payload := map[string]any{
		"deployment_id": s.deploymentID.String(),
		"status":        status,
		"step":          s.step,
		"log_offset":    offset,
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	if s.nodeID != nil {
		payload["node_id"] = s.nodeID.String()
	}
	s.manager.bus.Publish(eventbus.Event{
		Type:    eventbus.EventSfuDeploymentUpdate,
		UserIDs: []uuid.UUID{s.actorID},
		Payload: payload,
	})
}

// complete 写入终态并推送最后一帧事件。
func (s *runState) complete(status, errMsg string) {
	content, offset := s.buf.Snapshot()
	now := time.Now().UTC()
	updates := map[string]any{
		"status":      status,
		"step":        s.step,
		"log":         content,
		"error_msg":   errMsg,
		"finished_at": now,
	}
	if s.nodeID != nil {
		updates["node_id"] = *s.nodeID
	}
	s.db().Model(&model.SfuNodeDeployment{}).Where("id = ?", s.deploymentID).Updates(updates)
	s.publish(status, errMsg, offset)

	detail := map[string]any{"deployment_id": s.deploymentID.String(), "status": status}
	if s.nodeID != nil {
		detail["node_id"] = s.nodeID.String()
	}
	if errMsg != "" {
		detail["error"] = errMsg
	}
	actor := s.actorID
	audit.Log(s.db(), audit.Entry{
		ActorID:    &actor,
		ActorType:  "system_admin",
		Action:     "sfu.deploy.finish",
		TargetType: "sfu_deployment",
		TargetID:   s.deploymentID.String(),
		Detail:     detail,
	})
}

// cleanupPendingNode 失败回滚：删除尚未 enroll 的占位节点，避免面板残留。
func (s *runState) cleanupPendingNode() {
	if s.nodeID == nil || s.nodeEnrolled {
		if s.nodeID != nil {
			s.logf("节点 %s 已完成 enroll，保留其记录；如需清理请在节点列表手动吊销", s.nodeID)
		}
		return
	}
	var node model.SfuNode
	if err := s.db().First(&node, "id = ?", *s.nodeID).Error; err != nil {
		return
	}
	if node.Status != model.SfuNodePendingEnrollment {
		s.nodeEnrolled = true
		return
	}
	if err := s.db().Delete(&node).Error; err == nil {
		s.logf("已清理未完成接入的占位节点 %s", node.ID)
	}
}

// execute 顺序推进各步骤。
func (s *runState) execute(ctx context.Context, req Request) error {
	cfg := s.manager.cfg

	// ---- 参数校验（进入脚本的每个值都必须过白名单）----
	spec, err := normalizeSpec(req.Node, req.Target.Host)
	if err != nil {
		return err
	}
	if err := validateShellSafe("目标主机", req.Target.Host, hostPattern); err != nil {
		return err
	}
	binaryURL, sha, releaseName, err := s.manager.resolveRelease(spec.Release)
	if err != nil {
		return err
	}
	if err := validateURL("二进制下载地址", binaryURL); err != nil {
		return err
	}
	healthURL := strings.TrimRight(cfg.PublicBaseURL, "/") + "/healthz"
	if err := validateURL("Server 健康检查地址", healthURL); err != nil {
		return err
	}
	controlHost, controlPort := splitEndpoint(cfg.SFUControlPublicEndpoint)
	if err := validateShellSafe("控制面地址", controlHost, hostPattern); err != nil {
		return err
	}

	// ---- CONNECTING ----
	s.setStep(model.SfuDeployStepConnecting)
	s.logf("正在连接 %s@%s:%d ...", req.Target.Username, req.Target.Host, req.Target.Port)
	client, err := Dial(ctx, req.Target, req.Credential)
	if err != nil {
		return err
	}
	defer client.Close()
	s.logf("SSH 已连接，主机指纹 %s", client.Fingerprint)
	s.db().Model(&model.SfuNodeDeployment{}).Where("id = ?", s.deploymentID).
		Update("params", mergeParam(s.db(), s.deploymentID, "host_key_fingerprint", client.Fingerprint))
	if req.ServerID != nil {
		s.db().Model(&model.SfuDeployServer{}).Where("id = ?", *req.ServerID).
			Update("host_key_fingerprint", client.Fingerprint)
	}

	// ---- PRECHECK ----
	s.setStep(model.SfuDeployStepPrecheck)
	if err := s.precheck(ctx, client, req.Credential); err != nil {
		return err
	}

	// ---- INSTALL_DEPS（含二进制下载）----
	s.setStep(model.SfuDeployStepInstallDeps)
	s.logf("开始安装运行环境与下载 owl-sfu（%s）...", releaseName)
	phase1, err := renderPhase1(phase1Data{
		InstallDir:      installDir,
		SSHHost:         req.Target.Host,
		ReleaseName:     releaseName,
		BinaryURL:       binaryURL,
		BinarySHA256:    sha,
		ServerHealthURL: healthURL,
		ControlHost:     controlHost,
		ControlPort:     controlPort,
		MediaUDPPort:    spec.MediaUDPPort,
		PublicIP:        spec.PublicIP,
		InstallCaddy:    spec.TLSMode == TLSModeCaddy,
		ConfigureUFW:    req.Options.ConfigureUFW,
		EnableCascade:   spec.EnableCascade,
		ForceReinstall:  req.Options.ForceReinstall,
		NoTLS:           spec.TLSMode != TLSModeCustom,
		Domain:          spec.Domain,
	})
	if err != nil {
		return err
	}
	detectedIP := spec.PublicIP
	var detectedArch string
	err = client.RunScript(ctx, phase1, func(line string) {
		if m := phase1OKPattern.FindStringSubmatch(line); m != nil {
			detectedArch = m[1]
			if m[2] != "" {
				detectedIP = m[2]
			}
			return
		}
		s.logf("%s", line)
	})
	if err != nil {
		return fmt.Errorf("安装运行环境失败: %w", err)
	}
	if detectedArch == "" {
		return fmt.Errorf("安装脚本未正常结束（未收到完成标记）")
	}
	if err := s.manager.checkArchMatch(releaseName, detectedArch); err != nil {
		return err
	}
	if detectedIP == "" {
		detectedIP = req.Target.Host
	}
	if err := validateShellSafe("公网 IP", detectedIP, hostPattern); err != nil {
		return err
	}
	spec.PublicIP = detectedIP
	s.logf("运行环境就绪（架构 %s，公网 IP %s）", detectedArch, detectedIP)

	// ---- CREATE_NODE ----
	// 放在耗时步骤之后：enrollment token 只有 30 分钟有效期，此处签发可把暴露窗口压到 1 分钟内。
	s.setStep(model.SfuDeployStepCreateNode)
	node, token, _, err := sfucontrol.CreateNodeWithEnrollment(s.db(), spec.DisplayName, spec.Labels)
	if err != nil {
		return err
	}
	s.nodeID = &node.ID
	s.db().Model(&model.SfuNodeDeployment{}).Where("id = ?", s.deploymentID).Update("node_id", node.ID)
	s.logf("已创建节点占位 %s（%s），并签发一次性接入凭证", node.ID, spec.DisplayName)

	// ---- CONFIGURE + START ----
	s.setStep(model.SfuDeployStepConfigure)
	wssListen, localPort, advertise := spec.endpoints()
	phase2, err := renderPhase2(phase2Data{
		InstallDir:      installDir,
		NodeID:          node.ID.String(),
		EnrollToken:     token,
		ControlEndpoint: cfg.SFUControlPublicEndpoint,
		// 控制面使用内置 CA 自签证书，首次 enroll 必须跳过 TLS 校验；
		// enroll 成功后节点持 CA bundle，后续控制通道走完整 mTLS 校验。
		EnrollInsecure:  true,
		WSSListen:       wssListen,
		LocalWSSPort:    localPort,
		NoTLS:           spec.TLSMode != TLSModeCustom,
		TLSCertFile:     spec.TLSCertFile,
		TLSKeyFile:      spec.TLSKeyFile,
		MediaUDPPort:    spec.MediaUDPPort,
		PublicIP:        spec.PublicIP,
		AdvertiseWssURL: advertise,
		MaxUsers:        spec.MaxUsers,
		EnableCascade:   spec.EnableCascade,
		InstallCaddy:    spec.TLSMode == TLSModeCaddy,
		ForceReinstall:  req.Options.ForceReinstall,
		Domain:          spec.Domain,
	})
	if err != nil {
		return err
	}
	sawPhase2OK := false
	err = client.RunScript(ctx, phase2, func(line string) {
		if strings.TrimSpace(line) == "PHASE2_OK" {
			sawPhase2OK = true
			return
		}
		s.logf("%s", line)
	})
	if err != nil {
		s.disableRemoteService(client)
		return fmt.Errorf("配置并启动 owl-sfu 失败: %w", err)
	}
	if !sawPhase2OK {
		s.disableRemoteService(client)
		return fmt.Errorf("owl-sfu 启动脚本未正常结束")
	}
	s.logf("owl-sfu 已在目标机启动，等待其接入本 Server ...")

	// ---- WAIT_ONLINE ----
	s.setStep(model.SfuDeployStepWaitOnline)
	if err := s.waitOnline(ctx, node.ID); err != nil {
		return err
	}

	// ---- ENABLE_SCHEDULING ----
	if spec.EnableScheduling {
		s.setStep(model.SfuDeployStepEnableScheduling)
		if err := s.db().Model(&model.SfuNode{}).Where("id = ?", node.ID).
			Update("enabled_for_scheduling", true).Error; err != nil {
			return fmt.Errorf("开启调度失败: %w", err)
		}
		s.logf("已开启该节点的调度")
	}
	s.logf("部署完成：节点 %s 已上线，客户端接入地址 %s", spec.DisplayName, advertise)
	return nil
}

// precheck 探测目标机权限并按需启用 sudo 提权。
func (s *runState) precheck(ctx context.Context, client *Client, cred Credential) error {
	uid, err := client.Run(ctx, "id -u")
	if err != nil {
		return fmt.Errorf("执行远程命令失败（目标机可能不是 Linux）: %w", err)
	}
	uid = strings.TrimSpace(uid)
	if uid == "0" {
		s.logf("登录用户为 root，无需提权")
		return nil
	}
	s.logf("登录用户非 root（uid=%s），检查 sudo 权限 ...", uid)
	if _, err := client.Run(ctx, "sudo -n true"); err == nil {
		s.logf("已确认免密 sudo 可用")
		client.EnablePrivilegeEscalation("")
		return nil
	}
	secret := cred.SudoSecret()
	if secret == "" {
		return fmt.Errorf("该用户不是 root 且未配置免密 sudo；请改用 root 登录、配置 NOPASSWD sudo，或在表单中填写 sudo 密码")
	}
	if out, err := client.RunWithStdin(ctx, "sudo -S -p '' true", secret+"\n"); err != nil {
		return fmt.Errorf("sudo 提权失败（密码错误或该用户无 sudo 权限）: %s", strings.TrimSpace(out))
	}
	s.logf("已通过 sudo 密码提权")
	client.EnablePrivilegeEscalation(secret)
	return nil
}

// waitOnline 轮询等待节点完成 enroll 并建立控制通道。
func (s *runState) waitOnline(ctx context.Context, nodeID uuid.UUID) error {
	deadline := time.Now().Add(waitOnlineTimeout)
	reportedEnroll := false
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		var node model.SfuNode
		if err := s.db().First(&node, "id = ?", nodeID).Error; err != nil {
			return fmt.Errorf("读取节点状态失败: %w", err)
		}
		if node.Status != model.SfuNodePendingEnrollment && !reportedEnroll {
			reportedEnroll = true
			s.nodeEnrolled = true
			s.logf("节点已完成接入认证（证书已签发），等待控制通道建立 ...")
		}
		if s.manager.registry != nil {
			if snapshot, ok := s.manager.registry.Snapshot(nodeID); ok && snapshot.Online {
				s.logf("节点控制通道已连接，状态 ONLINE")
				return nil
			}
		}
		if node.Status == model.SfuNodeOnline {
			s.logf("节点状态 ONLINE")
			return nil
		}
	}
	if s.nodeEnrolled {
		return fmt.Errorf("节点已 enroll 但控制通道未在 %.0f 秒内建立；请检查目标机到 %s 的出站连通性",
			waitOnlineTimeout.Seconds(), s.manager.cfg.SFUControlPublicEndpoint)
	}
	return fmt.Errorf("节点未在 %.0f 秒内完成接入；请检查目标机 journalctl -u owl-sfu 日志与到控制面 %s 的连通性",
		waitOnlineTimeout.Seconds(), s.manager.cfg.SFUControlPublicEndpoint)
}

// disableRemoteService 启动失败时停掉远端服务，避免留下反复重启的坏单元。
func (s *runState) disableRemoteService(client *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := "systemctl disable --now owl-sfu"
	if client.useSudo {
		cmd = "sudo -S -p '' " + cmd
	}
	if _, err := client.Run(ctx, cmd); err == nil {
		s.logf("已停用目标机上启动失败的 owl-sfu 服务")
	}
}

// normalizedSpec 校验并补全后的节点配置。
type normalizedSpec struct {
	NodeSpec
}

// endpoints 按 TLS 模式推导监听地址、本机端口与对客户端公布的信令地址。
func (s normalizedSpec) endpoints() (wssListen, localPort, advertise string) {
	switch s.TLSMode {
	case TLSModeCaddy:
		// SFU 只听本机，由 Caddy 终结 TLS 并反代。
		return "127.0.0.1:8443", "8443", "wss://" + s.Domain + "/ws"
	case TLSModeCustom:
		return "0.0.0.0:8443", "8443", "wss://" + s.Domain + ":8443/ws"
	default:
		return "0.0.0.0:8443", "8443", "ws://" + s.PublicIP + ":8443/ws"
	}
}

// normalizeSpec 校验表单参数并填充默认值。
func normalizeSpec(spec NodeSpec, sshHost string) (normalizedSpec, error) {
	spec.DisplayName = strings.TrimSpace(spec.DisplayName)
	if spec.DisplayName == "" || len(spec.DisplayName) > 100 {
		return normalizedSpec{}, fmt.Errorf("节点名称长度须为 1–100")
	}
	spec.Domain = strings.TrimSpace(spec.Domain)
	spec.PublicIP = strings.TrimSpace(spec.PublicIP)
	switch spec.TLSMode {
	case TLSModeCaddy, TLSModeCustom:
		if spec.Domain == "" {
			return normalizedSpec{}, fmt.Errorf("该 TLS 模式必须填写域名")
		}
		if err := validateShellSafe("域名", spec.Domain, domainPattern); err != nil {
			return normalizedSpec{}, err
		}
	case TLSModeNone, "":
		spec.TLSMode = TLSModeNone
		spec.Domain = ""
	default:
		return normalizedSpec{}, fmt.Errorf("未知的 TLS 模式 %q", spec.TLSMode)
	}
	if spec.TLSMode == TLSModeCustom {
		if err := validateShellSafe("证书路径", spec.TLSCertFile, pathPattern); err != nil {
			return normalizedSpec{}, err
		}
		if err := validateShellSafe("私钥路径", spec.TLSKeyFile, pathPattern); err != nil {
			return normalizedSpec{}, err
		}
	} else {
		spec.TLSCertFile, spec.TLSKeyFile = "", ""
	}
	if spec.PublicIP != "" {
		if err := validateShellSafe("公网 IP", spec.PublicIP, hostPattern); err != nil {
			return normalizedSpec{}, err
		}
	} else if spec.TLSMode == TLSModeNone {
		// 明文模式必须有确定的对外地址；无输入时先用 SSH 主机，phase1 会再探测覆盖。
		spec.PublicIP = sshHost
	}
	if spec.MediaUDPPort <= 0 {
		spec.MediaUDPPort = 3478
	}
	if spec.MediaUDPPort > 65535 {
		return normalizedSpec{}, fmt.Errorf("媒体 UDP 端口非法")
	}
	if spec.MaxUsers <= 0 {
		spec.MaxUsers = 1200
	}
	if spec.MaxUsers > 100000 {
		return normalizedSpec{}, fmt.Errorf("最大用户数超出合理范围")
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		if strings.TrimSpace(k) == "" {
			continue
		}
		labels[k] = v
	}
	spec.Labels = labels
	return normalizedSpec{spec}, nil
}

// resolveRelease 解析要下发的发布工件，返回下载 URL、SHA-256 与文件名。
// release 为空时自动选取发布目录中最新的 linux/amd64 工件。
func (m *Manager) resolveRelease(release string) (downloadURL, sha, name string, err error) {
	base := strings.TrimRight(m.cfg.PublicBaseURL, "/")
	if base == "" {
		return "", "", "", fmt.Errorf("未配置 PUBLIC_BASE_URL，节点无法下载二进制；请先设置该环境变量")
	}
	dir := m.cfg.SFUReleaseDir
	release = strings.TrimSpace(release)
	if release != "" {
		if !releasePattern.MatchString(release) {
			return "", "", "", fmt.Errorf("发布工件名非法：%q", release)
		}
	} else {
		picked, err := pickDefaultRelease(dir)
		if err != nil {
			return "", "", "", err
		}
		release = picked
	}
	path := filepath.Join(dir, release)
	sum, err := fileSHA256(path)
	if err != nil {
		return "", "", "", fmt.Errorf("读取发布工件 %s 失败：%w（请确认已放入 SFU_RELEASE_DIR=%s）", release, err, dir)
	}
	return base + "/sfu-releases/" + url.PathEscape(release), sum, release, nil
}

// checkArchMatch 目标机架构与所选工件不一致时拒绝继续（否则远端会报 Exec format error）。
func (m *Manager) checkArchMatch(release, arch string) error {
	if arch == "" {
		return nil
	}
	if strings.HasSuffix(release, "-linux-"+arch) {
		return nil
	}
	return fmt.Errorf("所选工件 %s 与目标机架构 linux/%s 不匹配；请在 SFU_RELEASE_DIR 放入对应架构的二进制后重试", release, arch)
}

// pickDefaultRelease 选发布目录中最新修改的 linux 工件。
func pickDefaultRelease(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("读取发布目录 %s 失败：%w", dir, err)
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !releasePattern.MatchString(e.Name()) || !strings.Contains(e.Name(), "-linux-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = e.Name(), info.ModTime()
		}
	}
	if best == "" {
		return "", fmt.Errorf("发布目录 %s 中没有 linux 平台的 owl-sfu 工件；请先构建并放入（文件名如 owl-sfu-0.1.0-linux-amd64）", dir)
	}
	return best, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// mergeParam 把一个键值合并进部署记录的 params（保留既有键）。
func mergeParam(db *gorm.DB, deploymentID uuid.UUID, key, value string) model.SfuDeployParamMap {
	var record model.SfuNodeDeployment
	params := model.SfuDeployParamMap{}
	if err := db.First(&record, "id = ?", deploymentID).Error; err == nil && record.Params != nil {
		params = record.Params
	}
	params[key] = value
	return params
}
