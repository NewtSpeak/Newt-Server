package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	"github.com/owlspeak/owl-server/backend/internal/secretstore"
)

func newCSR(t *testing.T, commonName string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func TestLoadPersistsAndReloads(t *testing.T) {
	store := secretstore.NewMemoryStore()
	first, err := Load(store)
	if err != nil {
		t.Fatalf("首次生成 CA 失败: %v", err)
	}
	second, err := Load(store)
	if err != nil {
		t.Fatalf("重新加载 CA 失败: %v", err)
	}
	if first.CertPEM() != second.CertPEM() {
		t.Fatal("重启后 CA 证书应保持一致")
	}
	if first.cert.Subject.CommonName != "OwlSpeak Cluster CA" {
		t.Fatalf("CA CN 错误: %s", first.cert.Subject.CommonName)
	}
	if !first.cert.IsCA {
		t.Fatal("CA 证书必须带 IsCA")
	}
}

func TestSignNodeCSRRewritesCN(t *testing.T) {
	authority, err := Load(secretstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "0a4f0f7e-1111-2222-3333-444455556666"
	certPEM, fingerprint, notAfter, err := authority.SignNodeCSR(newCSR(t, "forged-cn"), nodeID)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != nodeID {
		t.Fatalf("必须强制 CN=node_id，实际为 %s", cert.Subject.CommonName)
	}
	if got := Fingerprint(cert.Raw); got != fingerprint {
		t.Fatalf("指纹不一致: %s != %s", got, fingerprint)
	}
	if len(fingerprint) != 64 {
		t.Fatalf("指纹应为 sha256 hex（64 字符），实际 %d", len(fingerprint))
	}
	if remaining := time.Until(notAfter); remaining < 89*24*time.Hour || remaining > 91*24*time.Hour {
		t.Fatalf("节点证书有效期应约 90 天，实际剩余 %s", remaining)
	}
	// 控制通道客户端 + 级联监听服务端双用途（docs 15 BG.2：级联端口复用节点证书）。
	if len(cert.ExtKeyUsage) != 2 ||
		cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		cert.ExtKeyUsage[1] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("节点证书 ExtKeyUsage 必须为 ClientAuth+ServerAuth，实际 %v", cert.ExtKeyUsage)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != nodeID {
		t.Fatalf("节点证书 DNS SAN 必须为 node_id（级联拨号 ServerName 校验），实际 %v", cert.DNSNames)
	}
	// 证书链必须能被集群 CA 校验（控制通道 ClientCAs 用同一 Pool）。
	if _, err := cert.Verify(x509.VerifyOptions{Roots: authority.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("节点证书链校验失败: %v", err)
	}
}

func TestSignNodeCSRRejectsInvalidPEM(t *testing.T) {
	authority, err := Load(secretstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := authority.SignNodeCSR("not a csr", "node"); err == nil {
		t.Fatal("无效 CSR 应报错")
	}
}

func TestServerCertSANs(t *testing.T) {
	authority, err := Load(secretstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	tlsCert, err := authority.ServerCert([]string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatalf("签发服务端证书失败: %v", err)
	}
	cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "localhost" {
		t.Fatalf("DNS SAN 错误: %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "127.0.0.1" {
		t.Fatalf("IP SAN 错误: %v", cert.IPAddresses)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatal("服务端证书 ExtKeyUsage 必须为 ServerAuth")
	}
}
