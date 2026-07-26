import { CheckIcon, SettingsIcon, TriangleAlertIcon, XIcon } from "lucide-react"
import { Link } from "react-router"

import { ErrorState } from "~/components/states"
import { buttonVariants } from "~/components/ui/button"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "~/components/ui/sheet"
import { Skeleton } from "~/components/ui/skeleton"
import { cn } from "~/lib/utils"
import type { SfuDeployPreflight, SfuDeployPreflightCheck } from "~/lib/api"

const STATUS_STYLE = {
  ok: {
    icon: CheckIcon,
    dot: "bg-emerald-500",
    text: "text-emerald-700 dark:text-emerald-400",
    ring: "ring-emerald-500/30",
  },
  warn: {
    icon: TriangleAlertIcon,
    dot: "bg-amber-500",
    text: "text-amber-700 dark:text-amber-400",
    ring: "ring-amber-500/30",
  },
  error: {
    icon: XIcon,
    dot: "bg-destructive",
    text: "text-destructive",
    ring: "ring-destructive/35",
  },
} as const

function CheckCard({ check, index }: { check: SfuDeployPreflightCheck; index: number }) {
  const style = STATUS_STYLE[check.status] ?? STATUS_STYLE.ok
  const Icon = style.icon
  return (
    <div
      className="anim-item rounded-xl bg-muted/40 p-3"
      style={{ "--stagger-index": index } as React.CSSProperties}
    >
      <div className="flex items-center gap-2">
        <span className={cn("grid size-5 shrink-0 place-items-center rounded-full ring-1", style.ring, style.text)}>
          <Icon className="size-3" />
        </span>
        <p className="min-w-0 flex-1 truncate text-sm font-medium">{check.label}</p>
      </div>
      <p className={cn("mt-1.5 truncate font-mono text-xs", check.status === "ok" ? "text-muted-foreground" : style.text)}>
        {check.detail}
      </p>
      {check.hint && <p className="mt-1 text-xs text-muted-foreground">{check.hint}</p>}
    </div>
  )
}

type Props = {
  data: SfuDeployPreflight | null
  status: "idle" | "loading" | "success" | "error"
  error?: string
  onRetry: () => void
}

/** 展开态：三张检查卡，仅在表单模式下渲染。 */
export function PreflightPanel({ data, status, error, onRetry }: Props) {
  if (status === "loading" && !data) {
    return (
      <div className="grid gap-3 sm:grid-cols-3">
        {[0, 1, 2].map(i => (
          <Skeleton key={i} className="h-[92px] rounded-xl" />
        ))}
      </div>
    )
  }
  if (status === "error" && !data) {
    return <ErrorState message={error ?? "读取环境预检失败"} onRetry={onRetry} />
  }
  if (!data) return null

  return (
    <div className="rounded-2xl border bg-card p-4 shadow-xs">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">环境预检</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {data.ok
              ? "本 Server 的配置已满足远程部署要求。"
              : "以下配置需要先处理，否则目标节点无法下载程序或回连本 Server。"}
          </p>
        </div>
        {!data.ok && (
          <Link to="/settings" className={cn(buttonVariants({ variant: "outline", size: "sm" }))}>
            <SettingsIcon data-icon="inline-start" />
            前往设置
          </Link>
        )}
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        {data.checks.map((check, index) => (
          <CheckCard key={check.key} check={check} index={index} />
        ))}
      </div>
    </div>
  )
}

/** 塌缩态：部署进行中时收进页头的一枚 chip。 */
export function PreflightChip({ data, onClick }: { data: SfuDeployPreflight | null; onClick: () => void }) {
  if (!data) return null
  const failed = data.checks.filter(c => c.status === "error").length
  const style = data.ok ? STATUS_STYLE.ok : STATUS_STYLE.error
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "inline-flex h-6 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium",
        "transition-colors hover:bg-muted active:scale-[0.96]",
        data.ok
          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
          : "border-destructive/30 bg-destructive/10 text-destructive"
      )}
    >
      <span className={cn("size-1.5 rounded-full", style.dot)} />
      {data.ok ? "环境就绪" : `${failed} 项待处理`}
    </button>
  )
}

/** 侧滑展开的详情，供 chip 点击后查看。 */
export function PreflightSheet({
  open,
  onOpenChange,
  data,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  data: SfuDeployPreflight | null
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>环境预检</SheetTitle>
          <SheetDescription>本 Server 侧影响远程部署的配置项。</SheetDescription>
        </SheetHeader>
        <div className="grid gap-3 px-4 pb-4">
          {data?.checks.map((check, index) => (
            <CheckCard key={check.key} check={check} index={index} />
          ))}
        </div>
      </SheetContent>
    </Sheet>
  )
}
