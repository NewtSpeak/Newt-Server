package sfudeploy

import (
	"strings"
	"testing"

	"github.com/newtspeak/newt-server/backend/internal/secretstore"
)

func TestCredentialRoundTrip(t *testing.T) {
	store := secretstore.NewMemoryStore()
	cipher, err := LoadCredentialCipher(store)
	if err != nil {
		t.Fatalf("加载凭据加密器失败: %v", err)
	}
	original := Credential{Password: "p@ss w0rd", PrivateKey: "-----BEGIN KEY-----\nabc\n", SudoPassword: "sudo!"}
	sealed, err := cipher.Encrypt(original)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if strings.Contains(string(sealed), "p@ss") {
		t.Fatal("密文中出现了明文密码")
	}
	// 同一主密钥的新实例必须能解开（重启后仍可复用已保存凭据）。
	reopened, err := LoadCredentialCipher(store)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	got, err := reopened.Decrypt(sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != original {
		t.Fatalf("解密结果不一致: %+v", got)
	}
	if got.SudoSecret() != "sudo!" {
		t.Fatalf("SudoSecret 应优先返回显式 sudo 密码，得到 %q", got.SudoSecret())
	}
	if (Credential{Password: "only-login"}).SudoSecret() != "only-login" {
		t.Fatal("未设置 sudo 密码时应回落登录密码")
	}
}

func TestCredentialDecryptWrongKey(t *testing.T) {
	cipherA, _ := LoadCredentialCipher(secretstore.NewMemoryStore())
	cipherB, _ := LoadCredentialCipher(secretstore.NewMemoryStore())
	sealed, err := cipherA.Encrypt(Credential{Password: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipherB.Decrypt(sealed); err == nil {
		t.Fatal("换主密钥后不应能解密")
	}
}

func TestNormalizeSpecDefaultsAndTLSModes(t *testing.T) {
	spec, err := normalizeSpec(NodeSpec{DisplayName: "东京节点", TLSMode: TLSModeNone}, "203.0.113.9")
	if err != nil {
		t.Fatalf("意外失败: %v", err)
	}
	if spec.MediaUDPPort != 3478 || spec.MaxUsers != 1200 {
		t.Fatalf("默认值未填充: %+v", spec)
	}
	if spec.PublicIP != "203.0.113.9" {
		t.Fatalf("明文模式应回落 SSH 主机作为对外地址，得到 %q", spec.PublicIP)
	}
	_, _, advertise := spec.endpoints()
	if advertise != "ws://203.0.113.9:8443/ws" {
		t.Fatalf("明文模式 advertise 不正确: %s", advertise)
	}

	caddy, err := normalizeSpec(NodeSpec{DisplayName: "n", TLSMode: TLSModeCaddy, Domain: "sfu.example.com"}, "1.2.3.4")
	if err != nil {
		t.Fatalf("caddy 模式失败: %v", err)
	}
	listen, port, adv := caddy.endpoints()
	if listen != "127.0.0.1:8443" || port != "8443" || adv != "wss://sfu.example.com/ws" {
		t.Fatalf("caddy 模式端点不正确: %s %s %s", listen, port, adv)
	}

	if _, err := normalizeSpec(NodeSpec{DisplayName: "n", TLSMode: TLSModeCaddy}, "1.2.3.4"); err == nil {
		t.Fatal("caddy 模式缺域名应报错")
	}
	if _, err := normalizeSpec(NodeSpec{DisplayName: "", TLSMode: TLSModeNone}, "1.2.3.4"); err == nil {
		t.Fatal("空名称应报错")
	}
	if _, err := normalizeSpec(NodeSpec{DisplayName: "n", TLSMode: "bogus"}, "1.2.3.4"); err == nil {
		t.Fatal("未知 TLS 模式应报错")
	}
}

// 注入攻击面：域名/IP 字段直接进入 shell 脚本，必须挡住元字符。
func TestNormalizeSpecRejectsShellInjection(t *testing.T) {
	cases := []NodeSpec{
		{DisplayName: "n", TLSMode: TLSModeCaddy, Domain: "a.com; rm -rf /"},
		{DisplayName: "n", TLSMode: TLSModeCaddy, Domain: "a.com\nwhoami"},
		{DisplayName: "n", TLSMode: TLSModeNone, PublicIP: "1.1.1.1 && curl evil.sh"},
		{DisplayName: "n", TLSMode: TLSModeNone, PublicIP: "$(id)"},
		{DisplayName: "n", TLSMode: TLSModeCustom, Domain: "a.com", TLSCertFile: "/etc/x`id`", TLSKeyFile: "/etc/k"},
	}
	for _, spec := range cases {
		if _, err := normalizeSpec(spec, "1.2.3.4"); err == nil {
			t.Fatalf("应拒绝注入输入: %+v", spec)
		}
	}
}

func TestSplitEndpoint(t *testing.T) {
	for _, tc := range []struct{ in, host, port string }{
		{"newt.example.com:9443", "newt.example.com", "9443"},
		{"1.2.3.4:19443", "1.2.3.4", "19443"},
		{"newt.example.com", "newt.example.com", "9443"},
		{"", "127.0.0.1", "9443"},
	} {
		host, port := splitEndpoint(tc.in)
		if host != tc.host || port != tc.port {
			t.Fatalf("splitEndpoint(%q) = %s/%s，期望 %s/%s", tc.in, host, port, tc.host, tc.port)
		}
	}
}

func TestValidateURL(t *testing.T) {
	if err := validateURL("url", "https://newt.example.com/sfu-releases/owl-sfu-1.0-linux-amd64"); err != nil {
		t.Fatalf("合法 URL 被拒: %v", err)
	}
	for _, bad := range []string{"file:///etc/passwd", "https://a.com/x;rm -rf /", "not-a-url", "https://a.com/`id`"} {
		if err := validateURL("url", bad); err == nil {
			t.Fatalf("应拒绝 %q", bad)
		}
	}
}

// 渲染出的脚本必须是合法 bash，且绝不能泄漏 token 到 set -x。
func TestRenderScripts(t *testing.T) {
	p1, err := renderPhase1(phase1Data{
		InstallDir: installDir, SSHHost: "1.2.3.4", ReleaseName: "owl-sfu-1.0-linux-amd64",
		BinaryURL: "https://newt.example.com/sfu-releases/owl-sfu-1.0-linux-amd64",
		BinarySHA256: strings.Repeat("a", 64), ServerHealthURL: "https://newt.example.com/healthz",
		ControlHost: "newt.example.com", ControlPort: "9443", MediaUDPPort: 3478,
		InstallCaddy: true, ConfigureUFW: true, Domain: "sfu.example.com",
	})
	if err != nil {
		t.Fatalf("phase1 渲染失败: %v", err)
	}
	for _, want := range []string{"PHASE1_OK", "sha256sum -c", "newt-sfu.new", "ufw allow 3478/udp", "caddy"} {
		if !strings.Contains(p1, want) {
			t.Fatalf("phase1 缺少 %q", want)
		}
	}
	if strings.Contains(p1, "set -x") {
		t.Fatal("脚本不得开启 set -x（会把敏感值写进日志）")
	}

	p2, err := renderPhase2(phase2Data{
		InstallDir: installDir, NodeID: "11111111-2222-3333-4444-555555555555",
		EnrollToken: "deadbeef", ControlEndpoint: "newt.example.com:9443", EnrollInsecure: true,
		WSSListen: "127.0.0.1:8443", LocalWSSPort: "8443", NoTLS: true,
		MediaUDPPort: 3478, PublicIP: "1.2.3.4", AdvertiseWssURL: "wss://sfu.example.com/ws",
		MaxUsers: 1200, InstallCaddy: true, Domain: "sfu.example.com",
	})
	if err != nil {
		t.Fatalf("phase2 渲染失败: %v", err)
	}
	for _, want := range []string{
		"PHASE2_OK", "NEWTSFU_NODE_ID=11111111-2222-3333-4444-555555555555",
		"NEWTSFU_ENROLL_TOKEN=deadbeef", "chmod 600", "systemctl restart owl-sfu",
		"reverse_proxy 127.0.0.1:8443", "/healthz",
	} {
		if !strings.Contains(p2, want) {
			t.Fatalf("phase2 缺少 %q", want)
		}
	}
	if strings.Contains(p2, "set -x") {
		t.Fatal("phase2 不得开启 set -x")
	}
	// env heredoc 必须是引号形式，否则 token 里的 $ 会被 shell 展开。
	if !strings.Contains(p2, "<<'NEWTSFU_ENV_EOF'") {
		t.Fatal("env heredoc 必须用引号形式防止变量展开")
	}
}

func TestCheckArchMatch(t *testing.T) {
	m := &Manager{}
	if err := m.checkArchMatch("owl-sfu-1.0-linux-amd64", "amd64"); err != nil {
		t.Fatalf("架构一致却报错: %v", err)
	}
	if err := m.checkArchMatch("owl-sfu-1.0-linux-amd64", "arm64"); err == nil {
		t.Fatal("架构不匹配应报错")
	}
}

func TestLogBufferTruncates(t *testing.T) {
	buf := newLogBuffer()
	line := strings.Repeat("x", 1000)
	var offset int
	for i := 0; i < 400; i++ {
		offset = buf.Append(line)
	}
	if offset != 400*1001 {
		t.Fatalf("offset 应为累计写入字节数，得到 %d", offset)
	}
	content, total := buf.Snapshot()
	if total != offset {
		t.Fatalf("Snapshot offset 不一致: %d vs %d", total, offset)
	}
	if len(content) > logLimit+64 {
		t.Fatalf("日志未被截断，长度 %d", len(content))
	}
	if !strings.Contains(content, "已截断") {
		t.Fatal("截断后应有提示")
	}
}
