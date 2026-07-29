package publicinvite

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// 邀请落地页（服务端渲染，无需登录、不依赖控制台前端资源）：
//   - 展示服务器信息、公告/注意事项/协议；
//   - 「在客户端中打开」按钮 + 可选自动唤起深链；
//   - 未安装客户端时提供各平台下载引导。

type landingNoticeGroup struct {
	Label   string
	Notices []model.InviteNotice
}

type landingDownload struct {
	Label string
	URL   string
}

type landingView struct {
	AppName      string
	GuildName    string
	MemberCount  int64
	Description  string
	Code         string
	DeepLink     string
	AutoDeepLink bool
	Groups       []landingNoticeGroup
	Downloads    []landingDownload
	WebsiteURL   string
	SignupOpen   bool
}

var landingTemplate = template.Must(template.New("landing").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>加入 {{.GuildName}} · {{.AppName}}</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; margin: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    background: radial-gradient(1200px 600px at 50% -10%, #2b2f4a 0%, #16181f 55%, #0e0f14 100%);
    color: #e7e9ee; min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 32px 16px;
  }
  .card { width: 100%; max-width: 560px; background: rgba(255,255,255,.045); border: 1px solid rgba(255,255,255,.09);
    border-radius: 20px; padding: 36px 32px; backdrop-filter: blur(12px); }
  .app { font-size: 13px; letter-spacing: .2em; text-transform: uppercase; color: #8b90a0; text-align: center; }
  h1 { margin-top: 10px; text-align: center; font-size: 28px;
    background: linear-gradient(90deg, #7dd3fc, #a78bfa, #f0abfc); -webkit-background-clip: text; background-clip: text; color: transparent; }
  .meta { margin-top: 8px; text-align: center; color: #9aa0b2; font-size: 14px; }
  .desc { margin-top: 18px; color: #c3c8d6; font-size: 14px; line-height: 1.7; white-space: pre-wrap; }
  .open { display: block; margin: 26px auto 0; width: 100%; padding: 14px; border: 0; border-radius: 12px;
    background: linear-gradient(90deg, #6366f1, #8b5cf6); color: #fff; font-size: 16px; font-weight: 600;
    text-align: center; text-decoration: none; cursor: pointer; transition: filter .15s ease; }
  .open:hover { filter: brightness(1.1); }
  .hint { margin-top: 10px; text-align: center; font-size: 12px; color: #8b90a0; }
  section { margin-top: 26px; }
  section > h2 { font-size: 13px; letter-spacing: .12em; text-transform: uppercase; color: #8b90a0; margin-bottom: 10px; }
  .notice { border: 1px solid rgba(255,255,255,.08); background: rgba(255,255,255,.03); border-radius: 12px;
    padding: 14px 16px; margin-bottom: 10px; }
  .notice h3 { font-size: 15px; margin-bottom: 6px; }
  .notice p { font-size: 13px; line-height: 1.7; color: #b6bccb; white-space: pre-wrap; }
  .downloads { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 10px; }
  .downloads a { display: block; text-align: center; padding: 11px 8px; border-radius: 10px; font-size: 14px;
    border: 1px solid rgba(255,255,255,.12); color: #dfe2ea; text-decoration: none; transition: background .15s ease; }
  .downloads a:hover { background: rgba(255,255,255,.07); }
  footer { margin-top: 26px; text-align: center; font-size: 12px; color: #6d7284; }
  footer a { color: #8b93f8; text-decoration: none; }
</style>
</head>
<body>
<main class="card">
  <p class="app">{{.AppName}} · 服务器邀请</p>
  <h1>{{.GuildName}}</h1>
  <p class="meta">{{.MemberCount}} 位成员 · 邀请码 {{.Code}}</p>
  {{if .Description}}<p class="desc">{{.Description}}</p>{{end}}

  <a class="open" id="open-app" href="{{.DeepLink}}">在客户端中打开并加入</a>
  <p class="hint">已安装客户端会自动填入服务器地址与邀请码{{if .SignupOpen}}；已有本服务器后端账号可免注册直接加入{{end}}。</p>

  {{range .Groups}}
  <section>
    <h2>{{.Label}}</h2>
    {{range .Notices}}
    <div class="notice">
      <h3>{{.Title}}</h3>
      {{if .Body}}<p>{{.Body}}</p>{{end}}
    </div>
    {{end}}
  </section>
  {{end}}

  {{if .Downloads}}
  <section>
    <h2>还没有客户端？下载后重新打开本页</h2>
    <div class="downloads">
      {{range .Downloads}}<a href="{{.URL}}" rel="noopener">{{.Label}}</a>{{end}}
    </div>
  </section>
  {{end}}

  <footer>{{if .WebsiteURL}}<a href="{{.WebsiteURL}}" rel="noopener">了解 {{.AppName}}</a>{{else}}Powered by {{.AppName}}{{end}}</footer>
</main>
{{if .AutoDeepLink}}
<script>
  // 打开页面时自动尝试唤起客户端；未安装时浏览器静默失败，用户停留在下载引导。
  setTimeout(function () { window.location.href = {{.DeepLink}}; }, 600);
</script>
{{end}}
</body>
</html>`))

// landingPage GET /invite/{code}：服务端渲染的邀请落地页。
func (h *api) landingPage(c *gin.Context) {
	data, ok := h.resolveInvite(c.Param("code"))
	if !ok {
		c.Data(http.StatusNotFound, "text/html; charset=utf-8",
			[]byte(`<!DOCTYPE html><html lang="zh-CN"><meta charset="utf-8"><body style="font-family:sans-serif;background:#16181f;color:#e7e9ee;display:flex;align-items:center;justify-content:center;min-height:100vh"><p>邀请不存在或已过期</p></body></html>`))
		return
	}
	base := h.baseURL(c)
	groupLabels := []struct {
		kind  model.InviteNoticeKind
		label string
	}{
		{model.NoticeAnnouncement, "公告"},
		{model.NoticeNotice, "注意事项"},
		{model.NoticeAgreement, "协议"},
	}
	var groups []landingNoticeGroup
	for _, item := range groupLabels {
		var matched []model.InviteNotice
		for _, notice := range data.Notices {
			if notice.Kind == item.kind {
				matched = append(matched, notice)
			}
		}
		if len(matched) > 0 {
			groups = append(groups, landingNoticeGroup{Label: item.label, Notices: matched})
		}
	}
	var downloads []landingDownload
	for _, item := range []struct{ label, url string }{
		{"Windows", data.Portal.WindowsURL},
		{"macOS", data.Portal.MacosURL},
		{"Linux", data.Portal.LinuxURL},
		{"Android", data.Portal.AndroidURL},
		{"iOS", data.Portal.IosURL},
	} {
		if item.url != "" {
			downloads = append(downloads, landingDownload{Label: item.label, URL: item.url})
		}
	}
	view := landingView{
		AppName:      data.Portal.AppName,
		GuildName:    data.Guild.Name,
		MemberCount:  data.MemberCount,
		Description:  data.Landing.Description,
		Code:         data.Invite.Code,
		DeepLink:     deepLink(data.Portal.DeepLinkScheme, base, data.Invite.Code, data.Guild.ID),
		AutoDeepLink: data.Landing.AutoDeepLink,
		Groups:       groups,
		Downloads:    downloads,
		WebsiteURL:   data.Portal.WebsiteURL,
		SignupOpen:   signupEnabled(),
	}
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := landingTemplate.Execute(c.Writer, view); err != nil {
		_ = err
	}
}
