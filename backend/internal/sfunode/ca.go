package sfunode

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
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// 证书有效期（docs 03 §3.1：节点证书 30–90 天，CA 更长）。
const (
	caCertTTL     = 10 * 365 * 24 * time.Hour
	serverCertTTL = 2 * 365 * 24 * time.Hour
	nodeCertTTL   = 60 * 24 * time.Hour
)

// ClusterCA 内置集群 CA：负责 Server 证书与节点证书签发（docs 03 §3，标准库实现）。
type ClusterCA struct {
	caCert     *x509.Certificate
	caKey      *ecdsa.PrivateKey
	caPEM      []byte
	serverCert tls.Certificate
}

// LoadOrCreateCA 首次启动时在 dataDir/sfu-ca 下生成 CA 与 Server 证书；已存在则加载。
// 私钥文件权限 0600（docs 03 §7）。
func LoadOrCreateCA(dataDir string) (*ClusterCA, error) {
	dir := filepath.Join(dataDir, "sfu-ca")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 CA 目录失败: %w", err)
	}
	caCertPath := filepath.Join(dir, "ca.crt")
	caKeyPath := filepath.Join(dir, "ca.key")
	serverCertPath := filepath.Join(dir, "server.crt")
	serverKeyPath := filepath.Join(dir, "server.key")

	if _, err := os.Stat(caCertPath); os.IsNotExist(err) {
		if err := generateCA(caCertPath, caKeyPath); err != nil {
			return nil, err
		}
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("读取 CA 证书失败: %w", err)
	}
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return nil, fmt.Errorf("读取 CA 私钥失败: %w", err)
	}
	caCert, err := parseCertPEM(caPEM)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 证书失败: %w", err)
	}
	caKey, err := parseECKeyPEM(caKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 私钥失败: %w", err)
	}
	ca := &ClusterCA{caCert: caCert, caKey: caKey, caPEM: caPEM}

	if _, err := os.Stat(serverCertPath); os.IsNotExist(err) {
		if err := ca.generateServerCert(serverCertPath, serverKeyPath); err != nil {
			return nil, err
		}
	}
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		return nil, fmt.Errorf("加载 Server 证书失败: %w", err)
	}
	ca.serverCert = serverCert
	return ca, nil
}

// CABundlePEM 返回 CA 证书 PEM（enroll 时下发给节点校验 Server）。
func (ca *ClusterCA) CABundlePEM() []byte { return ca.caPEM }

// SignNodeCSR 校验 CSR 签名并签发节点证书。
// 证书身份强制以 Server 记录的 nodeID 为准，忽略 CSR 中自报的 CN（docs 03 §4.3）。
func (ca *ClusterCA) SignNodeCSR(csrPEM []byte, nodeID uuid.UUID) (certPEM []byte, fingerprint string, notAfter time.Time, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", time.Time{}, fmt.Errorf("CSR PEM 格式不合法")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("解析 CSR 失败: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("CSR 签名校验失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	now := time.Now().UTC()
	notAfter = now.Add(nodeCertTTL)
	spiffe := &url.URL{Scheme: "spiffe", Host: "owlspeak", Path: "/sfu/" + nodeID.String()}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID.String(), Organization: []string{"NewtSpeak SFU Node"}},
		URIs:         []*url.URL{spiffe},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// 节点既作为控制通道客户端，也可能在池内级联时作为服务端（docs 08 §6.2）。
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, csr.PublicKey, ca.caKey)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("签发节点证书失败: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, fingerprintDER(der), notAfter, nil
}

// ServerTLSConfig 控制通道服务端 TLS 配置：强制双向认证，客户端证书必须由集群 CA 签发。
func (ca *ClusterCA) ServerTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca.caCert)
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{ca.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
}

// FingerprintCert 计算证书 SHA-256 指纹（hex）。
func FingerprintCert(cert *x509.Certificate) string { return fingerprintDER(cert.Raw) }

func fingerprintDER(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func generateCA(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成 CA 私钥失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "NewtSpeak Cluster CA", Organization: []string{"NewtSpeak"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caCertTTL),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("生成 CA 证书失败: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("序列化 CA 私钥失败: %w", err)
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
}

func (ca *ClusterCA) generateServerCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成 Server 私钥失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	dnsNames := []string{"localhost", "owl-server"}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "owl-server-control", Organization: []string{"NewtSpeak"}},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(serverCertTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		return fmt.Errorf("签发 Server 证书失败: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("序列化 Server 私钥失败: %w", err)
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("生成证书序列号失败: %w", err)
	}
	return serial, nil
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("PEM 中不含证书")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("PEM 中不含私钥")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}
