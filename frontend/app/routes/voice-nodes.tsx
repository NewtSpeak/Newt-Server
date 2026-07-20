import { useEffect, useRef, useState, type FormEvent } from "react"
import {
  CpuIcon,
  EllipsisVerticalIcon,
  PauseCircleIcon,
  PlayCircleIcon,
  PlusIcon,
  ServerIcon,
  ShieldAlertIcon,
  TriangleAlertIcon,
  Users2Icon,
  WavesIcon,
} from "lucide-react"
import { toast } from "sonner"

import { CopyButton } from "~/components/copy-button"
import { NodeStatusBadge } from "~/components/node-status-badge"
import { PageHeader } from "~/components/page-header"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import {
  createSfuNode,
  listSfuNodes,
  sfuNodeAction,
  type SfuNode,
  type SfuNodeAction,
  type SfuNodeCreated,
} from "~/lib/api"
import { formatRelative } from "~/lib/format"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"
import { cn } from "~/lib/utils"

const ACTION_LABELS: Record<SfuNodeAction, string> = {
  enable: "启用调度",
  disable: "停用调度",
  drain: "排空节点",
  undrain: "结束排空",
  revoke: "吊销证书",
}

export default function VoiceNodesPage() {
  const nodes = useAsyncData<SfuNode[]>(() => listSfuNodes(), [])
  const containerRef = useRef<HTMLDivElement>(null)
  const previousStatuses = useRef<Map<string, string>>(new Map())

  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<SfuNodeCreated | null>(null)

  // 轮询 15s 刷新（节点生命周期走 internal.NODE_* 内部事件，不下发客户端）；
  // 节点池变更事件作为辅助信号（授权/勾选变化通常伴随节点面板操作）。
  useEffect(() => {
    const timer = setInterval(() => nodes.reload(true), 15_000)
    return () => clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useGatewayEvent("VOICE_NODE_POOL_UPDATE", () => nodes.reload(true))

  // 列表入场 stagger
  useGSAP(
    () => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.from(".node-card", {
          autoAlpha: 0,
          y: 14,
          duration: MOTION.enter,
          ease: MOTION.ease,
          stagger: MOTION.stagger,
          clearProps: "all",
        })
      })
    },
    { dependencies: [nodes.status === "success"], scope: containerRef }
  )

  // 节点状态实时变化的强调动画（比较前后状态，仅对变化的卡片做 glow）
  useEffect(() => {
    if (!nodes.data) return
    const changed: string[] = []
    for (const node of nodes.data) {
      const previous = previousStatuses.current.get(node.node_id)
      if (previous && previous !== node.status) changed.push(node.node_id)
      previousStatuses.current.set(node.node_id, node.status)
    }
    if (changed.length === 0 || !containerRef.current) return
    const ctx = gsap.context(() => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        for (const id of changed) {
          gsap.fromTo(
            `[data-node-card="${id}"]`,
            { boxShadow: "0 0 0 3px color-mix(in oklch, var(--ring) 55%, transparent)" },
            { boxShadow: "0 0 0 0px transparent", duration: 0.9, ease: "power2.out", clearProps: "boxShadow" }
          )
          gsap.fromTo(`[data-node-card="${id}"] [data-node-status]`, { scale: 1.12 }, { scale: 1, duration: 0.4, ease: "back.out(2)" })
        }
      })
    }, containerRef)
    return () => ctx.revert()
  }, [nodes.data])

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const displayName = String(form.get("node-name") ?? "").trim()
    const region = String(form.get("node-region") ?? "").trim()
    if (!displayName) return
    setCreating(true)
    try {
      const result = await createSfuNode({ display_name: displayName, labels: region ? { region } : undefined })
      setCreated(result)
      nodes.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "节点创建失败")
    } finally {
      setCreating(false)
    }
  }

  async function onAction(node: SfuNode, action: SfuNodeAction) {
    if (action === "revoke" && !window.confirm(`吊销「${node.display_name}」的证书后需重新 Enrollment 才能接入，确定继续？`)) return
    try {
      await sfuNodeAction(node.node_id, action)
      toast.success(`已对「${node.display_name}」执行：${ACTION_LABELS[action]}`)
      nodes.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : `${ACTION_LABELS[action]}失败`)
    }
  }

  const list = nodes.data ?? []

  return (
    <main ref={containerRef} className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="SFU 节点"
        description="节点须完成 Enrollment（一次性令牌 → 证书 → mTLS）并显式启用调度后才进入调度池。"
        actions={
          <Button onClick={() => { setCreated(null); setCreateOpen(true) }}>
            <PlusIcon data-icon="inline-start" />
            创建节点
          </Button>
        }
      />

      <Dialog
        open={createOpen}
        onOpenChange={open => {
          setCreateOpen(open)
          if (!open) setCreated(null)
        }}
      >
        <DialogContent>
          {created ? (
            <>
              <DialogHeader>
                <DialogTitle>节点占位已创建</DialogTitle>
                <DialogDescription>
                  以下 Enrollment Token 为一次性凭证，<span className="font-medium text-foreground">仅此一次展示</span>
                  ，请通过安全渠道（如 SSH）下发到目标机器。
                </DialogDescription>
              </DialogHeader>
              <div className="grid gap-3">
                <div className="grid gap-1.5">
                  <Label>节点 ID</Label>
                  <div className="flex items-center gap-2 rounded-xl border bg-muted/40 px-3 py-2.5">
                    <code className="flex-1 truncate font-mono text-xs">{created.node_id}</code>
                    <CopyButton text={created.node_id} />
                  </div>
                </div>
                <div className="grid gap-1.5">
                  <Label>Enrollment Token（一次性）</Label>
                  <div className="flex items-center gap-2 rounded-xl border border-amber-500/40 bg-amber-500/5 px-3 py-2.5">
                    <code className="flex-1 truncate font-mono text-xs">{created.enrollment_token}</code>
                    <CopyButton text={created.enrollment_token} />
                  </div>
                  <p className="flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400">
                    <TriangleAlertIcon className="size-3.5" />
                    关闭此弹窗后无法再次查看；令牌短时有效且仅可使用一次。
                  </p>
                </div>
              </div>
              <DialogFooter>
                <Button onClick={() => setCreateOpen(false)}>我已保存，关闭</Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>创建 SFU 节点</DialogTitle>
                <DialogDescription>创建占位记录并签发一次性 Enrollment Token，节点凭它换取 mTLS 证书。</DialogDescription>
              </DialogHeader>
              <form onSubmit={onCreate} className="grid gap-4">
                <div className="grid gap-2">
                  <Label htmlFor="node-name">节点名称</Label>
                  <Input id="node-name" name="node-name" placeholder="如：sfu-sh-01" required maxLength={64} autoFocus />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="node-region">地域标签（可选）</Label>
                  <Input id="node-region" name="node-region" placeholder="如：cn-shanghai" maxLength={64} />
                </div>
                <DialogFooter>
                  <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                    取消
                  </Button>
                  <Button type="submit" disabled={creating}>
                    {creating ? "创建中…" : "创建并签发 Token"}
                  </Button>
                </DialogFooter>
              </form>
            </>
          )}
        </DialogContent>
      </Dialog>

      <section className="px-4 lg:px-6">
        {nodes.status === "loading" && <LoadingState rows={4} />}
        {nodes.status === "error" && !nodes.data && <ErrorState message={nodes.error} onRetry={() => nodes.reload()} />}
        {nodes.status === "success" && list.length === 0 && (
          <EmptyState
            icon={ServerIcon}
            title="还没有 SFU 节点"
            description="创建节点占位并下发 Enrollment Token，让 Owl-SFU 实例接入集群。"
          />
        )}
        {list.length > 0 && (
          <div className="grid gap-3 lg:grid-cols-2 2xl:grid-cols-3">
            {list.map(node => {
              const current = node.capacity?.current_users ?? 0
              const max = node.capacity?.max_users ?? 0
              const load = max > 0 ? Math.min(100, Math.round((current / max) * 100)) : 0
              const cpu = node.capacity?.cpu_pct
              return (
                <article
                  key={node.node_id}
                  data-node-card={node.node_id}
                  className="node-card flex flex-col gap-4 rounded-2xl border bg-card p-5 shadow-xs transition-[border-color] hover:border-primary/30"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h2 className="flex items-center gap-2 truncate font-medium">
                        <ServerIcon className="size-4 shrink-0 text-muted-foreground" />
                        {node.display_name}
                      </h2>
                      <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">{node.node_id}</p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      <NodeStatusBadge status={node.status} />
                      <DropdownMenu>
                        <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label="节点操作" />}>
                          <EllipsisVerticalIcon />
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onClick={() => onAction(node, "enable")}>
                            <PlayCircleIcon />
                            启用调度
                          </DropdownMenuItem>
                          <DropdownMenuItem onClick={() => onAction(node, "disable")}>
                            <PauseCircleIcon />
                            停用调度
                          </DropdownMenuItem>
                          <DropdownMenuItem onClick={() => onAction(node, "drain")}>
                            <WavesIcon />
                            排空节点
                          </DropdownMenuItem>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem variant="destructive" onClick={() => onAction(node, "revoke")}>
                            <ShieldAlertIcon />
                            吊销证书
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </div>

                  <div className="grid gap-2 text-xs text-muted-foreground">
                    <div className="flex items-center justify-between gap-2">
                      <span className="flex items-center gap-1">
                        <Users2Icon className="size-3.5" />
                        在线用户
                      </span>
                      <span className="font-medium text-foreground tabular-nums">
                        {current}
                        {max > 0 && <span className="text-muted-foreground"> / {max}</span>}
                      </span>
                    </div>
                    <div className="h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={load} aria-valuemin={0} aria-valuemax={100} aria-label="容量占用">
                      <div
                        className={cn(
                          "h-full rounded-full transition-[width] duration-500 ease-(--resize-ease)",
                          load >= 90 ? "bg-destructive" : load >= 70 ? "bg-amber-500" : "bg-emerald-500"
                        )}
                        style={{ width: `${load}%` }}
                      />
                    </div>
                    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 pt-1">
                      <span className="flex items-center gap-1">
                        <CpuIcon className="size-3.5" />
                        CPU <span className="font-medium text-foreground tabular-nums">{cpu != null ? `${Math.round(cpu)}%` : "—"}</span>
                      </span>
                      <span>
                        地域 <span className="font-medium text-foreground">{node.labels?.region ?? "—"}</span>
                      </span>
                      <span>
                        调度{" "}
                        <span className={cn("font-medium", node.enabled_for_scheduling ? "text-emerald-600 dark:text-emerald-400" : "text-foreground")}>
                          {node.enabled_for_scheduling ? "已启用" : "未启用"}
                        </span>
                      </span>
                      <span className="ml-auto">心跳 {formatRelative(node.last_seen_at)}</span>
                    </div>
                  </div>
                </article>
              )
            })}
          </div>
        )}
        {nodes.status === "error" && nodes.data && (
          <p className="mt-3 text-xs text-muted-foreground">最近一次刷新失败（{nodes.error}），展示的是缓存数据。</p>
        )}
      </section>
    </main>
  )
}
