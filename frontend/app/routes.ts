import { type RouteConfig, index, layout, route } from "@react-router/dev/routes"

export default [
  index("routes/home.tsx"),
  route("login", "routes/login.tsx"),
  route("signup", "routes/signup.tsx"),
  layout("routes/console-layout.tsx", [
    // 概览
    route("dashboard", "routes/dashboard.tsx"),
    // 服务器管理
    route("servers", "routes/servers.tsx"),
    route("servers/:guildId", "routes/server-detail.tsx"),
    // 语音基础设施
    route("voice/nodes", "routes/voice-nodes.tsx"),
    route("voice/pools", "routes/node-pools.tsx"),
    route("voice/states", "routes/voice-states.tsx"),
    // 治理
    route("governance/restrictions", "routes/restrictions.tsx"),
    route("governance/bans", "routes/bans.tsx"),
    route("governance/audit", "routes/audit.tsx"),
    route("governance/presence", "routes/presence.tsx"),
    // 消息
    route("messages", "routes/messages.tsx"),
    route("search", "routes/search.tsx"),
    // 舞台与共享
    route("stage", "routes/stage.tsx"),
    route("screen-quota", "routes/screen-quota.tsx"),
    // 开放平台
    route("bots", "routes/bots.tsx"),
    // 系统
    route("users", "routes/users.tsx"),
    route("settings", "routes/settings.tsx"),
  ]),
] satisfies RouteConfig
