import { useEffect, useMemo, useRef, useState } from "react"
import { XIcon } from "lucide-react"

import { DeployLog, scrollToLogLine } from "~/components/sfu-deploy/deploy-log"
import { DeployOutcome } from "~/components/sfu-deploy/deploy-outcome"
import { DeployStepper } from "~/components/sfu-deploy/deploy-stepper"
import {
  currentStepIndex,
  deriveStepStates,
  linesToText,
  STEPS,
  type LogLine,
} from "~/components/sfu-deploy/shared"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { formatDuration, formatFullTime } from "~/lib/format"
import { cn } from "~/lib/utils"
import type { SfuDeployment } from "~/lib/api"

type Props = {
  deployment: SfuDeployment
  lines: LogLine[]
  running: boolean
  onCancel: () => Promise<void> | void
  onRetry?: () => void
  onEditAndRetry?: () => void
  onCloneConfig?: () => void
  compact?: boolean
  className?: string
}

const STATUS_META: Record<string, { label: string; className: string }> = {
  RUNNING: { label: "进行中", className: "border-primary/30 bg-primary/10 text-primary" },
  SUCCEEDED: {
    label: "已完成",
    className: "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  },
  FAILED: { label: "失败", className: "border-destructive/30 bg-destructive/10 text-destructive" },
  CANCELED: { label: "已取消", className: "border-border bg-muted text-muted-foreground" },
}

export function DeployProgress({
  deployment,
  lines,
  running,
  onCancel,
  onRetry,
  onEditAndRetry,
  onCloneConfig,
  compact,
  className,
}: Props) {
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [elapsedSec, setElapsedSec] = useState(0)
  const logWrapRef = useRef<HTMLDivElement>(null)
  /** 进入当前步骤的时刻，用于「已用 mm:ss」计时。 */
  const stepEnteredAtRef = useRef(Date.now())
  const stepRef = useRef(deployment.step)

  const states = useMemo(() => deriveStepStates(deployment), [deployment])
  const index = currentStepIndex(deployment)

  useEffect(() => {
    if (stepRef.current !== deployment.step) {
      stepRef.current = deployment.step
      stepEnteredAtRef.current = Date.now()
      setElapsedSec(0)
    }
  }, [deployment.step])

  useEffect(() => {
    if (!running) return
    const timer = setInterval(
      () => setElapsedSec(Math.floor((Date.now() - stepEnteredAtRef.current) / 1000)),
      1000
    )
    return () => clearInterval(timer)
  }, [running])

  const firstErrorLine = useMemo(() => lines.find(l => l.level === "error")?.n, [lines])
  const status = STATUS_META[deployment.status] ?? STATUS_META.RUNNING
  const params = deployment.params ?? {}
  const displayName = typeof params.display_name === "string" ? params.display_name : ""

  const logMeta = useMemo(() => {
    const stamp = deployment.created_at.replace(/[-:T]/g, "").slice(0, 15)
    const header = [
      "# NewtSpeak SFU 部署日志",
      `# 部署 ID    : ${deployment.id}`,
      `# 目标       : ${deployment.username}@${deployment.host}:${deployment.port}`,
      `# 节点       : ${displayName || "—"}${deployment.node_id ? ` (${deployment.node_id})` : ""}`,
      `# 状态       : ${deployment.status}`,
      `# 步骤       : ${deployment.step}`,
      deployment.error ? `# 错误       : ${deployment.error}` : null,
      `# 发起于     : ${formatFullTime(deployment.created_at)}`,
      `# 耗时       : ${formatDuration(deployment.created_at, deployment.updated_at)}`,
      "# 注意：单次部署日志上限 256 KB，较早内容可能已被截断。",
    ]
      .filter(Boolean)
      .join("\n")
    return { filename: `newt-sfu-deploy-${deployment.host}-${stamp}.log`, header }
  }, [deployment, displayName])

  function downloadLog() {
    const content = `${logMeta.header}\n${"─".repeat(48)}\n${linesToText(lines)}\n`
    const blob = new Blob([content], { type: "text/plain;charset=utf-8" })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = logMeta.filename
    anchor.click()
    URL.revokeObjectURL(url)
  }

  const totalPct = index >= 0 ? ((index + 1) / STEPS.length) * 100 : 0

  return (
    <div className={cn("flex min-w-0 flex-col gap-4", className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="truncate font-medium">{displayName || deployment.host}</h2>
            <span
              className={cn(
                "inline-flex h-5 items-center rounded-3xl border px-2 text-xs font-medium",
                status.className
              )}
            >
              <span key={deployment.status} className="t-text-swap">
                {status.label}
              </span>
            </span>
          </div>
          <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
            {deployment.username}@{deployment.host}:{deployment.port}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <p className="text-xs tabular-nums text-muted-foreground">
            {formatDuration(deployment.created_at, running ? null : deployment.updated_at)}
          </p>
          {running && (
            <Button variant="outline" size="sm" onClick={() => setConfirmCancel(true)}>
              <XIcon data-icon="inline-start" />
              取消部署
            </Button>
          )}
        </div>
      </div>

      <div
        role="progressbar"
        aria-valuenow={Math.max(index + 1, 0)}
        aria-valuemin={0}
        aria-valuemax={STEPS.length}
        aria-valuetext={`第 ${Math.max(index + 1, 1)} 步，共 ${STEPS.length} 步：${STEPS[index]?.label ?? "准备中"}`}
        aria-label="部署总进度"
        className="h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <div
          data-slot="deploy-progress-fill"
          className={cn(
            "h-full rounded-full transition-[width,background-color] duration-500 ease-(--resize-ease)",
            deployment.status === "FAILED"
              ? "bg-destructive"
              : deployment.status === "SUCCEEDED"
                ? "bg-emerald-500"
                : "bg-primary"
          )}
          style={{ width: `${deployment.status === "SUCCEEDED" ? 100 : totalPct}%` }}
        />
      </div>

      <div className={cn("grid min-w-0 gap-5", !compact && "md:grid-cols-[190px_minmax(0,1fr)] xl:grid-cols-[220px_minmax(0,1fr)]")}>
        <DeployStepper
          states={states}
          currentIndex={index}
          elapsedSec={running ? elapsedSec : undefined}
          compact={compact}
        />

        <div ref={logWrapRef} className="flex min-w-0 flex-col gap-4">
          {!running && (
            <DeployOutcome
              deployment={deployment}
              firstErrorLine={firstErrorLine}
              onJumpToError={n => scrollToLogLine(logWrapRef.current, n)}
              onRetry={onRetry}
              onEditAndRetry={onEditAndRetry}
              onCloneConfig={onCloneConfig}
              onDownloadLog={downloadLog}
              compact={compact}
            />
          )}
          <DeployLog
            lines={lines}
            running={running}
            meta={logMeta}
            compact={compact}
            heightClassName={compact ? "h-64" : undefined}
          />
        </div>
      </div>

      <Dialog open={confirmCancel} onOpenChange={setConfirmCancel}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>取消这次部署？</DialogTitle>
            <DialogDescription>
              取消后目标服务器上可能残留半成品配置（已下载的二进制、未启动的 systemd 单元），需要时请登录检查。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmCancel(false)}>
              继续部署
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                setConfirmCancel(false)
                void onCancel()
              }}
            >
              取消部署
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** 部署记录列表项用的状态徽章，与进度头部保持同一色板。 */
export function DeploymentStatusBadge({ status, className }: { status: string; className?: string }) {
  const meta = STATUS_META[status] ?? STATUS_META.RUNNING
  return (
    <Badge variant="outline" className={cn(meta.className, className)}>
      <span key={status} className="t-text-swap">
        {meta.label}
      </span>
    </Badge>
  )
}
