import type { SfuNodeStatus } from "~/lib/api"
import { cn } from "~/lib/utils"

export const NODE_STATUS_META: Record<SfuNodeStatus, { label: string; className: string; dot: string; pulse?: boolean }> = {
  PENDING_ENROLLMENT: {
    label: "待接入",
    className: "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
    dot: "bg-amber-500",
  },
  ENROLLED: {
    label: "已注册",
    className: "border-border bg-muted text-muted-foreground",
    dot: "bg-muted-foreground",
  },
  ONLINE: {
    label: "在线",
    className: "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
    dot: "bg-emerald-500",
    pulse: true,
  },
  DRAINING: {
    label: "排空中",
    className: "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
    dot: "bg-sky-500",
    pulse: true,
  },
  DISABLED: {
    label: "已禁用",
    className: "border-border bg-muted text-muted-foreground opacity-80",
    dot: "bg-muted-foreground",
  },
  REVOKED: {
    label: "已吊销",
    className: "border-destructive/30 bg-destructive/10 text-destructive",
    dot: "bg-destructive",
  },
}

export function NodeStatusBadge({ status, className }: { status: SfuNodeStatus; className?: string }) {
  const meta = NODE_STATUS_META[status] ?? NODE_STATUS_META.ENROLLED
  return (
    <span
      data-node-status={status}
      className={cn(
        "inline-flex h-6 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium whitespace-nowrap",
        meta.className,
        className
      )}
    >
      <span className="relative flex size-1.5">
        {meta.pulse && <span className={cn("absolute inline-flex h-full w-full animate-ping rounded-full opacity-60", meta.dot)} />}
        <span className={cn("relative inline-flex size-1.5 rounded-full", meta.dot)} />
      </span>
      {/* 状态文字切换：key 变化触发 text swap 入场 */}
      <span key={status} className="t-text-swap">
        {meta.label}
      </span>
    </span>
  )
}
