import { useRef } from "react"
import { Link } from "react-router"
import {
  ArrowDownIcon,
  CheckIcon,
  CircleSlashIcon,
  CopyPlusIcon,
  DownloadIcon,
  InfoIcon,
  NetworkIcon,
  RotateCcwIcon,
  ServerIcon,
  TriangleAlertIcon,
} from "lucide-react"

import { CopyButton } from "~/components/copy-button"
import { deriveAdvertiseURL, diagnose, STEPS } from "~/components/sfu-deploy/shared"
import { Button, buttonVariants } from "~/components/ui/button"
import { formatDuration } from "~/lib/format"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"
import { cn } from "~/lib/utils"
import type { SfuDeployment } from "~/lib/api"

type Props = {
  deployment: SfuDeployment
  /** 第一条 error 日志的行号；有则显示「跳到第一条错误」。 */
  firstErrorLine?: number
  onJumpToError?: (lineNumber: number) => void
  onRetry?: () => void
  onEditAndRetry?: () => void
  onCloneConfig?: () => void
  onDownloadLog?: () => void
  compact?: boolean
}

export function DeployOutcome({
  deployment,
  firstErrorLine,
  onJumpToError,
  onRetry,
  onEditAndRetry,
  onCloneConfig,
  onDownloadLog,
  compact,
}: Props) {
  const rootRef = useRef<HTMLDivElement>(null)
  const iconRef = useRef<HTMLSpanElement>(null)

  useGSAP(
    () => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        const timeline = gsap.timeline()
        timeline.fromTo(
          rootRef.current,
          { autoAlpha: 0, y: 16 },
          { autoAlpha: 1, y: 0, duration: MOTION.enter, ease: MOTION.ease, clearProps: "all" }
        )
        if (iconRef.current) {
          timeline.fromTo(
            iconRef.current,
            { scale: 0.7 },
            { scale: 1, duration: 0.4, ease: "back.out(2)", clearProps: "all" },
            "-=0.16"
          )
        }
      })
    },
    { dependencies: [deployment.status], scope: rootRef }
  )

  const params = deployment.params ?? {}
  const displayName = typeof params.display_name === "string" ? params.display_name : deployment.host
  const elapsed = formatDuration(deployment.created_at, deployment.updated_at)

  if (deployment.status === "SUCCEEDED") {
    const advertiseURL = deriveAdvertiseURL({
      tls_mode: params.tls_mode as string,
      domain: params.domain as string,
      public_ip: params.public_ip as string,
      host: deployment.host,
    })
    const scheduled = params.enable_scheduling !== false

    return (
      <div
        ref={rootRef}
        className="rounded-2xl border border-emerald-500/30 bg-emerald-500/[0.06] p-4"
        role="status"
      >
        <div className="flex items-start gap-3">
          <span
            ref={iconRef}
            className="grid size-9 shrink-0 place-items-center rounded-2xl bg-emerald-500/15 text-emerald-700 dark:text-emerald-400"
          >
            <CheckIcon className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <p className="font-medium">节点「{displayName}」已上线</p>
            <p className="mt-0.5 text-sm text-muted-foreground">
              共用时 <span className="tabular-nums">{elapsed}</span>
              {deployment.node_id && (
                <>
                  {" · 节点 ID "}
                  <span className="font-mono text-xs">{deployment.node_id}</span>
                </>
              )}
            </p>

            {advertiseURL && (
              <div className="mt-3 flex items-center gap-2 rounded-xl border bg-card px-3 py-2">
                <code className="min-w-0 flex-1 truncate font-mono text-xs">{advertiseURL}</code>
                <CopyButton text={advertiseURL} label="复制接入地址" />
              </div>
            )}

            {!compact && (
              <ul className="mt-3 grid gap-1.5 text-sm">
                <li className="anim-item flex items-start gap-2" style={{ "--stagger-index": 0 } as React.CSSProperties}>
                  {scheduled ? (
                    <>
                      <CheckIcon className="mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400" />
                      已启用调度，节点开始承载用户
                    </>
                  ) : (
                    <>
                      <InfoIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                      <span>
                        尚未启用调度，
                        <Link to="/voice/nodes" className="underline underline-offset-4 hover:text-primary">
                          前往启用
                        </Link>
                      </span>
                    </>
                  )}
                </li>
                <li className="anim-item flex items-start gap-2" style={{ "--stagger-index": 1 } as React.CSSProperties}>
                  <NetworkIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span>
                    如需限定该节点服务的服务器，
                    <Link to="/voice/pools" className="underline underline-offset-4 hover:text-primary">
                      配置节点池
                    </Link>
                  </span>
                </li>
                <li className="anim-item flex items-start gap-2" style={{ "--stagger-index": 2 } as React.CSSProperties}>
                  <ServerIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span>
                    在{" "}
                    <Link to="/voice/nodes" className="underline underline-offset-4 hover:text-primary">
                      SFU 节点
                    </Link>{" "}
                    查看容量、CPU 与心跳
                  </span>
                </li>
              </ul>
            )}
          </div>
        </div>

        {!compact && (
          <div className="mt-4 flex flex-wrap gap-2">
            {onCloneConfig && (
              <Button variant="outline" size="sm" onClick={onCloneConfig}>
                <CopyPlusIcon data-icon="inline-start" />
                再部署一台（沿用配置）
              </Button>
            )}
            <Link to="/voice/nodes" className={cn(buttonVariants({ variant: "outline", size: "sm" }))}>
              查看节点
            </Link>
          </div>
        )}
      </div>
    )
  }

  if (deployment.status === "CANCELED") {
    return (
      <div ref={rootRef} className="rounded-2xl border bg-muted/40 p-4" role="status">
        <div className="flex items-start gap-3">
          <span className="grid size-9 shrink-0 place-items-center rounded-2xl bg-muted text-muted-foreground">
            <CircleSlashIcon className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <p className="font-medium">部署已取消</p>
            <p className="mt-1 text-sm text-muted-foreground">
              目标服务器上可能残留半成品配置，建议登录后检查 <code className="rounded bg-muted px-1">/opt/newtspeak</code>{" "}
              与 owl-sfu 服务状态。
            </p>
          </div>
        </div>
        {onRetry && !compact && (
          <div className="mt-4">
            <Button variant="outline" size="sm" onClick={onRetry}>
              <RotateCcwIcon data-icon="inline-start" />
              沿用配置重试
            </Button>
          </div>
        )}
      </div>
    )
  }

  // FAILED
  const failedStep = STEPS.find(s => s.key === deployment.step)
  const diagnosis = diagnose(deployment)

  return (
    <div ref={rootRef} className="rounded-2xl border border-destructive/35 bg-destructive/[0.06] p-4" role="alert">
      <div className="flex items-start gap-3">
        <span
          ref={iconRef}
          className="grid size-9 shrink-0 place-items-center rounded-2xl bg-destructive/15 text-destructive"
        >
          <TriangleAlertIcon className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="font-medium text-destructive">
            部署失败{failedStep ? `于「${failedStep.label}」` : ""}
          </p>
          {deployment.error && <p className="mt-1 text-sm break-words">{deployment.error}</p>}

          {diagnosis && (
            <div className="mt-3 rounded-xl border bg-card p-3 text-sm">
              <p className="font-medium">可能原因</p>
              <p className="mt-1 text-muted-foreground">{diagnosis.cause}</p>
              <p className="mt-2 font-medium">建议操作</p>
              <ul className="mt-1 grid gap-1 text-muted-foreground">
                {diagnosis.actions.map((action, index) => (
                  <li
                    key={action}
                    className="anim-item flex items-start gap-2"
                    style={{ "--stagger-index": index } as React.CSSProperties}
                  >
                    <span aria-hidden className="mt-1.5 size-1 shrink-0 rounded-full bg-muted-foreground/50" />
                    {action}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </div>

      <div className="mt-4 flex flex-wrap gap-2">
        {onRetry && (
          <Button size="sm" onClick={onRetry}>
            <RotateCcwIcon data-icon="inline-start" />
            沿用配置重试
          </Button>
        )}
        {onEditAndRetry && (
          <Button variant="outline" size="sm" onClick={onEditAndRetry}>
            修改配置后重试
          </Button>
        )}
        {firstErrorLine != null && onJumpToError && (
          <Button variant="outline" size="sm" onClick={() => onJumpToError(firstErrorLine)}>
            <ArrowDownIcon data-icon="inline-start" />
            跳到第一条错误
          </Button>
        )}
        {onDownloadLog && (
          <Button variant="ghost" size="sm" onClick={onDownloadLog}>
            <DownloadIcon data-icon="inline-start" />
            下载日志
          </Button>
        )}
      </div>
    </div>
  )
}
