import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { InfoIcon, NetworkIcon, RefreshCwIcon } from "lucide-react"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { Button } from "~/components/ui/button"
import {
  getSfuTopology,
  type SfuNode,
  type SfuTopology,
  type SfuTopologyAggEdge,
  type SfuTopologyControlLink,
  type SfuTopologyPathType,
  type SfuTopologyServer,
} from "~/lib/api"
import { cn } from "~/lib/utils"

const POLL_MS = 2000
const SERVER_NODE_ID = "owl-server"

type Point = { x: number; y: number }

type RateSample = { tx: number; rx: number; t: number }
type EdgeRates = { bpsTx: number; bpsRx: number }

function edgeKey(e: Pick<SfuTopologyAggEdge, "parent_node_id" | "child_node_id">) {
  return `${e.parent_node_id}|${e.child_node_id}`
}

function formatBps(bps: number): string {
  if (!Number.isFinite(bps) || bps < 0) return "—"
  if (bps < 1000) return `${Math.round(bps)} bps`
  if (bps < 1_000_000) return `${(bps / 1000).toFixed(1)} Kbps`
  if (bps < 1_000_000_000) return `${(bps / 1_000_000).toFixed(2)} Mbps`
  return `${(bps / 1_000_000_000).toFixed(2)} Gbps`
}

function pathLabel(path: SfuTopologyPathType): string {
  switch (path) {
    case "lan":
      return "内网"
    case "wan":
      return "外网"
    default:
      return "未知"
  }
}

/**
 * 路径主色：内网绿 / 外网琥珀 / 未知灰。
 * 方向（上下行）不改主色，靠箭头指向区分。
 */
function pathFlowColor(path: SfuTopologyPathType, edgeUp: boolean): string {
  if (!edgeUp) return "color-mix(in oklch, var(--muted-foreground) 42%, transparent)"
  if (path === "lan") return "oklch(0.68 0.17 152)" // 内网 · 绿
  if (path === "wan") return "oklch(0.72 0.17 55)" // 外网 · 琥珀
  return "color-mix(in oklch, var(--muted-foreground) 72%, transparent)"
}

function flowStrokeWidth(bps: number, active: boolean): number {
  if (!active) return 1.35
  // 约 8 kbps 起可见加粗，上限 6px
  return Math.min(6, 1.6 + Math.log10(1 + Math.max(0, bps) / 4000) * 1.6)
}

type Seg = { x1: number; y1: number; x2: number; y2: number; mx: number; my: number }

/**
 * 计算两点间平行偏移连线（用于双向流量分轨）。
 * offset > 0 时向线段法线正侧偏移，形成两条平行轨。
 */
function parallelSegment(
  from: Point,
  to: Point,
  padFrom: number,
  padTo: number,
  offset: number,
): Seg | null {
  const dx = to.x - from.x
  const dy = to.y - from.y
  const len = Math.hypot(dx, dy)
  if (len < 1) return null
  const ux = dx / len
  const uy = dy / len
  // 法线（逆时针 90°）
  const nx = -uy
  const ny = ux
  const x1 = from.x + ux * padFrom + nx * offset
  const y1 = from.y + uy * padFrom + ny * offset
  const x2 = to.x - ux * padTo + nx * offset
  const y2 = to.y - uy * padTo + ny * offset
  return { x1, y1, x2, y2, mx: (x1 + x2) / 2, my: (y1 + y2) / 2 }
}

/** 在线段末端绘制实心箭头（比 SVG marker 更清晰、随线色） */
function arrowHeadPoints(seg: Seg, size = 9): string {
  const dx = seg.x2 - seg.x1
  const dy = seg.y2 - seg.y1
  const len = Math.hypot(dx, dy) || 1
  const ux = dx / len
  const uy = dy / len
  const nx = -uy
  const ny = ux
  const tipX = seg.x2
  const tipY = seg.y2
  const baseX = tipX - ux * size
  const baseY = tipY - uy * size
  const half = size * 0.55
  const lX = baseX + nx * half
  const lY = baseY + ny * half
  const rX = baseX - nx * half
  const rY = baseY - ny * half
  return `${tipX},${tipY} ${lX},${lY} ${rX},${rY}`
}

/** 标签色：严格跟随内网/外网路径 */
function pathLabelClass(path: SfuTopologyPathType, edgeUp: boolean): string {
  if (!edgeUp) return "bg-muted/90 text-muted-foreground"
  if (path === "lan") return "bg-emerald-500/18 text-emerald-800 dark:text-emerald-300"
  if (path === "wan") return "bg-amber-500/18 text-amber-900 dark:text-amber-300"
  return "bg-muted/90 text-muted-foreground"
}

/**
 * 布局：Server 居中，SFU 节点环绕。
 * 无 SFU 时 Server 仍居中显示。
 */
function layoutTopology(
  server: SfuTopologyServer | null,
  nodes: SfuNode[],
  width: number,
  height: number,
): Map<string, Point> {
  const map = new Map<string, Point>()
  const cx = width / 2
  const cy = height / 2
  if (server) {
    map.set(server.id || SERVER_NODE_ID, { x: cx, y: cy })
  }
  if (nodes.length === 0) return map
  if (nodes.length === 1) {
    // 单节点：放在 Server 右侧，避免重叠
    map.set(nodes[0].node_id, { x: cx + Math.min(180, width * 0.28), y: cy })
    return map
  }
  const rx = Math.max(140, width * 0.36)
  const ry = Math.max(100, height * 0.34)
  nodes.forEach((node, i) => {
    const angle = (Math.PI * 2 * i) / nodes.length - Math.PI / 2
    map.set(node.node_id, {
      x: cx + rx * Math.cos(angle),
      y: cy + ry * Math.sin(angle),
    })
  })
  return map
}

function computeRates(
  edges: SfuTopologyAggEdge[],
  prev: Map<string, RateSample>,
  now: number,
): { rates: Map<string, EdgeRates>; next: Map<string, RateSample> } {
  const rates = new Map<string, EdgeRates>()
  const next = new Map<string, RateSample>()
  for (const e of edges) {
    const k = edgeKey(e)
    const sample: RateSample = { tx: e.bytes_tx, rx: e.bytes_rx, t: now }
    next.set(k, sample)
    const old = prev.get(k)
    if (!old || now <= old.t) {
      rates.set(k, { bpsTx: 0, bpsRx: 0 })
      continue
    }
    const dt = (now - old.t) / 1000
    if (dt < 0.2) {
      rates.set(k, { bpsTx: 0, bpsRx: 0 })
      continue
    }
    const dTx = Math.max(0, e.bytes_tx - old.tx)
    const dRx = Math.max(0, e.bytes_rx - old.rx)
    rates.set(k, { bpsTx: (dTx * 8) / dt, bpsRx: (dRx * 8) / dt })
  }
  return { rates, next }
}

export function SfuTopologyInfoButton({ className }: { className?: string }) {
  const [open, setOpen] = useState(false)

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className={cn("text-muted-foreground hover:text-foreground", className)}
        aria-label="查看级联拓扑"
        title="级联拓扑"
        onClick={() => setOpen(true)}
      >
        <InfoIcon className="size-4" />
      </Button>
      <SfuTopologyDialog open={open} onOpenChange={setOpen} />
    </>
  )
}

export function SfuTopologyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [data, setData] = useState<SfuTopology | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [rates, setRates] = useState<Map<string, EdgeRates>>(new Map())
  const prevSamples = useRef<Map<string, RateSample>>(new Map())
  const [tick, setTick] = useState(0)

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const topo = await getSfuTopology()
      const now = Date.now()
      const { rates: nextRates, next } = computeRates(topo.aggregated_edges ?? [], prevSamples.current, now)
      prevSamples.current = next
      setRates(nextRates)
      setData(topo)
      setError(null)
      setTick(t => t + 1)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "加载拓扑失败")
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!open) return
    void load(false)
    const timer = setInterval(() => void load(true), POLL_MS)
    return () => clearInterval(timer)
  }, [open, load])

  const width = 760
  const height = 460
  const nodes = data?.nodes ?? []
  const edges = data?.aggregated_edges ?? []
  const server = data?.server ?? null
  const controlLinks: SfuTopologyControlLink[] = data?.control_links ?? []
  const positions = useMemo(
    () => layoutTopology(server, nodes, width, height),
    [server, nodes, tick],
  )

  const totalUsers = nodes.reduce((sum, n) => sum + (n.capacity?.current_users ?? 0), 0)
  const upEdges = edges.filter(e => e.up).length
  const upControl = controlLinks.filter(l => l.up).length
  const serverId = server?.id || SERVER_NODE_ID
  const serverPos = positions.get(serverId)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl gap-3 p-5">
        <DialogHeader className="pr-8">
          <DialogTitle className="flex items-center gap-2">
            <NetworkIcon className="size-4 text-muted-foreground" />
            集群拓扑
          </DialogTitle>
          <DialogDescription>
            中心为 Newt-Server 控制面；环上为 SFU。蓝色虚线为 gRPC 控制通道；节点间双向媒体连线带箭头指示方向：
            <span className="font-medium text-emerald-700 dark:text-emerald-400"> 绿色=内网</span>、
            <span className="font-medium text-amber-700 dark:text-amber-400"> 琥珀色=外网</span>
            ，线宽反映实时速率。约每 {POLL_MS / 1000}s 刷新。
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <span>
            Server{" "}
            <span className={cn("font-medium", server?.online ? "text-emerald-600 dark:text-emerald-400" : "text-foreground")}>
              {server?.online ? "在线" : "—"}
            </span>
          </span>
          <span>
            SFU <span className="font-medium text-foreground tabular-nums">{nodes.length}</span>
            {server && (
              <span className="text-muted-foreground">
                {" "}
                · 控制连 <span className="font-medium text-foreground tabular-nums">{upControl}</span>/{controlLinks.length || nodes.length}
              </span>
            )}
          </span>
          <span>
            级联边 <span className="font-medium text-foreground tabular-nums">{upEdges}</span>
            {edges.length > 0 && <span className="text-muted-foreground"> / {edges.length}</span>}
          </span>
          <span>
            活跃用户 <span className="font-medium text-foreground tabular-nums">{totalUsers}</span>
          </span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="ml-auto h-7 gap-1 px-2"
            onClick={() => void load(false)}
            disabled={loading}
          >
            <RefreshCwIcon className={cn("size-3.5", loading && "animate-spin")} />
            刷新
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-4 rounded-xl border bg-muted/30 px-3 py-2 text-[11px]">
          <span className="flex items-center gap-1.5">
            <span className="inline-block size-2.5 rounded-sm bg-sky-500" />
            Newt-Server
          </span>
          <span className="flex items-center gap-1.5">
            <span className="inline-block h-0.5 w-4 border-t-2 border-dashed border-sky-500/80" />
            控制通道
          </span>
          <span className="flex items-center gap-1.5">
            <span className="inline-block size-2.5 rounded-full bg-emerald-500" />
            <span className="font-medium text-emerald-700 dark:text-emerald-400">内网 LAN</span>
          </span>
          <span className="flex items-center gap-1.5">
            <span className="inline-block size-2.5 rounded-full bg-amber-500" />
            <span className="font-medium text-amber-700 dark:text-amber-400">外网 WAN</span>
          </span>
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <svg width="28" height="10" viewBox="0 0 28 10" aria-hidden className="shrink-0">
              <line x1="2" y1="5" x2="18" y2="5" stroke="currentColor" strokeWidth="1.6" />
              <polygon points="26,5 18,1.5 18,8.5" fill="currentColor" />
            </svg>
            箭头 = 流量方向
          </span>
          <span className="text-muted-foreground">↓下行 P→C · ↑上行 C→P · 线宽∝速率</span>
        </div>

        {error && !data && (
          <div className="rounded-xl border border-destructive/30 bg-destructive/5 px-4 py-6 text-center text-sm text-destructive">
            {error}
          </div>
        )}

        {data && !server && nodes.length === 0 && (
          <div className="rounded-xl border bg-muted/20 px-4 py-10 text-center text-sm text-muted-foreground">
            暂无拓扑数据
          </div>
        )}

        {data && (server || nodes.length > 0) && (
          <div className="relative overflow-hidden rounded-2xl border bg-card">
            <svg
              viewBox={`0 0 ${width} ${height}`}
              className="h-auto w-full"
              role="img"
              aria-label="Newt-Server 与 SFU 集群拓扑图"
            >
              <defs>
                {/* 控制通道箭头；媒体流量用几何箭头（与线同色）保证指向清晰 */}
                <marker
                  id="topo-arrow-control"
                  viewBox="0 0 12 12"
                  refX="10"
                  refY="6"
                  markerWidth="7"
                  markerHeight="7"
                  orient="auto"
                  markerUnits="userSpaceOnUse"
                >
                  <path d="M 0 1 L 10 6 L 0 11 z" fill="oklch(0.65 0.14 240)" />
                </marker>
              </defs>

              {/* Server → SFU 控制通道（虚线，无媒体） */}
              {serverPos &&
                controlLinks.map(link => {
                  const to = positions.get(link.node_id)
                  if (!to) return null
                  const dx = to.x - serverPos.x
                  const dy = to.y - serverPos.y
                  const len = Math.hypot(dx, dy) || 1
                  const padFrom = 42
                  const padTo = 34
                  const x1 = serverPos.x + (dx / len) * padFrom
                  const y1 = serverPos.y + (dy / len) * padFrom
                  const x2 = to.x - (dx / len) * padTo
                  const y2 = to.y - (dy / len) * padTo
                  const mx = (x1 + x2) / 2
                  const my = (y1 + y2) / 2
                  return (
                    <g key={`ctl-${link.node_id}`}>
                      <line
                        x1={x1}
                        y1={y1}
                        x2={x2}
                        y2={y2}
                        stroke={link.up ? "oklch(0.65 0.14 240)" : "color-mix(in oklch, var(--muted-foreground) 45%, transparent)"}
                        strokeWidth={1.4}
                        strokeLinecap="round"
                        strokeDasharray="5 4"
                        markerEnd={link.up ? "url(#topo-arrow-control)" : undefined}
                        opacity={link.up ? 0.75 : 0.35}
                      />
                      <foreignObject x={mx - 36} y={my - 10} width={72} height={18}>
                        <div className="flex justify-center">
                          <span
                            className={cn(
                              "rounded px-1 text-[9px] font-medium backdrop-blur-sm",
                              link.up
                                ? "bg-sky-500/15 text-sky-700 dark:text-sky-300"
                                : "bg-muted/80 text-muted-foreground",
                            )}
                          >
                            {link.up ? "控制" : "未连"}
                          </span>
                        </div>
                      </foreignObject>
                    </g>
                  )
                })}

              {/* SFU 级联媒体：双向分轨；颜色=内网/外网，箭头=方向 */}
              {edges.map(edge => {
                const parentPos = positions.get(edge.parent_node_id)
                const childPos = positions.get(edge.child_node_id)
                if (!parentPos || !childPos) return null
                const k = edgeKey(edge)
                const rate = rates.get(k) ?? { bpsTx: 0, bpsRx: 0 }
                const railOffset = 10
                // 末端多留空间给箭头，避免箭头顶进节点圆
                const padFrom = 34
                const padTo = 40
                const downSeg = parallelSegment(parentPos, childPos, padFrom, padTo, railOffset)
                const upSeg = parallelSegment(childPos, parentPos, padFrom, padTo, railOffset)
                if (!downSeg || !upSeg) return null

                const stroke = pathFlowColor(edge.path_type, edge.up)
                const labelCls = pathLabelClass(edge.path_type, edge.up)
                const downW = flowStrokeWidth(rate.bpsTx, edge.up)
                const upW = flowStrokeWidth(rate.bpsRx, edge.up)
                const downActive = edge.up && rate.bpsTx > 400
                const upActive = edge.up && rate.bpsRx > 400
                const labelX = (downSeg.mx + upSeg.mx) / 2
                const labelY = (downSeg.my + upSeg.my) / 2
                const arrowSize = edge.up ? 10 : 8

                const renderFlowRail = (
                  seg: Seg,
                  dir: "down" | "up",
                  bps: number,
                  width: number,
                  active: boolean,
                  dirGlyph: string,
                ) => {
                  // 线画到箭头根部，避免与箭头重叠
                  const dx = seg.x2 - seg.x1
                  const dy = seg.y2 - seg.y1
                  const len = Math.hypot(dx, dy) || 1
                  const ux = dx / len
                  const uy = dy / len
                  const lineEndX = seg.x2 - ux * (arrowSize * 0.85)
                  const lineEndY = seg.y2 - uy * (arrowSize * 0.85)
                  const opacity = edge.up ? (bps > 0 ? 0.98 : 0.7) : 0.35

                  return (
                    <g key={`${k}-${dir}`}>
                      <line
                        x1={seg.x1}
                        y1={seg.y1}
                        x2={lineEndX}
                        y2={lineEndY}
                        stroke={stroke}
                        strokeWidth={width}
                        strokeLinecap="round"
                        strokeDasharray={edge.up ? undefined : "6 5"}
                        opacity={opacity}
                      />
                      {/* 实心箭头：指向明确，颜色与路径一致 */}
                      <polygon
                        points={arrowHeadPoints(seg, arrowSize)}
                        fill={stroke}
                        opacity={opacity}
                        stroke={stroke}
                        strokeWidth={0.5}
                        strokeLinejoin="round"
                      />
                      {active && (
                        <line
                          x1={seg.x1}
                          y1={seg.y1}
                          x2={lineEndX}
                          y2={lineEndY}
                          stroke={stroke}
                          strokeWidth={Math.max(1.2, width - 0.5)}
                          strokeLinecap="round"
                          strokeDasharray="6 8"
                          opacity={0.75}
                        >
                          <animate
                            attributeName="stroke-dashoffset"
                            from="32"
                            to="0"
                            dur={dir === "down" ? "0.8s" : "0.95s"}
                            repeatCount="indefinite"
                          />
                        </line>
                      )}
                      <foreignObject x={seg.mx - 48} y={seg.my - 12} width={96} height={20}>
                        <div className="flex justify-center">
                          <span
                            className={cn(
                              "inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 font-mono text-[9px] font-medium tabular-nums shadow-xs backdrop-blur-sm",
                              labelCls,
                            )}
                          >
                            <span aria-hidden>{dirGlyph}</span>
                            {formatBps(bps)}
                          </span>
                        </div>
                      </foreignObject>
                    </g>
                  )
                }

                return (
                  <g key={k}>
                    {renderFlowRail(downSeg, "down", rate.bpsTx, downW, downActive, "↓")}
                    {renderFlowRail(upSeg, "up", rate.bpsRx, upW, upActive, "↑")}

                    {/* 路径类型徽章（内网绿 / 外网琥珀） */}
                    <foreignObject x={labelX - 60} y={labelY - 32} width={120} height={22}>
                      <div className="flex justify-center">
                        <span
                          className={cn(
                            "inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[9px] font-semibold shadow-xs backdrop-blur-sm",
                            labelCls,
                          )}
                        >
                          <span
                            className={cn(
                              "inline-block size-1.5 rounded-full",
                              edge.path_type === "lan" && edge.up
                                ? "bg-emerald-500"
                                : edge.path_type === "wan" && edge.up
                                  ? "bg-amber-500"
                                  : "bg-muted-foreground/50",
                            )}
                          />
                          {pathLabel(edge.path_type)}
                          {edge.up ? "" : " · 断开"}
                          {edge.room_count > 1 ? ` · ${edge.room_count}房` : ""}
                          {edge.rtt_ms > 0 ? ` · ${edge.rtt_ms.toFixed(0)}ms` : ""}
                        </span>
                      </div>
                    </foreignObject>
                  </g>
                )
              })}

              {/* Newt-Server 中心节点 */}
              {server && serverPos && (
                <g transform={`translate(${serverPos.x}, ${serverPos.y})`}>
                  <rect
                    x={-48}
                    y={-34}
                    width={96}
                    height={68}
                    rx={14}
                    fill="var(--card)"
                    stroke="oklch(0.65 0.14 240)"
                    strokeWidth={2.5}
                  />
                  <rect
                    x={-48}
                    y={-34}
                    width={96}
                    height={18}
                    rx={14}
                    fill="oklch(0.65 0.14 240 / 0.15)"
                  />
                  {/* 顶部圆角条补齐 */}
                  <path
                    d="M -48 -16 L -48 -20 Q -48 -34 -34 -34 L 34 -34 Q 48 -34 48 -20 L 48 -16 Z"
                    fill="oklch(0.65 0.14 240 / 0.15)"
                  />
                  <circle
                    r={5}
                    cx={40}
                    cy={-26}
                    fill={server.online ? "oklch(0.72 0.17 155)" : "color-mix(in oklch, var(--muted-foreground) 55%, transparent)"}
                  />
                  <text textAnchor="middle" y={-18} className="fill-sky-700 dark:fill-sky-300" style={{ fontSize: 9, fontWeight: 600 }}>
                    控制面
                  </text>
                  <text textAnchor="middle" y={4} className="fill-foreground" style={{ fontSize: 12, fontWeight: 700 }}>
                    {(server.display_name || "Newt-Server").slice(0, 12)}
                  </text>
                  <text textAnchor="middle" y={20} className="fill-muted-foreground" style={{ fontSize: 9 }}>
                    已连 {server.connected_sfu_count}/{nodes.length} SFU
                  </text>
                </g>
              )}

              {/* SFU 节点 */}
              {nodes.map(node => {
                const p = positions.get(node.node_id)
                if (!p) return null
                const users = node.capacity?.current_users ?? 0
                const max = node.capacity?.max_users ?? 0
                const online = Boolean(node.online) || node.status === "ONLINE"
                const region = node.labels?.region
                return (
                  <g key={node.node_id} transform={`translate(${p.x}, ${p.y})`}>
                    <circle
                      r={30}
                      fill="var(--card)"
                      stroke={online ? "oklch(0.72 0.17 155)" : "color-mix(in oklch, var(--muted-foreground) 50%, transparent)"}
                      strokeWidth={online ? 2.5 : 1.5}
                    />
                    <circle
                      r={6}
                      cx={20}
                      cy={-20}
                      fill={online ? "oklch(0.72 0.17 155)" : "color-mix(in oklch, var(--muted-foreground) 55%, transparent)"}
                    />
                    <text
                      textAnchor="middle"
                      y={-2}
                      className="fill-foreground"
                      style={{ fontSize: 11, fontWeight: 600 }}
                    >
                      {(node.display_name || node.node_id).slice(0, 12)}
                    </text>
                    <text textAnchor="middle" y={14} className="fill-muted-foreground" style={{ fontSize: 10 }}>
                      {users}
                      {max > 0 ? `/${max}` : ""} 人
                    </text>
                    {region && (
                      <text textAnchor="middle" y={46} className="fill-muted-foreground" style={{ fontSize: 9 }}>
                        {region}
                      </text>
                    )}
                  </g>
                )
              })}
            </svg>

            {server && (
              <div className="pointer-events-none absolute top-2 right-2 max-w-[220px] rounded-lg border bg-background/85 px-2.5 py-1.5 text-[10px] text-muted-foreground shadow-xs backdrop-blur-sm">
                <div className="font-medium text-foreground">Server 控制端点</div>
                <div className="mt-0.5 font-mono break-all">
                  {server.sfu_control_endpoint || server.sfu_control_listen || "—"}
                </div>
                {server.http_address && (
                  <div className="mt-0.5 font-mono break-all">HTTP {server.http_address}</div>
                )}
              </div>
            )}

            {edges.length === 0 && nodes.length > 0 && (
              <p className="pointer-events-none absolute inset-x-0 bottom-3 text-center text-xs text-muted-foreground">
                当前无活跃级联边（多节点同房互听时才会建 SFU↔SFU 媒体边）
              </p>
            )}
          </div>
        )}

        {error && data && (
          <p className="text-xs text-muted-foreground">最近一次刷新失败（{error}），展示的是缓存数据。</p>
        )}

        {/* 控制通道明细 */}
        {data && controlLinks.length > 0 && (
          <div className="max-h-24 overflow-auto rounded-xl border">
            <table className="w-full text-left text-[11px]">
              <thead className="sticky top-0 bg-muted/80 text-muted-foreground backdrop-blur-sm">
                <tr>
                  <th className="px-2 py-1.5 font-medium">控制通道</th>
                  <th className="px-2 py-1.5 font-medium">类型</th>
                  <th className="px-2 py-1.5 font-medium">状态</th>
                  <th className="px-2 py-1.5 font-medium">节点用户</th>
                </tr>
              </thead>
              <tbody>
                {controlLinks.map(link => {
                  const node = nodes.find(n => n.node_id === link.node_id)
                  const users = node?.capacity?.current_users ?? 0
                  const max = node?.capacity?.max_users ?? 0
                  return (
                    <tr key={`ctl-row-${link.node_id}`} className="border-t">
                      <td className="px-2 py-1 font-mono">
                        <span className="text-sky-700 dark:text-sky-300">{server?.display_name ?? "Newt-Server"}</span>
                        <span className="text-muted-foreground"> → </span>
                        {node?.display_name ?? link.node_id.slice(0, 8)}
                      </td>
                      <td className="px-2 py-1 text-muted-foreground">gRPC 控制</td>
                      <td className="px-2 py-1">
                        <span
                          className={cn(
                            "rounded px-1 py-0.5 font-medium",
                            link.up
                              ? "bg-sky-500/15 text-sky-700 dark:text-sky-300"
                              : "bg-muted text-muted-foreground",
                          )}
                        >
                          {link.up ? "已连接" : "未连接"}
                        </span>
                      </td>
                      <td className="px-2 py-1 font-mono tabular-nums">
                        {users}
                        {max > 0 ? ` / ${max}` : ""}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        {/* 级联媒体边明细 */}
        {data && edges.length > 0 && (
          <div className="max-h-28 overflow-auto rounded-xl border">
            <table className="w-full text-left text-[11px]">
              <thead className="sticky top-0 bg-muted/80 text-muted-foreground backdrop-blur-sm">
                <tr>
                  <th className="px-2 py-1.5 font-medium">级联边 Parent ↔ Child</th>
                  <th className="px-2 py-1.5 font-medium">路径</th>
                  <th className="px-2 py-1.5 font-medium">下行 P→C</th>
                  <th className="px-2 py-1.5 font-medium">上行 C→P</th>
                  <th className="px-2 py-1.5 font-medium">RTT</th>
                  <th className="px-2 py-1.5 font-medium">房间</th>
                </tr>
              </thead>
              <tbody>
                {edges.map(edge => {
                  const parent = nodes.find(n => n.node_id === edge.parent_node_id)
                  const child = nodes.find(n => n.node_id === edge.child_node_id)
                  const rate = rates.get(edgeKey(edge)) ?? { bpsTx: 0, bpsRx: 0 }
                  return (
                    <tr key={edgeKey(edge)} className="border-t">
                      <td className="px-2 py-1 font-mono">
                        {parent?.display_name ?? edge.parent_node_id.slice(0, 8)}
                        <span className="text-muted-foreground"> ↔ </span>
                        {child?.display_name ?? edge.child_node_id.slice(0, 8)}
                      </td>
                      <td className="px-2 py-1">
                        <span
                          className={cn(
                            "rounded px-1 py-0.5 font-medium",
                            edge.path_type === "lan"
                              ? "bg-emerald-500/15 text-emerald-700 dark:text-emerald-300"
                              : edge.path_type === "wan"
                                ? "bg-amber-500/15 text-amber-800 dark:text-amber-300"
                                : "bg-muted text-muted-foreground",
                          )}
                        >
                          {pathLabel(edge.path_type)}
                          {!edge.up && " · 断"}
                        </span>
                        {(edge.local_ip || edge.remote_ip) && (
                          <span className="ml-1 font-mono text-muted-foreground">
                            {edge.local_ip || "?"}↔{edge.remote_ip || "?"}
                          </span>
                        )}
                      </td>
                      <td
                        className={cn(
                          "px-2 py-1 font-mono tabular-nums",
                          edge.path_type === "lan"
                            ? "text-emerald-700 dark:text-emerald-300"
                            : edge.path_type === "wan"
                              ? "text-amber-700 dark:text-amber-300"
                              : "text-muted-foreground",
                        )}
                      >
                        ↓ {formatBps(rate.bpsTx)}
                      </td>
                      <td
                        className={cn(
                          "px-2 py-1 font-mono tabular-nums",
                          edge.path_type === "lan"
                            ? "text-emerald-700 dark:text-emerald-300"
                            : edge.path_type === "wan"
                              ? "text-amber-700 dark:text-amber-300"
                              : "text-muted-foreground",
                        )}
                      >
                        ↑ {formatBps(rate.bpsRx)}
                      </td>
                      <td className="px-2 py-1 font-mono tabular-nums">
                        {edge.rtt_ms > 0 ? `${edge.rtt_ms.toFixed(1)} ms` : "—"}
                      </td>
                      <td className="px-2 py-1 tabular-nums">{edge.room_count}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
