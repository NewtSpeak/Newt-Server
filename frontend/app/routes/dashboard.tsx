import { useRef } from "react"
import { Link, useOutletContext } from "react-router"
import {
  ActivityIcon,
  ArrowRightIcon,
  MicIcon,
  ServerIcon,
  ShieldCheckIcon,
  ShieldOffIcon,
  Users2Icon,
} from "lucide-react"

import { PageHeader } from "~/components/page-header"
import { Badge } from "~/components/ui/badge"
import { Card, CardAction, CardDescription, CardFooter, CardHeader, CardTitle } from "~/components/ui/card"
import { useAsyncData } from "~/hooks/use-async-data"
import { listSfuNodes, type SfuNode } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"

export default function DashboardPage() {
  const { user, guilds } = useOutletContext<ConsoleContext>()
  const containerRef = useRef<HTMLDivElement>(null)

  // 节点接口由后端并行开发，404 时静默降级为 “—”
  const nodes = useAsyncData<SfuNode[]>(user.system_admin ? () => listSfuNodes() : null, [user.system_admin])
  const nodeList = nodes.data ?? []
  const onlineNodes = nodeList.filter(node => node.status === "ONLINE").length
  const voiceUsers = nodeList.reduce((sum, node) => sum + (node.capacity?.current_users ?? 0), 0)

  // 关键指标卡入场 stagger（GSAP，尊重 prefers-reduced-motion）
  useGSAP(
    () => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.from(".metric-card", {
          autoAlpha: 0,
          y: 16,
          duration: MOTION.enter,
          ease: MOTION.ease,
          stagger: MOTION.stagger,
          clearProps: "all",
        })
      })
    },
    { scope: containerRef }
  )

  const metrics = [
    {
      label: "已加入服务器",
      value: String(guilds.length),
      note: "独立 RBAC 权限域",
      icon: <Users2Icon />,
    },
    {
      label: "SFU 节点在线",
      value: nodes.status === "success" ? `${onlineNodes} / ${nodeList.length}` : "—",
      note: nodes.status === "error" ? "节点接口暂不可用" : "ONLINE / 全部节点",
      icon: <ServerIcon />,
    },
    {
      label: "语音在线人数",
      value: nodes.status === "success" ? String(voiceUsers) : "—",
      note: "各节点心跳上报聚合",
      icon: <ActivityIcon />,
    },
    {
      label: "平台身份",
      value: user.system_admin ? "系统管理员" : "普通用户",
      note: "权限由后端权威裁决",
      icon: <ShieldCheckIcon />,
    },
  ]

  const shortcuts = [
    { title: "SFU 节点", description: "Enrollment、调度开关与健康度", url: "/voice/nodes", icon: ServerIcon },
    { title: "多维限制", description: "文字看/说、语音听/说的制裁层", url: "/governance/restrictions", icon: ShieldOffIcon },
    { title: "舞台管理", description: "FREE / STAGE 模式与申请队列", url: "/stage", icon: MicIcon },
  ]

  return (
    <main ref={containerRef} className="@container/main flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader title="控制台" description={`欢迎回来，${user.username}。这里汇总平台的服务器、语音基础设施与治理概况。`} />

      <div className="grid grid-cols-1 gap-4 px-4 *:data-[slot=card]:bg-linear-to-t *:data-[slot=card]:from-primary/5 *:data-[slot=card]:to-card *:data-[slot=card]:shadow-xs lg:px-6 @xl/main:grid-cols-2 @5xl/main:grid-cols-4">
        {metrics.map(metric => (
          <Card key={metric.label} className="metric-card @container/card">
            <CardHeader>
              <CardDescription>{metric.label}</CardDescription>
              <CardTitle className="text-2xl font-semibold tabular-nums @[250px]/card:text-3xl">
                {/* 数字更新 pop-in：key 变化触发重新入场 */}
                <span key={metric.value} className="t-number-pop">
                  {metric.value}
                </span>
              </CardTitle>
              <CardAction>
                <Badge variant="outline">{metric.icon}</Badge>
              </CardAction>
            </CardHeader>
            <CardFooter className="text-sm text-muted-foreground">{metric.note}</CardFooter>
          </Card>
        ))}
      </div>

      <section className="grid gap-4 px-4 lg:grid-cols-3 lg:px-6">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle className="text-base">我的服务器</CardTitle>
            <CardDescription>点击进入服务器详情，管理频道、成员、角色与权限覆盖。</CardDescription>
          </CardHeader>
          <div className="grid gap-2 px-6 pb-6 sm:grid-cols-2">
            {guilds.length ? (
              guilds.map((guild, index) => (
                <Link
                  key={guild.id}
                  to={`/servers/${guild.id}`}
                  style={{ "--stagger-index": index } as React.CSSProperties}
                  className="anim-item group flex min-h-11 items-center justify-between gap-3 rounded-xl border px-4 py-3 transition-[background-color,border-color] hover:border-primary/40 hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none active:scale-[0.99]"
                >
                  <div className="min-w-0">
                    <p className="truncate text-sm font-medium">{guild.name}</p>
                    <p className="truncate font-mono text-xs text-muted-foreground">{guild.id}</p>
                  </div>
                  <ArrowRightIcon className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
                </Link>
              ))
            ) : (
              <p className="col-span-full py-4 text-sm text-muted-foreground">
                尚未加入服务器，请前往「服务器」页面创建。
              </p>
            )}
          </div>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">快捷入口</CardTitle>
            <CardDescription>常用治理与语音运维操作。</CardDescription>
          </CardHeader>
          <div className="flex flex-col gap-2 px-6 pb-6">
            {shortcuts.map((item, index) => (
              <Link
                key={item.url}
                to={item.url}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item group flex items-center gap-3 rounded-xl border px-4 py-3 transition-[background-color,border-color] hover:border-primary/40 hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none active:scale-[0.99]"
              >
                <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-muted text-foreground">
                  <item.icon className="size-4" />
                </span>
                <span className="min-w-0">
                  <span className="block text-sm font-medium">{item.title}</span>
                  <span className="block truncate text-xs text-muted-foreground">{item.description}</span>
                </span>
                <ArrowRightIcon className="ml-auto size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
              </Link>
            ))}
          </div>
        </Card>
      </section>
    </main>
  )
}
