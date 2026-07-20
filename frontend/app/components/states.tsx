import type { LucideIcon } from "lucide-react"
import { InboxIcon, RotateCcwIcon, TriangleAlertIcon } from "lucide-react"

import { Button } from "~/components/ui/button"
import { Skeleton } from "~/components/ui/skeleton"
import { cn } from "~/lib/utils"

export function LoadingState({ rows = 4, className }: { rows?: number; className?: string }) {
  return (
    <div className={cn("flex flex-col gap-3", className)} aria-busy="true" aria-label="加载中">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="flex items-center gap-3">
          <Skeleton className="size-9 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-1/3" />
            <Skeleton className="h-3 w-2/3" />
          </div>
        </div>
      ))}
    </div>
  )
}

export function EmptyState({
  icon: Icon = InboxIcon,
  title,
  description,
  action,
  className,
}: {
  icon?: LucideIcon
  title: string
  description?: string
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("anim-item flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed px-6 py-12 text-center", className)}>
      <div className="grid size-11 place-items-center rounded-2xl bg-muted text-muted-foreground">
        <Icon className="size-5" />
      </div>
      <p className="mt-1 text-sm font-medium">{title}</p>
      {description && <p className="max-w-sm text-xs text-muted-foreground">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

export function ErrorState({ message, onRetry, className }: { message: string; onRetry?: () => void; className?: string }) {
  return (
    <div
      role="alert"
      className={cn(
        "anim-item flex flex-col items-center justify-center gap-2 rounded-xl border border-destructive/25 bg-destructive/5 px-6 py-10 text-center",
        className
      )}
    >
      <div className="grid size-11 place-items-center rounded-2xl bg-destructive/10 text-destructive">
        <TriangleAlertIcon className="size-5" />
      </div>
      <p className="mt-1 text-sm font-medium text-destructive">加载失败</p>
      <p className="max-w-md text-xs break-all text-muted-foreground">{message}</p>
      {onRetry && (
        <Button variant="outline" size="sm" className="mt-2" onClick={onRetry}>
          <RotateCcwIcon data-icon="inline-start" />
          重试
        </Button>
      )}
    </div>
  )
}
