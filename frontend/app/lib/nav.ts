import type { LucideIcon } from "lucide-react"
import {
  ActivityIcon,
  BotIcon,
  GaugeIcon,
  GiftIcon,
  LayoutDashboardIcon,
  MessageSquareIcon,
  EyeOffIcon,
  MicIcon,
  MonitorUpIcon,
  NetworkIcon,
  PackageIcon,
  ScrollTextIcon,
  SearchIcon,
  ServerIcon,
  ShapesIcon,
  ShoppingBagIcon,
  Settings2Icon,
  ShieldBanIcon,
  ShieldOffIcon,
  TagsIcon,
  UserCogIcon,
  Users2Icon,
} from "lucide-react"

export type NavItem = { title: string; url: string; icon: LucideIcon }
export type NavGroup = { label: string; items: NavItem[] }

export const NAV_GROUPS: NavGroup[] = [
  {
    label: "概览",
    items: [{ title: "控制台", url: "/dashboard", icon: LayoutDashboardIcon }],
  },
  {
    label: "服务器管理",
    items: [{ title: "服务器", url: "/servers", icon: Users2Icon }],
  },
  {
    label: "语音基础设施",
    items: [
      { title: "SFU 节点", url: "/voice/nodes", icon: ServerIcon },
      { title: "节点池", url: "/voice/pools", icon: NetworkIcon },
      { title: "语音状态", url: "/voice/states", icon: ActivityIcon },
    ],
  },
  {
    label: "治理",
    items: [
      { title: "多维限制", url: "/governance/restrictions", icon: ShieldOffIcon },
      { title: "封禁管理", url: "/governance/bans", icon: ShieldBanIcon },
      { title: "操作日志", url: "/governance/audit", icon: ScrollTextIcon },
      { title: "临场与音频审计", url: "/governance/presence", icon: EyeOffIcon },
    ],
  },
  {
    label: "消息",
    items: [
      { title: "频道消息", url: "/messages", icon: MessageSquareIcon },
      { title: "全系统搜索", url: "/search", icon: SearchIcon },
    ],
  },
  {
    label: "舞台与共享",
    items: [
      { title: "舞台管理", url: "/stage", icon: MicIcon },
      { title: "屏幕共享配额", url: "/screen-quota", icon: MonitorUpIcon },
    ],
  },
  {
    label: "开放平台",
    items: [{ title: "机器人", url: "/bots", icon: BotIcon }],
  },
  {
    label: "装扮商店",
    items: [
      { title: "单品", url: "/cosmetics/items", icon: ShoppingBagIcon },
      { title: "捆绑包", url: "/cosmetics/bundles", icon: PackageIcon },
      { title: "品类", url: "/cosmetics/categories", icon: ShapesIcon },
      { title: "标签", url: "/cosmetics/tags", icon: TagsIcon },
      { title: "发放工具", url: "/cosmetics/grants", icon: GiftIcon },
    ],
  },
  {
    label: "系统",
    items: [
      { title: "平台用户", url: "/users", icon: UserCogIcon },
      { title: "系统设置", url: "/settings", icon: Settings2Icon },
    ],
  },
]

export const NAV_ICON_FALLBACK = GaugeIcon

export function pageTitle(pathname: string): string {
  if (/^\/servers\/[^/]+/.test(pathname)) return "服务器详情"
  if (/^\/cosmetics\/items\/[^/]+/.test(pathname)) return "单品详情"
  if (pathname === "/governance/presence") return "临场与音频审计"
  for (const group of NAV_GROUPS) {
    for (const item of group.items) {
      if (item.url === pathname) return item.title
    }
  }
  return "OwlSpeak 管理控制台"
}
