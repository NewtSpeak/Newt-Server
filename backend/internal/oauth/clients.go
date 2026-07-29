package oauth

// 内置公开 OAuth 客户端（无 client_secret；Device + PKCE）。
const (
	ClientOwlCLI = "owl-cli"
	ClientOwlMCP = "owl-mcp"
)

type clientInfo struct {
	ID          string
	Name        string
	Description string
}

var builtinClients = map[string]clientInfo{
	ClientOwlCLI: {
		ID:          ClientOwlCLI,
		Name:        "Owl CLI",
		Description: "官方命令行与 AI Agent 工具，代表你操作 NewtSpeak。",
	},
	ClientOwlMCP: {
		ID:          ClientOwlMCP,
		Name:        "Owl MCP",
		Description: "官方 MCP Server，供 AI 助手调用 NewtSpeak 工具。",
	},
}

func lookupClient(clientID string) (clientInfo, bool) {
	c, ok := builtinClients[clientID]
	return c, ok
}
