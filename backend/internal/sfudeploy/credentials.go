// Package sfudeploy 通过 SSH 把 newt-sfu 节点自动部署到远程 Linux 服务器：
// 安装依赖 → 下载二进制 → 创建占位节点并签发 enrollment token → 写配置与 systemd → 启动 →
// 等待节点 enroll 上线。部署过程以 Gateway 事件实时回传日志（见 deployer.go）。
package sfudeploy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/newtspeak/newt-server/backend/internal/secretstore"
)

// credentialSecretName 凭据加密主密钥在 ClusterSecret 中的键名（与 CA 私钥同一存储层级）。
const credentialSecretName = "sfu_deploy_credential_key"

// Credential 一台目标服务器的 SSH 凭据明文形态。
// 仅在内存与加密块中出现，绝不写入日志、审计、部署参数或 API 响应。
type Credential struct {
	// Password 密码登录时的登录密码；非 root 时默认复用为 sudo 密码。
	Password string `json:"password,omitempty"`
	// PrivateKey PEM 私钥内容（私钥登录）。
	PrivateKey string `json:"private_key,omitempty"`
	// Passphrase 私钥口令（可选）。
	Passphrase string `json:"passphrase,omitempty"`
	// SudoPassword 非 root 且需要密码 sudo 时使用；为空则回落 Password。
	SudoPassword string `json:"sudo_password,omitempty"`
}

// SudoSecret 返回用于 sudo -S 的密码（优先显式 sudo 密码，其次登录密码）。
func (c Credential) SudoSecret() string {
	if c.SudoPassword != "" {
		return c.SudoPassword
	}
	return c.Password
}

// CredentialCipher 基于 AES-256-GCM 的凭据加解密器；主密钥持久化在 secretstore。
type CredentialCipher struct {
	aead cipher.AEAD
}

// LoadCredentialCipher 读取或首次生成 32 字节主密钥并构造 AEAD。
func LoadCredentialCipher(store secretstore.Store) (*CredentialCipher, error) {
	encoded, ok, err := store.Get(credentialSecretName)
	if err != nil {
		return nil, fmt.Errorf("读取部署凭据主密钥失败: %w", err)
	}
	var key []byte
	if ok {
		key, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("部署凭据主密钥损坏（应为 32 字节 base64）")
		}
	} else {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("生成部署凭据主密钥失败: %w", err)
		}
		if err := store.Set(credentialSecretName, base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, fmt.Errorf("保存部署凭据主密钥失败: %w", err)
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &CredentialCipher{aead: aead}, nil
}

// Encrypt 序列化并加密凭据，返回 nonce||ciphertext。
func (c *CredentialCipher) Encrypt(cred Credential) ([]byte, error) {
	plain, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plain, nil), nil
}

// Decrypt 解密并反序列化凭据。
func (c *CredentialCipher) Decrypt(sealed []byte) (Credential, error) {
	nonceSize := c.aead.NonceSize()
	if len(sealed) < nonceSize {
		return Credential{}, fmt.Errorf("凭据密文长度非法")
	}
	plain, err := c.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return Credential{}, fmt.Errorf("解密凭据失败（主密钥可能已变更）: %w", err)
	}
	var cred Credential
	if err := json.Unmarshal(plain, &cred); err != nil {
		return Credential{}, err
	}
	return cred, nil
}
