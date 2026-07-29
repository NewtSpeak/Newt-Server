// Package ca 内嵌集群 CA（docs 03 §3.1）：ECDSA P-256 自签根证书持久化于 ClusterSecret，
// 负责签发 SFU 节点客户端证书（Enroll/Renew）与控制面 gRPC 服务端证书。
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/newtspeak/newt-server/backend/internal/secretstore"
)

const (
	secretCACert = "cluster_ca_cert"
	secretCAKey  = "cluster_ca_key"

	caValidity       = 10 * 365 * 24 * time.Hour
	nodeCertValidity = 90 * 24 * time.Hour
	serverCertValidity = 90 * 24 * time.Hour
)

// CA 集群证书签发机构。
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

// Load 从 Store 加载 CA；不存在时生成自签 CA 并持久化，保证多次重启一致。
func Load(store secretstore.Store) (*CA, error) {
	certPEM, hasCert, err := store.Get(secretCACert)
	if err != nil {
		return nil, fmt.Errorf("读取集群 CA 证书: %w", err)
	}
	keyPEM, hasKey, err := store.Get(secretCAKey)
	if err != nil {
		return nil, fmt.Errorf("读取集群 CA 私钥: %w", err)
	}
	if hasCert && hasKey {
		return parse([]byte(certPEM), []byte(keyPEM))
	}
	generated, err := generate()
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(generated.key)
	if err != nil {
		return nil, fmt.Errorf("序列化 CA 私钥: %w", err)
	}
	if err := store.Set(secretCACert, string(generated.certPEM)); err != nil {
		return nil, fmt.Errorf("持久化 CA 证书: %w", err)
	}
	if err := store.Set(secretCAKey, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))); err != nil {
		return nil, fmt.Errorf("持久化 CA 私钥: %w", err)
	}
	return generated, nil
}

func generate() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 私钥: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "NewtSpeak Cluster CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("自签 CA 证书: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

func parse(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("集群 CA 证书 PEM 无效")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析集群 CA 证书: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("集群 CA 私钥 PEM 无效")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析集群 CA 私钥: %w", err)
	}
	return &CA{cert: cert, key: key, certPEM: certPEM}, nil
}

// CertPEM 返回 CA 根证书 PEM（下发给节点作为 ca_bundle）。
func (c *CA) CertPEM() string { return string(c.certPEM) }

// Pool 返回用于校验节点客户端证书的 CertPool。
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

// SignNodeCSR 解析节点 CSR，无视 CSR 内声明的身份，强制签发 CN=nodeID 的客户端证书（90 天）。
// 返回证书 PEM、sha256 指纹（hex）与过期时间。
func (c *CA) SignNodeCSR(csrPEM, nodeID string) (certPEM, fingerprint string, notAfter time.Time, err error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return "", "", time.Time{}, fmt.Errorf("CSR PEM 无效")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("解析 CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return "", "", time.Time{}, fmt.Errorf("CSR 签名校验失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", time.Time{}, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		DNSNames:     []string{nodeID},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(nodeCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// ClientAuth：控制通道 mTLS 客户端（docs 03 §5）；
		// ServerAuth：级联 mTLS 监听（tcp/8843）时同证书作服务端，child 拨号方
		// 以 ServerName=node_id 校验（docs 15 BG.2/BH），缺 ServerAuth 会握手失败。
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("签发节点证书: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Fingerprint(der), template.NotAfter, nil
}

// ServerCert 从 CA 签发控制面 gRPC 服务端证书（内存态，进程启动时生成）。
// sans 中的 IP 字面量进 IPAddresses，其余进 DNSNames。
func (c *CA) ServerCert(sans []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("生成服务端私钥: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "newt-server-control"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(serverCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("签发服务端证书: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Fingerprint 证书 DER 的 sha256 hex 指纹。
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("生成证书序列号: %w", err)
	}
	return serial, nil
}
