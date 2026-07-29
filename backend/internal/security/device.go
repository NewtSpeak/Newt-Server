package security

import "strings"

// DeviceInfo 从 User-Agent 推断「设备名 / 平台」展示串（Newt-Desktop docs 01 FR-27
// 会话列表设备元数据）。只做粗粒度识别，识别不出时回退原始 UA 截断。
func DeviceInfo(userAgent string) (device, platform string) {
	ua := strings.ToLower(userAgent)
	switch {
	case ua == "":
		return "未知设备", "unknown"
	case strings.Contains(ua, "owl-desktop") || strings.Contains(ua, "tauri"):
		device = "Owl 桌面客户端"
	case strings.Contains(ua, "electron"):
		device = "桌面应用"
	case strings.Contains(ua, "firefox"):
		device = "Firefox 浏览器"
	case strings.Contains(ua, "edg/"):
		device = "Edge 浏览器"
	case strings.Contains(ua, "chrome"):
		device = "Chrome 浏览器"
	case strings.Contains(ua, "safari"):
		device = "Safari 浏览器"
	case strings.Contains(ua, "curl") || strings.Contains(ua, "httpie") || strings.Contains(ua, "python"):
		device = "API 客户端"
	default:
		device = truncate(userAgent, 64)
	}
	switch {
	case strings.Contains(ua, "windows"):
		platform = "windows"
	case strings.Contains(ua, "mac os") || strings.Contains(ua, "macos") || strings.Contains(ua, "darwin"):
		platform = "macos"
	case strings.Contains(ua, "android"):
		platform = "android"
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad") || strings.Contains(ua, "ios"):
		platform = "ios"
	case strings.Contains(ua, "linux"):
		platform = "linux"
	default:
		platform = "unknown"
	}
	return device, platform
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
