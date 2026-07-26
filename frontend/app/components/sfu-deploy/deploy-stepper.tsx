import { CheckIcon, MinusIcon, XIcon } from "lucide-react"

import { STEPS, type StepState } from "~/components/sfu-deploy/shared"
import { formatElapsed, formatSeconds } from "~/lib/format"
import { cn } from "~/lib/utils"

const SR_STATE: Record<StepState, string> = {
  pending: "待执行",
  active: "进行中",
  done: "已完成",
  failed: "失败",
  skipped: "已跳过",
}

type Props = {
  states: StepState[]
  currentIndex: number
  /** 当前步骤已用秒数（active 时显示计时）。 */
  elapsedSec?: number
  /** 各步骤耗时（秒），done 态显示。 */
  durations?: Record<string, number>
  /** 紧凑模式：只渲染横向压缩条（弹窗内使用）。 */
  compact?: boolean
  className?: string
}

/** 连接线填充状态：上游已完成 → 填满；当前进行中 → 部分填充。 */
function fillOf(states: StepState[], index: number): "none" | "partial" | "done" | "failed" | "skipped" {
  const state = states[index]
  if (state === "done") return "done"
  if (state === "skipped") return "skipped"
  if (state === "failed") return "failed"
  if (state === "active") return "partial"
  return "none"
}

export function DeployStepper({ states, currentIndex, elapsedSec, durations, compact, className }: Props) {
  const currentLabel = STEPS[currentIndex]?.label ?? "准备中"

  const horizontal = (
    <div className={cn(compact ? "" : "md:hidden", className)}>
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <p className="truncate text-sm font-medium">
          <span key={currentLabel} className="t-text-swap">
            {currentLabel}
          </span>
        </p>
        <p className="shrink-0 text-xs tabular-nums text-muted-foreground">
          第{" "}
          <span key={currentIndex} className="t-number-pop">
            {Math.max(currentIndex + 1, 1)}
          </span>{" "}
          / {STEPS.length} 步
        </p>
      </div>
      <ol className="grid auto-cols-fr grid-flow-col items-center gap-1" aria-label="部署步骤">
        {STEPS.map((step, index) => (
          <li
            key={step.key}
            data-state={states[index]}
            className={cn(
              "h-1 rounded-full bg-border transition-colors duration-300 ease-(--resize-ease)",
              "data-[state=done]:bg-emerald-500/70",
              "data-[state=active]:bg-primary",
              "data-[state=failed]:bg-destructive",
              "data-[state=skipped]:bg-border"
            )}
          >
            <span className="sr-only">
              {step.label}：{SR_STATE[states[index]]}
            </span>
          </li>
        ))}
      </ol>
    </div>
  )

  if (compact) return horizontal

  return (
    <>
      {horizontal}
      <ol role="list" aria-label="部署步骤" className={cn("relative hidden flex-col md:flex", className)}>
        {STEPS.map((step, index) => {
          const state = states[index]
          const duration = durations?.[step.key]
          const subline =
            state === "active"
              ? elapsedSec != null
                ? `已用 ${formatElapsed(elapsedSec)}`
                : ""
              : state === "done" && duration != null
                ? formatSeconds(duration)
                : state === "skipped"
                  ? "已跳过"
                  : ""

          return (
            <li
              key={step.key}
              data-state={state}
              aria-current={state === "active" ? "step" : undefined}
              className="group/step relative grid grid-cols-[1.125rem_1fr] items-start gap-x-3 pb-5 last:pb-0"
            >
              {index < STEPS.length - 1 && (
                <span
                  aria-hidden
                  className="absolute top-[1.375rem] bottom-0 left-[0.4375rem] w-0.5 overflow-hidden rounded-full bg-border"
                >
                  <span
                    data-slot="step-connector-fill"
                    data-fill={fillOf(states, index)}
                    className={cn(
                      "block h-full w-full origin-top scale-y-0 rounded-full",
                      "transition-transform duration-600 ease-(--resize-ease)",
                      "data-[fill=partial]:scale-y-[0.4] data-[fill=partial]:bg-gradient-to-b data-[fill=partial]:from-primary/55 data-[fill=partial]:to-transparent",
                      "data-[fill=done]:scale-y-100 data-[fill=done]:bg-emerald-500/70",
                      "data-[fill=failed]:scale-y-100 data-[fill=failed]:bg-destructive/70",
                      "data-[fill=skipped]:scale-y-100 data-[fill=skipped]:bg-border"
                    )}
                  />
                </span>
              )}

              <span
                data-slot="step-dot"
                className={cn(
                  "relative z-10 grid size-[1.125rem] place-items-center rounded-full",
                  "transition-[background-color,box-shadow] duration-200",
                  "group-data-[state=pending]/step:bg-card group-data-[state=pending]/step:ring-1 group-data-[state=pending]/step:ring-border group-data-[state=pending]/step:ring-inset",
                  "group-data-[state=active]/step:step-breathe group-data-[state=active]/step:bg-primary/12 group-data-[state=active]/step:ring-2 group-data-[state=active]/step:ring-primary/45",
                  "group-data-[state=done]/step:bg-emerald-500/12 group-data-[state=done]/step:ring-1 group-data-[state=done]/step:ring-emerald-500/45",
                  "group-data-[state=failed]/step:bg-destructive/12 group-data-[state=failed]/step:ring-1 group-data-[state=failed]/step:ring-destructive/50",
                  "group-data-[state=skipped]/step:bg-muted group-data-[state=skipped]/step:ring-1 group-data-[state=skipped]/step:ring-border"
                )}
              >
                <span aria-hidden className="t-icon-swap size-2.5">
                  <span
                    data-state={state === "pending" ? "visible" : "hidden"}
                    className="size-1.5 rounded-full bg-muted-foreground/40"
                  />
                  <span
                    data-state={state === "active" ? "visible" : "hidden"}
                    className="size-1.5 rounded-full bg-primary"
                  />
                  <CheckIcon
                    data-state={state === "done" ? "visible" : "hidden"}
                    className="size-2.5 text-emerald-700 dark:text-emerald-400"
                  />
                  <XIcon
                    data-state={state === "failed" ? "visible" : "hidden"}
                    className="size-2.5 text-destructive"
                  />
                  <MinusIcon
                    data-state={state === "skipped" ? "visible" : "hidden"}
                    className="size-2.5 text-muted-foreground"
                  />
                </span>
              </span>

              <div className="-mt-px min-w-0">
                <p
                  className={cn(
                    "truncate text-sm leading-snug",
                    "group-data-[state=pending]/step:text-muted-foreground",
                    "group-data-[state=skipped]/step:text-muted-foreground",
                    "group-data-[state=active]/step:font-medium",
                    "group-data-[state=failed]/step:font-medium group-data-[state=failed]/step:text-destructive"
                  )}
                >
                  {step.label}
                </p>
                {subline && (
                  <p className="mt-0.5 hidden text-xs tabular-nums text-muted-foreground lg:block">
                    <span key={subline} className="t-text-swap">
                      {subline}
                    </span>
                  </p>
                )}
                <span className="sr-only">{SR_STATE[state]}</span>
              </div>
            </li>
          )
        })}
      </ol>
    </>
  )
}
