package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfudeploy"
	"gorm.io/gorm"
)

// AttachSfuDeploy 注入 SFU 自动部署编排器；未注入时相关路由返回 503。
func (a *API) AttachSfuDeploy(mgr *sfudeploy.Manager) { a.sfuDeploy = mgr }

func (a *API) requireSfuDeploy(c *gin.Context) bool {
	if a.sfuDeploy == nil {
		fail(c, http.StatusServiceUnavailable, "SFU_DEPLOY_UNAVAILABLE", "SFU 自动部署子系统未启用")
		return false
	}
	return true
}

// getSfuDeployPreflight GET /admin/sfu/deploy-preflight：部署前的服务端环境体检。
func (a *API) getSfuDeployPreflight(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	c.JSON(http.StatusOK, a.sfuDeploy.PreflightReport())
}

// ---- 已保存的部署目标服务器 ----

type deployServerSummary struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Host               string    `json:"host"`
	Port               int       `json:"port"`
	Username           string    `json:"username"`
	AuthMethod         string    `json:"auth_method"`
	HostKeyFingerprint string    `json:"host_key_fingerprint"`
	CreatedAt          time.Time `json:"created_at"`
}

func toDeployServerSummary(s model.SfuDeployServer) deployServerSummary {
	return deployServerSummary{
		ID: s.ID, Name: s.Name, Host: s.Host, Port: s.Port, Username: s.Username,
		AuthMethod: s.AuthMethod, HostKeyFingerprint: s.HostKeyFingerprint, CreatedAt: s.CreatedAt,
	}
}

// sshConnectionInput 表单中的 SSH 连接信息；密码/私钥仅用于本次请求与加密保存。
type sshConnectionInput struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	AuthMethod string `json:"auth_method"` // password | private_key
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
	// SudoPassword 非 root 且需要密码 sudo 时使用；留空则复用登录密码。
	SudoPassword string `json:"sudo_password"`
	// SaveAs 非空时把这台服务器与凭据加密保存，供后续复用。
	SaveAs string `json:"save_as"`
}

func (in sshConnectionInput) credential() sfudeploy.Credential {
	return sfudeploy.Credential{
		Password:     in.Password,
		PrivateKey:   in.PrivateKey,
		Passphrase:   in.Passphrase,
		SudoPassword: in.SudoPassword,
	}
}

func (in *sshConnectionInput) normalize() error {
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	if in.Host == "" {
		return errors.New("host 不能为空")
	}
	if in.Username == "" {
		in.Username = "root"
	}
	if in.Port <= 0 {
		in.Port = 22
	}
	if in.Port > 65535 {
		return errors.New("port 非法")
	}
	switch in.AuthMethod {
	case "private_key":
		if strings.TrimSpace(in.PrivateKey) == "" {
			return errors.New("私钥登录必须提供 private_key")
		}
	case "password", "":
		in.AuthMethod = "password"
		if in.Password == "" {
			return errors.New("密码登录必须提供 password")
		}
	default:
		return errors.New("auth_method 仅支持 password 或 private_key")
	}
	return nil
}

// listSfuDeployServers GET /admin/sfu/deploy-servers
func (a *API) listSfuDeployServers(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	var servers []model.SfuDeployServer
	if err := a.db.Order("created_at DESC").Find(&servers).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取部署服务器失败")
		return
	}
	out := make([]deployServerSummary, 0, len(servers))
	for _, s := range servers {
		out = append(out, toDeployServerSummary(s))
	}
	c.JSON(http.StatusOK, out)
}

// createSfuDeployServer POST /admin/sfu/deploy-servers：保存目标服务器与加密凭据。
func (a *API) createSfuDeployServer(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	var input sshConnectionInput
	if !bind(c, &input) {
		return
	}
	if err := input.normalize(); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	name := strings.TrimSpace(input.SaveAs)
	if name == "" {
		name = input.Host
	}
	server, err := a.saveDeployServer(name, input)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	actor := currentUser(c).ID
	audit.Log(a.db, audit.Entry{
		ActorID: &actor, ActorType: "system_admin",
		Action: "sfu.deploy_server.create", TargetType: "sfu_deploy_server", TargetID: server.ID.String(),
		Detail: map[string]any{"host": server.Host, "port": server.Port, "username": server.Username},
	})
	c.JSON(http.StatusCreated, toDeployServerSummary(server))
}

// saveDeployServer 按 host+port+username 幂等写入（重复保存即更新凭据）。
func (a *API) saveDeployServer(name string, input sshConnectionInput) (model.SfuDeployServer, error) {
	sealed, err := a.sfuDeploy.Cipher().Encrypt(input.credential())
	if err != nil {
		return model.SfuDeployServer{}, err
	}
	var server model.SfuDeployServer
	err = a.db.Where("host = ? AND port = ? AND username = ?", input.Host, input.Port, input.Username).
		First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		server = model.SfuDeployServer{
			ID: uuid.New(), Name: name, Host: input.Host, Port: input.Port,
			Username: input.Username, AuthMethod: input.AuthMethod, EncryptedCredential: sealed,
		}
		if err := a.db.Create(&server).Error; err != nil {
			return model.SfuDeployServer{}, err
		}
		return server, nil
	}
	if err != nil {
		return model.SfuDeployServer{}, err
	}
	updates := map[string]any{
		"name": name, "auth_method": input.AuthMethod, "encrypted_credential": sealed,
	}
	if err := a.db.Model(&server).Updates(updates).Error; err != nil {
		return model.SfuDeployServer{}, err
	}
	server.Name, server.AuthMethod, server.EncryptedCredential = name, input.AuthMethod, sealed
	return server, nil
}

// deleteSfuDeployServer DELETE /admin/sfu/deploy-servers/:serverID
func (a *API) deleteSfuDeployServer(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	var server model.SfuDeployServer
	if err := a.db.First(&server, "id = ?", c.Param("serverID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器记录不存在")
		return
	}
	if err := a.db.Delete(&server).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除失败")
		return
	}
	actor := currentUser(c).ID
	audit.Log(a.db, audit.Entry{
		ActorID: &actor, ActorType: "system_admin",
		Action: "sfu.deploy_server.delete", TargetType: "sfu_deploy_server", TargetID: server.ID.String(),
		Detail: map[string]any{"host": server.Host},
	})
	c.Status(http.StatusNoContent)
}

// ---- 部署任务 ----

type deployNodeInput struct {
	DisplayName      string            `json:"display_name"`
	Labels           map[string]string `json:"labels"`
	TLSMode          string            `json:"tls_mode"`
	Domain           string            `json:"domain"`
	TLSCertFile      string            `json:"tls_cert_file"`
	TLSKeyFile       string            `json:"tls_key_file"`
	PublicIP         string            `json:"public_ip"`
	MediaUDPPort     int               `json:"media_udp_port"`
	MaxUsers         int               `json:"max_users"`
	EnableCascade    bool              `json:"enable_cascade"`
	Release          string            `json:"release"`
	EnableScheduling *bool             `json:"enable_scheduling"`
}

type deployOptionsInput struct {
	ConfigureUFW    *bool `json:"configure_ufw"`
	ForceReinstall  bool  `json:"force_reinstall"`
	TrustNewHostKey bool  `json:"trust_new_hostkey"`
}

type createDeploymentRequest struct {
	// ServerID 使用已保存的服务器凭据；与 Connection 二选一。
	ServerID   string              `json:"server_id"`
	Connection *sshConnectionInput `json:"connection"`
	Node       deployNodeInput     `json:"node"`
	Options    deployOptionsInput  `json:"options"`
}

type deploymentSummary struct {
	ID        uuid.UUID      `json:"id"`
	ServerID  *uuid.UUID     `json:"server_id,omitempty"`
	NodeID    *uuid.UUID     `json:"node_id,omitempty"`
	Host      string         `json:"host"`
	Port      int            `json:"port"`
	Username  string         `json:"username"`
	Status    string         `json:"status"`
	Step      string         `json:"step"`
	Error     string         `json:"error,omitempty"`
	Params    map[string]any `json:"params"`
	CreatedBy uuid.UUID      `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func toDeploymentSummary(d model.SfuNodeDeployment) deploymentSummary {
	params := map[string]any{}
	for k, v := range d.Params {
		params[k] = v
	}
	return deploymentSummary{
		ID: d.ID, ServerID: d.ServerID, NodeID: d.NodeID, Host: d.Host, Port: d.Port,
		Username: d.Username, Status: d.Status, Step: d.Step, Error: d.ErrorMsg,
		Params: params, CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// createSfuDeployment POST /admin/sfu/deployments：发起一键部署。
func (a *API) createSfuDeployment(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	var input createDeploymentRequest
	if !bind(c, &input) {
		return
	}

	var (
		conn     sshConnectionInput
		cred     sfudeploy.Credential
		serverID *uuid.UUID
		expectFP string
	)
	switch {
	case strings.TrimSpace(input.ServerID) != "":
		id, err := uuid.Parse(strings.TrimSpace(input.ServerID))
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "server_id 非法")
			return
		}
		var server model.SfuDeployServer
		if err := a.db.First(&server, "id = ?", id).Error; err != nil {
			fail(c, http.StatusNotFound, "NOT_FOUND", "已保存的服务器不存在")
			return
		}
		cred, err = a.sfuDeploy.Cipher().Decrypt(server.EncryptedCredential)
		if err != nil {
			fail(c, http.StatusInternalServerError, "CREDENTIAL_ERROR", err.Error())
			return
		}
		conn = sshConnectionInput{Host: server.Host, Port: server.Port, Username: server.Username, AuthMethod: server.AuthMethod}
		serverID = &server.ID
		expectFP = server.HostKeyFingerprint
	case input.Connection != nil:
		conn = *input.Connection
		if err := conn.normalize(); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		cred = conn.credential()
		// 命中已保存的同一台机器时沿用其指纹做 TOFU 比对。
		var existing model.SfuDeployServer
		if err := a.db.Where("host = ? AND port = ? AND username = ?", conn.Host, conn.Port, conn.Username).
			First(&existing).Error; err == nil {
			expectFP = existing.HostKeyFingerprint
		}
		if strings.TrimSpace(conn.SaveAs) != "" {
			server, err := a.saveDeployServer(strings.TrimSpace(conn.SaveAs), conn)
			if err != nil {
				fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存服务器凭据失败: "+err.Error())
				return
			}
			serverID = &server.ID
			expectFP = server.HostKeyFingerprint
		}
	default:
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "必须提供 server_id 或 connection")
		return
	}

	configureUFW := true
	if input.Options.ConfigureUFW != nil {
		configureUFW = *input.Options.ConfigureUFW
	}
	enableScheduling := true
	if input.Node.EnableScheduling != nil {
		enableScheduling = *input.Node.EnableScheduling
	}

	req := sfudeploy.Request{
		Target: sfudeploy.Target{
			Host: conn.Host, Port: conn.Port, Username: conn.Username,
			ExpectedFingerprint: expectFP, TrustNewHostKey: input.Options.TrustNewHostKey,
		},
		Credential: cred,
		Node: sfudeploy.NodeSpec{
			DisplayName: input.Node.DisplayName, Labels: input.Node.Labels,
			TLSMode: input.Node.TLSMode, Domain: input.Node.Domain,
			TLSCertFile: input.Node.TLSCertFile, TLSKeyFile: input.Node.TLSKeyFile,
			PublicIP: input.Node.PublicIP, MediaUDPPort: input.Node.MediaUDPPort,
			MaxUsers: input.Node.MaxUsers, EnableCascade: input.Node.EnableCascade,
			Release: input.Node.Release, EnableScheduling: enableScheduling,
		},
		Options: sfudeploy.Options{
			ConfigureUFW:    configureUFW,
			ForceReinstall:  input.Options.ForceReinstall,
			TrustNewHostKey: input.Options.TrustNewHostKey,
		},
		ServerID: serverID,
		ActorID:  currentUser(c).ID,
	}

	deploymentID, err := a.sfuDeploy.Start(req)
	if err != nil {
		switch {
		case errors.Is(err, sfudeploy.ErrDeployInProgress):
			fail(c, http.StatusConflict, "DEPLOY_IN_PROGRESS", err.Error())
		default:
			fail(c, http.StatusBadRequest, "DEPLOY_PRECONDITION_FAILED", err.Error())
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"deployment_id": deploymentID})
}

// listSfuDeployments GET /admin/sfu/deployments
func (a *API) listSfuDeployments(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	var records []model.SfuNodeDeployment
	if err := a.db.Order("created_at DESC").Limit(limit).Find(&records).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取部署记录失败")
		return
	}
	out := make([]deploymentSummary, 0, len(records))
	for _, r := range records {
		out = append(out, toDeploymentSummary(r))
	}
	c.JSON(http.StatusOK, out)
}

// getSfuDeployment GET /admin/sfu/deployments/:deploymentID?log_offset=N
// log_offset 为客户端已收到的累计字节数；服务端据此判断是否需要回传全量日志。
func (a *API) getSfuDeployment(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	var record model.SfuNodeDeployment
	if err := a.db.First(&record, "id = ?", c.Param("deploymentID")).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "部署记录不存在")
		return
	}
	summary := toDeploymentSummary(record)
	c.JSON(http.StatusOK, gin.H{
		"deployment": summary,
		"log":        record.Log,
		"log_offset": len(record.Log),
	})
}

// cancelSfuDeployment POST /admin/sfu/deployments/:deploymentID/cancel
func (a *API) cancelSfuDeployment(c *gin.Context) {
	if !a.requireSfuDeploy(c) {
		return
	}
	id, err := uuid.Parse(c.Param("deploymentID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "部署 ID 非法")
		return
	}
	if err := a.sfuDeploy.Cancel(id); err != nil {
		fail(c, http.StatusConflict, "DEPLOY_NOT_RUNNING", err.Error())
		return
	}
	actor := currentUser(c).ID
	audit.Log(a.db, audit.Entry{
		ActorID: &actor, ActorType: "system_admin",
		Action: "sfu.deploy.cancel", TargetType: "sfu_deployment", TargetID: id.String(),
	})
	c.Status(http.StatusAccepted)
}
