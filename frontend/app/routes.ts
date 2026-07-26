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
    route("voice/deploy", "routes/voice-deploy.tsx"),
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
    // 装扮商店
    route("cosmetics/items", "routes/cosmetics-items.tsx"),
    route("cosmetics/items/:itemId", "routes/cosmetics-item-detail.tsx"),
    route("cosmetics/categories", "routes/cosmetics-categories.tsx"),
    route("cosmetics/bundles", "routes/cosmetics-bundles.tsx"),
    route("cosmetics/tags", "routes/cosmetics-tags.tsx"),
    route("cosmetics/grants", "routes/cosmetics-grants.tsx"),
    route("cosmetics/activity", "routes/cosmetics-activity.tsx"),
    // 系统
    route("users", "routes/users.tsx"),
    route("account", "routes/account.tsx"),
    route("settings", "routes/settings.tsx"),
  ]),
] satisfies RouteConfig
