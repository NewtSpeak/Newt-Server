package sfunode

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// newNodeKeyAndCSR 模拟节点侧：本地生成私钥并产出 CSR（CN 故意伪造）。
func newNodeKeyAndCSR(t *testing.T, fakeCN string) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: fakeCN},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

func TestLoadOrCreateCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 私钥文件权限必须是 0600（docs 03 §7）。
	for _, name := range []string{"ca.key", "server.key"} {
		info, err := os.Stat(filepath.Join(dir, "sfu-ca", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s 权限期望 0600，实际 %o", name, info.Mode().Perm())
		}
	}

	// 再次加载应复用同一 CA（幂等）。
	ca2, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca.CABundlePEM()) != string(ca2.CABundlePEM()) {
		t.Fatal("重复加载不应重新生成 CA")
	}
}

func TestSignNodeCSRForcesNodeIdentity(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nodeID := uuid.New()
	// CSR 中伪造 CN，签发时必须被 Server 记录的 node_id 覆盖（docs 03 §4.3）。
	_, csrPEM := newNodeKeyAndCSR(t, "evil-forged-identity")

	certPEM, fingerprint, notAfter, err := ca.SignNodeCSR(csrPEM, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != nodeID.String() {
		t.Fatalf("证书 CN 应为 node_id %s，实际 %s", nodeID, cert.Subject.CommonName)
	}
	wantURI := "spiffe://owlspeak/sfu/" + nodeID.String()
	if len(cert.URIs) != 1 || cert.URIs[0].String() != wantURI {
		t.Fatalf("证书 URI SAN 应为 %s，实际 %v", wantURI, cert.URIs)
	}
	if len(fingerprint) != 64 {
		t.Fatalf("指纹应为 SHA-256 hex（64 字符），实际 %d", len(fingerprint))
	}
	if FingerprintCert(cert) != fingerprint {
		t.Fatal("指纹与证书不匹配")
	}
	if notAfter.Before(cert.NotBefore) {
		t.Fatal("NotAfter 不合法")
	}

	// 证书链必须能被集群 CA 校验通过。
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.CABundlePEM()) {
		t.Fatal("CA bundle 解析失败")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("节点证书链校验失败: %v", err)
	}

	// 非法 CSR 拒绝。
	if _, _, _, err := ca.SignNodeCSR([]byte("not a csr"), nodeID); err == nil {
		t.Fatal("非法 CSR 应被拒绝")
	}
}

// TestMTLSHandshake 验证控制通道 TLS 配置：集群 CA 签发的客户端证书握手成功，
// 外部 CA 签发的证书被拒绝，无证书被拒绝。
func TestMTLSHandshake(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Server TLS 配置已含集群 CA 的 Server 证书，httptest 不会再覆盖。
	server.TLS = ca.ServerTLSConfig()
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CABundlePEM())

	clientFor := func(cert *tls.Certificate) *http.Client {
		// 强制 ServerName=localhost 匹配 Server 证书 SAN。
		tlsCfg := &tls.Config{RootCAs: roots, ServerName: "localhost"}
		if cert != nil {
			tlsCfg.Certificates = []tls.Certificate{*cert}
		}
		return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	}
	addr := server.Listener.Addr().String()

	t.Run("集群 CA 签发的节点证书握手成功", func(t *testing.T) {
		nodeKey, csrPEM := newNodeKeyAndCSR(t, "node")
		certPEM, _, _, err := ca.SignNodeCSR(csrPEM, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(nodeKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := clientFor(&clientCert).Get("https://" + addr)
		if err != nil {
			t.Fatalf("mTLS 握手应成功: %v", err)
		}
		resp.Body.Close()
	})

	t.Run("无客户端证书被拒绝", func(t *testing.T) {
		if _, err := clientFor(nil).Get("https://" + addr); err == nil {
			t.Fatal("无客户端证书应握手失败")
		}
	})

	t.Run("外部 CA 证书被拒绝", func(t *testing.T) {
		foreignCA, err := LoadOrCreateCA(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		nodeKey, csrPEM := newNodeKeyAndCSR(t, "node")
		certPEM, _, _, err := foreignCA.SignNodeCSR(csrPEM, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(nodeKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clientFor(&clientCert).Get("https://" + addr); err == nil {
			t.Fatal("外部 CA 签发的证书应握手失败")
		}
	})
}
