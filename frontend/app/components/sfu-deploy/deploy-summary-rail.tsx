import { TriangleAlertIcon } from "lucide-react"

import { CopyButton } from "~/components/copy-button"
import { deriveAdvertiseURL, TLS_OPTIONS, type DeployFormValues, type Risk } from "~/components/sfu-deploy/shared"
import { Button } from "~/components/ui/button"
import { cn } from "~/lib/utils"

type Props = {
  values: DeployFormValues
  host: string
  username: string
  port: string
  risks: Risk[]
  submitting: boolean
  onSubmit: () => void
  /** page = 右侧 sticky 侧栏；dialog = 底部紧凑操作条。 */
  variant?: "page" | "dialog"
  className?: string
}

function Row({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className="flex items-baseline gap-3 text-sm">
      <span className="w-10 shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("min-w-0 flex-1 truncate", muted ? "text-muted-foreground/60" : "font-mono text-xs")}>
        {value}
      </span>
    </div>
  )
}

export function DeploySummaryRail({
  values,
  host,
  username,
  port,
  risks,
  submitting,
  onSubmit,
  variant = "page",
  className,
}: Props) {
  const advertiseURL = deriveAdvertiseURL({
    tls_mode: values.tlsMode,
    domain: values.domain,
    public_ip: values.publicIP,
    host,
  })
  const tlsLabel = TLS_OPTIONS.find(o => o.value === values.tlsMode)?.title ?? "—"
  const target = host ? `${username || "root"}@${host}:${port || "22"}` : ""
  const attention = risks.filter(r => r.tone !== "info")

  if (variant === "dialog") {
    return (
      <div className={cn("flex flex-wrap items-center gap-3", className)}>
        {advertiseURL && (
          <p className="mr-auto min-w-0 truncate font-mono text-xs text-muted-foreground">
            客户端将连接 {advertiseURL}
          </p>
        )}
        <Button onClick={onSubmit} disabled={submitting}>
          {submitting ? "正在发起…" : "开始部署"}
        </Button>
      </div>
    )
  }

  return (
    <aside className={cn("rounded-2xl border bg-card p-4 shadow-xs lg:sticky lg:top-4", className)}>
      <h3 className="text-sm font-medium">本次部署</h3>

      <div className="mt-3 grid gap-1.5">
        <Row label="目标" value={target || "尚未填写"} muted={!target} />
        <Row label="节点" value={values.displayName || "尚未命名"} muted={!values.displayName} />
        <Row label="方案" value={tlsLabel} muted={false} />
      </div>

      <div className="mt-4 border-t pt-3">
        <p className="text-xs text-muted-foreground">客户端将连接</p>
        {advertiseURL ? (
          <div className="mt-1.5 flex items-center gap-2 rounded-lg bg-muted/60 px-2 py-1.5">
            <code className="min-w-0 flex-1 truncate font-mono text-xs">{advertiseURL}</code>
            <CopyButton text={advertiseURL} label="复制接入地址" />
          </div>
        ) : (
          <p className="mt-1.5 text-xs text-muted-foreground/60">
            {values.tlsMode === "none" ? "填写公网 IP 后显示" : "填写域名后显示"}
          </p>
        )}
      </div>

      {attention.length > 0 && (
        <ul className="mt-4 grid gap-1.5 border-t pt-3">
          {attention.map(risk => (
            <li
              key={risk.key}
              className={cn(
                "flex items-start gap-1.5 text-xs",
                risk.tone === "danger" ? "text-destructive" : "text-amber-700 dark:text-amber-400"
              )}
            >
              <TriangleAlertIcon className="mt-0.5 size-3 shrink-0" />
              <span className="min-w-0">{risk.text}</span>
            </li>
          ))}
        </ul>
      )}

      <Button onClick={onSubmit} disabled={submitting} className="mt-4 w-full">
        {submitting ? "正在发起…" : "开始部署"}
      </Button>
      <p className="mt-2 text-center text-xs text-muted-foreground">
        或按 <kbd className="rounded border bg-muted px-1 font-mono text-[10px]">Ctrl</kbd> +{" "}
        <kbd className="rounded border bg-muted px-1 font-mono text-[10px]">Enter</kbd>
      </p>
    </aside>
  )
}
