import { HistoryIcon } from "lucide-react"

import { DeploymentStatusBadge } from "~/components/sfu-deploy/deploy-progress"
import { STEPS } from "~/components/sfu-deploy/shared"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
import { formatDuration, formatRelative } from "~/lib/format"
import { cn } from "~/lib/utils"
import type { SfuDeployment } from "~/lib/api"

type Props = {
  items: SfuDeployment[]
  status: "idle" | "loading" | "success" | "error"
  error?: string
  selectedID?: string | null
  onSelect: (deployment: SfuDeployment) => void
  onRetryLoad: () => void
  onLoadMore?: () => void
  hasMore?: boolean
}

export function DeployHistory({
  items,
  status,
  error,
  selectedID,
  onSelect,
  onRetryLoad,
  onLoadMore,
  hasMore,
}: Props) {
  if (status === "loading" && items.length === 0) return <LoadingState rows={3} />
  if (status === "error" && items.length === 0) {
    return <ErrorState message={error ?? "读取部署记录失败"} onRetry={onRetryLoad} />
  }
  if (items.length === 0) {
    return (
      <EmptyState
        icon={HistoryIcon}
        title="还没有部署记录"
        description="发起一次自动部署后，这里会保留完整的过程日志，失败时可回看排障。"
      />
    )
  }

  return (
    <div className="grid gap-2">
      {items.map((item, index) => {
        const name = typeof item.params?.display_name === "string" ? item.params.display_name : ""
        const step = STEPS.find(s => s.key === item.step)
        const selected = item.id === selectedID
        return (
          <button
            key={item.id}
            type="button"
            onClick={() => onSelect(item)}
            aria-current={selected ? "true" : undefined}
            style={{ "--stagger-index": index } as React.CSSProperties}
            className={cn(
              "anim-item grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-xl border px-4 py-3 text-left",
              "transition-[background-color,border-color] duration-200",
              "focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none active:scale-[0.99]",
              selected ? "border-primary/40 bg-primary/[0.06]" : "hover:bg-muted/50"
            )}
          >
            <DeploymentStatusBadge status={item.status} />

            <div className="min-w-0">
              <p className="truncate text-sm font-medium">{name || item.host}</p>
              <p className="truncate font-mono text-xs text-muted-foreground">
                {item.username}@{item.host}:{item.port}
                {item.status === "FAILED" && step && (
                  <span className="ml-1.5 font-sans text-destructive">失败于{step.label}</span>
                )}
              </p>
            </div>

            <div className="shrink-0 text-right">
              <p className="text-xs tabular-nums text-muted-foreground">
                {formatDuration(item.created_at, item.updated_at)}
              </p>
              <p className="text-xs text-muted-foreground/70">{formatRelative(item.created_at)}</p>
            </div>
          </button>
        )
      })}

      {hasMore && onLoadMore && (
        <Button variant="ghost" size="sm" onClick={onLoadMore} className="mt-1 justify-self-center">
          加载更多记录
        </Button>
      )}
    </div>
  )
}
