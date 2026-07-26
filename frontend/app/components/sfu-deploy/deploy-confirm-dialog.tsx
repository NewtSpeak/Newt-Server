import { useEffect, useState } from "react"
import { InfoIcon, ShieldAlertIcon, TriangleAlertIcon } from "lucide-react"

import { hasBlockingRisk, type Risk } from "~/components/sfu-deploy/shared"
import { Button } from "~/components/ui/button"
import { Checkbox } from "~/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { Field, FieldLabel } from "~/components/ui/field"
import { cn } from "~/lib/utils"

const TONE_ICON = {
  danger: ShieldAlertIcon,
  warn: TriangleAlertIcon,
  info: InfoIcon,
} as const

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  host: string
  risks: Risk[]
  submitting: boolean
  onConfirm: () => void
}

/**
 * 风险确认。只在存在风险项时由向导弹出——每次都拦一下，用户几次之后就会
 * 无脑点确定，摩擦反而失效。
 */
export function DeployConfirmDialog({ open, onOpenChange, host, risks, submitting, onConfirm }: Props) {
  const needsAck = hasBlockingRisk(risks)
  const [ack, setAck] = useState(false)

  // 打开时把焦点送进弹窗：落在确认勾选框或「返回修改」上，绝不落在确认按钮
  //（防回车误触发起部署）。base-ui 的 initialFocus 在指针触发时不接管，故显式处理。
  useEffect(() => {
    if (!open) return
    setAck(false)
    const timer = window.setTimeout(() => {
      const target = document.querySelector<HTMLElement>(
        needsAck ? "[data-deploy-ack]" : "[data-deploy-dismiss]"
      )
      target?.focus({ preventScroll: true })
    }, 0)
    return () => window.clearTimeout(timer)
  }, [open, needsAck])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* 初始焦点落在确认勾选框或「返回修改」，绝不落在确认按钮——防回车误触发起部署。
          用 DOM 查询而非 ref：base-ui 把 id/ref 放在隐藏 input 上，可聚焦的是外层按钮。 */}
      <DialogContent
        className="max-w-lg"
        initialFocus={() =>
          document.querySelector<HTMLElement>(needsAck ? "[data-deploy-ack]" : "[data-deploy-dismiss]")
        }
      >
        <DialogHeader>
          <DialogTitle>确认部署到 {host}</DialogTitle>
          <DialogDescription>此次部署将对目标服务器产生以下影响。</DialogDescription>
        </DialogHeader>

        <ul className="grid gap-2">
          {risks.map((risk, index) => {
            const Icon = TONE_ICON[risk.tone]
            return (
              <li
                key={risk.key}
                data-tone={risk.tone}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className={cn(
                  "anim-item flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-sm",
                  "data-[tone=danger]:border-destructive/35 data-[tone=danger]:bg-destructive/[0.06] data-[tone=danger]:text-destructive",
                  "data-[tone=warn]:border-amber-500/35 data-[tone=warn]:bg-amber-500/[0.06] data-[tone=warn]:text-amber-700 dark:data-[tone=warn]:text-amber-400",
                  "data-[tone=info]:border-border data-[tone=info]:bg-muted/40 data-[tone=info]:text-muted-foreground"
                )}
              >
                <Icon className="mt-0.5 size-4 shrink-0" />
                <span className="min-w-0">{risk.text}</span>
              </li>
            )
          })}
        </ul>

        {needsAck && (
          <Field orientation="horizontal">
            <Checkbox
              data-deploy-ack=""
              id="deploy-risk-ack"
              checked={ack}
              onCheckedChange={next => setAck(Boolean(next))}
            />
            <FieldLabel htmlFor="deploy-risk-ack" className="text-sm font-normal">
              我已了解上述影响
            </FieldLabel>
          </Field>
        )}

        <DialogFooter>
          <Button data-deploy-dismiss="" variant="outline" onClick={() => onOpenChange(false)}>
            返回修改
          </Button>
          <Button onClick={onConfirm} disabled={submitting || (needsAck && !ack)}>
            {submitting ? "正在发起…" : "确认并开始部署"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
