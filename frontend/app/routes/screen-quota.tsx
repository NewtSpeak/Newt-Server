import { useEffect, useState } from "react"
import { useOutletContext } from "react-router"
import { InfoIcon, MonitorUpIcon, SaveIcon } from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { Switch } from "~/components/ui/switch"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { useGuildID } from "~/hooks/use-guild-id"
import { getScreenQuota, patchScreenQuota, type ScreenQuota } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { cn } from "~/lib/utils"

export default function ScreenQuotaPage() {
  const { user, guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)

  const quota = useAsyncData<ScreenQuota>(guildID ? () => getScreenQuota(guildID) : null, [guildID])
  useGatewayEvent(["SCREEN_QUOTA_UPDATE", "SCREEN_SHARE_START", "SCREEN_SHARE_STOP"], () => quota.reload(true))

  const [baseLimit, setBaseLimit] = useState(3)
  const [dynamicEnabled, setDynamicEnabled] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (quota.data) {
      setBaseLimit(quota.data.base_limit)
      setDynamicEnabled(quota.data.dynamic_enabled)
    }
  }, [quota.data])

  async function onSave() {
    if (!guildID) return
    setSaving(true)
    try {
      await patchScreenQuota(guildID, { base_limit: baseLimit, dynamic_enabled: dynamicEnabled })
      toast.success("屏幕共享配额已更新")
      quota.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "配额更新失败")
    } finally {
      setSaving(false)
    }
  }

  const data = quota.data
  const base = data?.base_limit ?? 0
  const effective = data?.effective_limit ?? base
  const active = data?.active ?? 0
  const scaleMax = Math.max(base, 1)
  const activePct = Math.min(100, (active / scaleMax) * 100)
  const effectivePct = Math.min(100, (effective / scaleMax) * 100)
  const throttled = data ? effective < base : false

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="屏幕共享配额"
        description="每服基准上限（新服默认 3 路）+ 负载动态有效上限（≤ 基准）；满额新开将被拒绝。"
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <SimpleSelect
          ariaLabel="选择服务器"
          placeholder="选择服务器"
          value={guildID}
          onChange={setGuildID}
          options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
          className="w-52"
        />

        {quota.status === "loading" && <LoadingState rows={3} />}
        {quota.status === "error" && <ErrorState message={quota.error} onRetry={() => quota.reload()} />}
        {quota.status === "idle" && guilds.length === 0 && (
          <EmptyState icon={MonitorUpIcon} title="暂无服务器" description="先创建服务器，再配置屏幕共享配额。" />
        )}

        {quota.status === "success" && data && (
          <div className="grid gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">当前占用</CardTitle>
                <CardDescription>基准 / 有效 / 占用三层可视化；动态降额时有效上限低于基准。</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-5">
                <div className="flex items-end gap-6">
                  {[
                    { label: "当前占用", value: active, tone: "text-foreground" },
                    { label: "有效上限", value: effective, tone: throttled ? "text-amber-600 dark:text-amber-400" : "text-foreground" },
                    { label: "基准上限", value: base, tone: "text-muted-foreground" },
                  ].map(item => (
                    <div key={item.label}>
                      <p className="text-xs text-muted-foreground">{item.label}</p>
                      {/* 数字变化 pop-in */}
                      <p key={item.value} className={cn("t-number-pop text-3xl font-semibold tabular-nums", item.tone)}>
                        {item.value}
                      </p>
                    </div>
                  ))}
                </div>

                <div className="grid gap-1.5">
                  <div
                    className="relative h-3 overflow-hidden rounded-full bg-muted"
                    role="progressbar"
                    aria-label="屏幕共享占用"
                    aria-valuenow={active}
                    aria-valuemin={0}
                    aria-valuemax={base}
                  >
                    {/* 有效上限区间 */}
                    <div
                      className="absolute inset-y-0 left-0 rounded-full bg-primary/15 transition-[width] duration-500 ease-(--resize-ease)"
                      style={{ width: `${effectivePct}%` }}
                    />
                    {/* 实际占用 */}
                    <div
                      className={cn(
                        "absolute inset-y-0 left-0 rounded-full transition-[width] duration-500 ease-(--resize-ease)",
                        active >= effective ? "bg-destructive" : "bg-primary"
                      )}
                      style={{ width: `${activePct}%` }}
                    />
                    {/* 有效上限刻度线 */}
                    {throttled && (
                      <div className="absolute inset-y-0 w-0.5 bg-amber-500" style={{ left: `${effectivePct}%` }} />
                    )}
                  </div>
                  <div className="flex justify-between text-[10px] text-muted-foreground tabular-nums">
                    <span>0</span>
                    <span>{base} 路</span>
                  </div>
                </div>

                {throttled && (
                  <p className="t-text-swap flex items-start gap-1.5 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
                    <InfoIcon className="mt-0.5 size-3.5 shrink-0" />
                    节点池负载偏高，动态降额已生效：有效上限 {effective} 路（基准 {base} 路）。负载恢复后将缓慢回升，不会超过基准。
                  </p>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">配额调整</CardTitle>
                <CardDescription>
                  {user.system_admin ? "系统管理员可调整各服基准与动态降额开关。" : "仅系统管理员可修改；当前为只读展示。"}
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-5">
                <div className="grid gap-2">
                  <Label htmlFor="base-limit">基准上限（并发屏幕路数）</Label>
                  <Input
                    id="base-limit"
                    type="number"
                    min={0}
                    max={50}
                    value={baseLimit}
                    onChange={event => setBaseLimit(Math.max(0, Number(event.target.value)))}
                    disabled={!user.system_admin}
                    className="w-32 tabular-nums"
                  />
                  <p className="text-xs text-muted-foreground">
                    每频道另有默认 2 路上限（可在「服务器详情 → 频道 → 舞台配置」按频道调整），实际允许 = min(频道剩余, 服有效剩余)。
                  </p>
                </div>
                <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
                  <div>
                    <p className="text-sm font-medium">动态降额</p>
                    <p className="text-xs text-muted-foreground">按节点池出口带宽 / CPU / 屏幕轨数聚合自动下调有效上限</p>
                  </div>
                  <Switch checked={dynamicEnabled} onCheckedChange={setDynamicEnabled} disabled={!user.system_admin} aria-label="动态降额" />
                </div>
                {user.system_admin && (
                  <Button onClick={onSave} disabled={saving}>
                    <SaveIcon data-icon="inline-start" />
                    {saving ? "保存中…" : "保存配额"}
                  </Button>
                )}
              </CardContent>
            </Card>
          </div>
        )}
      </section>
    </main>
  )
}
