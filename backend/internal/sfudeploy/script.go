package sfudeploy

import (
	"embed"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"text/template"
)

//go:embed scripts/*.tmpl
var scriptFS embed.FS

var scriptTemplates = template.Must(template.ParseFS(scriptFS, "scripts/*.tmpl"))

// installDir 远端安装根目录（与 deploy/prod/install.sh 保持一致）。
const installDir = "/opt/owlspeak"

// TLS 模式。
const (
	// TLSModeCaddy 装 Caddy 反代并自动申请 Let's Encrypt 证书（需域名）。
	TLSModeCaddy = "caddy"
	// TLSModeCustom 使用目标机上已有的证书文件，SFU 直接监听 8443。
	TLSModeCustom = "custom"
	// TLSModeNone 明文 ws://，仅适合内网与测试。
	TLSModeNone = "none"
)

var (
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)
	domainPattern  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)
	releasePattern = regexp.MustCompile(`^owl-sfu-[A-Za-z0-9._-]+-(linux|darwin|windows)-(amd64|arm64)$`)
	pathPattern    = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,255}$`)
)

// phase1Data phase1.sh.tmpl 的渲染参数。
type phase1Data struct {
	InstallDir      string
	SSHHost         string
	ReleaseName     string
	BinaryURL       string
	BinarySHA256    string
	ServerHealthURL string
	ControlHost     string
	ControlPort     string
	MediaUDPPort    int
	PublicIP        string
	InstallCaddy    bool
	ConfigureUFW    bool
	EnableCascade   bool
	ForceReinstall  bool
	NoTLS           bool
	Domain          string
}

// phase2Data phase2.sh.tmpl 的渲染参数。
type phase2Data struct {
	InstallDir      string
	NodeID          string
	EnrollToken     string
	ControlEndpoint string
	EnrollInsecure  bool
	WSSListen       string
	LocalWSSPort    string
	NoTLS           bool
	TLSCertFile     string
	TLSKeyFile      string
	MediaUDPPort    int
	PublicIP        string
	AdvertiseWssURL string
	MaxUsers        int
	EnableCascade   bool
	InstallCaddy    bool
	ForceReinstall  bool
	Domain          string
}

func renderPhase1(data phase1Data) (string, error) {
	var out strings.Builder
	if err := scriptTemplates.ExecuteTemplate(&out, "phase1.sh.tmpl", data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func renderPhase2(data phase2Data) (string, error) {
	var out strings.Builder
	if err := scriptTemplates.ExecuteTemplate(&out, "phase2.sh.tmpl", data); err != nil {
		return "", err
	}
	return out.String(), nil
}

// splitEndpoint 把 host:port 拆开；缺省端口回落 9443。
func splitEndpoint(endpoint string) (host, port string) {
	endpoint = strings.TrimSpace(endpoint)
	if h, p, err := net.SplitHostPort(endpoint); err == nil && h != "" && p != "" {
		return h, p
	}
	if endpoint == "" {
		return "127.0.0.1", "9443"
	}
	return endpoint, "9443"
}

// validateShellSafe 阻断任何可能逃逸出脚本模板的字符。
// 所有进入脚本的用户输入都必须先过这里（模板本身不做转义）。
func validateShellSafe(field, value string, pattern *regexp.Regexp) error {
	if strings.ContainsAny(value, "\n\r\"'`$\\;|&<>") {
		return fmt.Errorf("%s 含非法字符", field)
	}
	if pattern != nil && !pattern.MatchString(value) {
		return fmt.Errorf("%s 格式非法: %q", field, value)
	}
	return nil
}

// validateURL 校验二进制下载地址（只允许 http/https，且不含 shell 元字符）。
func validateURL(field, raw string) error {
	if strings.ContainsAny(raw, "\n\r\"'`$\\;|&<> ") {
		return fmt.Errorf("%s 含非法字符", field)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s 必须是合法的 http(s) 地址", field)
	}
	return nil
}
