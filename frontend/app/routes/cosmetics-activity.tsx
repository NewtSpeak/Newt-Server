import { useEffect, useMemo, useRef, useState } from "react"
import {
  CalculatorIcon,
  CheckCircle2Icon,
  CoinsIcon,
  FlameIcon,
  GiftIcon,
  Loader2Icon,
  SaveIcon,
  SearchIcon,
  UsersIcon,
  UserSearchIcon,
} from "lucide-react"
import { Bar, BarChart, Cell, XAxis } from "recharts"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "~/components/ui/card"
import { ChartContainer, ChartTooltip, type ChartConfig } from "~/components/ui/chart"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { Skeleton } from "~/components/ui/skeleton"
import { Switch } from "~/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "~/components/ui/table"
import { Textarea } from "~/components/ui/textarea"
import { FramedAvatar } from "~/components/user-avatar-frame"
import { useAsyncData } from "~/hooks/use-async-data"
import { useAvatarFrames } from "~/hooks/use-avatar-frames"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"
import {
  getActivityConfig,
  getActivityStats,
  getActivityUserDetail,
  listPlatformUsers,
  putActivityConfig,
  triggerActivitySettle,
  type ActivityConfigWrite,
  type PlatformUser,
} from "~/lib/api"
import { cn } from "~/lib/utils"

/** 活跃度运营：统计概况、计分/发放配置、手动结算与单用户明细查询 */
export default function CosmeticsActivityPage() {
  const containerRef = useRef<HTMLDivElement>(null)

  // 入场编排（与 dashboard 同节奏）：统计卡先行 stagger，功能卡错开半拍跟进。
  useGSAP(
    () => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.from(".metric-card", {
          autoAlpha: 0,
          y: 16,
          duration: MOTION.enter,
          ease: MOTION.ease,
          stagger: MOTION.stagger,
          clearProps: "all",
        })
        gsap.from("[data-panel-card]", {
          autoAlpha: 0,
          y: 16,
          duration: MOTION.enter,
          ease: MOTION.ease,
          stagger: 0.08,
          delay: 0.12,
          clearProps: "all",
        })
      })
    },
    { scope: containerRef },
  )

  return (
    <main ref={containerRef} className="@container/main flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="活跃度"
        description="活跃度计分与积分发放：维度权重、日上限、等级门槛配置，以及结算工具与单用户明细。"
      />
      <StatsSection />
      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div data-panel-card>
          <ConfigCard />
        </div>
        <div data-panel-card>
          <SettleCard />
        </div>
        <div data-panel-card>
          <UserDetailCard />
        </div>
      </section>
    </main>
  )
}

// ---------------------------------------------------------------------------
// 统计卡行
// ---------------------------------------------------------------------------

function StatsSection() {
  const [day, setDay] = useState("")
  const stats = useAsyncData(() => getActivityStats(day || undefined), [day])

  const loading = stats.status === "loading" && !stats.data
  const metrics = [
    { label: "今日活跃人数", value: stats.data ? String(stats.data.active_users) : "—", note: "当日有任一维度行为的用户数", icon: UsersIcon },
    { label: "今日总分", value: stats.data ? String(stats.data.total_score) : "—", note: "全平台当日活跃分合计", icon: FlameIcon },
    { label: "已发放人数", value: stats.data ? String(stats.data.granted_users) : "—", note: "当日已结算发放的用户数", icon: GiftIcon },
    { label: "已发放积分", value: stats.data ? String(stats.data.granted_points) : "—", note: "当日结算发放的积分合计", icon: CoinsIcon },
  ]

  return (
    <section className="flex flex-col gap-3 px-4 lg:px-6">
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-sm text-muted-foreground">
          统计业务日：<span className="font-mono tabular-nums">{stats.data?.day ?? (day || "今日")}</span>
        </p>
        <Input
          type="date"
          aria-label="切换统计日期"
          value={day}
          onChange={event => setDay(event.target.value)}
          className="ml-auto h-8 w-40"
        />
      </div>
      {stats.status === "error" && <ErrorState message={stats.error} onRetry={() => stats.reload()} />}
      <div className="grid grid-cols-1 gap-4 *:data-[slot=card]:bg-linear-to-t *:data-[slot=card]:from-primary/5 *:data-[slot=card]:to-card *:data-[slot=card]:shadow-xs @xl/main:grid-cols-2 @5xl/main:grid-cols-4">
        {metrics.map(metric => (
          <Card key={metric.label} className="metric-card @container/card">
            <CardHeader>
              <CardDescription>{metric.label}</CardDescription>
              <CardTitle className="text-2xl font-semibold tabular-nums @[250px]/card:text-3xl">
                {loading ? (
                  <Skeleton className="h-8 w-20" />
                ) : (
                  <span key={metric.value} className="t-number-pop">
                    {metric.value}
                  </span>
                )}
              </CardTitle>
              <CardAction>
                <Badge variant="outline">
                  <metric.icon />
                </Badge>
              </CardAction>
            </CardHeader>
            <CardFooter className="text-sm text-muted-foreground">{metric.note}</CardFooter>
          </Card>
        ))}
      </div>
    </section>
  )
}

// ---------------------------------------------------------------------------
// 配置卡
// ---------------------------------------------------------------------------

type NumericKey =
  | "day_offset_minutes"
  | "weight_message"
  | "cap_message"
  | "weight_voice_minute"
  | "cap_voice_minutes"
  | "weight_reaction"
  | "cap_reactions"
  | "weight_login"
  | "points_rate"
  | "bonus_per_level_pct"
  | "max_bonus_pct"

type ConfigForm = { enabled: boolean; thresholds: string } & Record<NumericKey, string>

/** 计分维度：权重字段 + 日上限字段（登录维度无上限字段，固定 1） */
const DIMENSIONS: { label: string; weight: NumericKey; cap?: NumericKey }[] = [
  { label: "消息", weight: "weight_message", cap: "cap_message" },
  { label: "语音（分钟）", weight: "weight_voice_minute", cap: "cap_voice_minutes" },
  { label: "表情回应", weight: "weight_reaction", cap: "cap_reactions" },
  { label: "登录", weight: "weight_login" },
]

function parseThresholds(text: string): { values?: number[]; error?: string } {
  let raw: unknown
  try {
    raw = JSON.parse(text)
  } catch {
    return { error: "不是合法的 JSON" }
  }
  if (!Array.isArray(raw) || raw.some(item => typeof item !== "number" || !Number.isFinite(item)))
    return { error: "必须是数字数组，如 [10, 30, 60]" }
  const values = raw as number[]
  if (values.some(value => value < 0)) return { error: "门槛不能为负数" }
  for (let i = 1; i < values.length; i++) {
    if (values[i] <= values[i - 1]) return { error: "门槛必须严格递增" }
  }
  return { values }
}

function ConfigCard() {
  const config = useAsyncData(() => getActivityConfig(), [])
  const [form, setForm] = useState<ConfigForm | null>(null)
  const [thresholdError, setThresholdError] = useState("")
  const [saving, setSaving] = useState(false)

  // 配置到达后回填表单（保存成功后 reload 也会刷新 updated_at，但不覆盖正在编辑的表单）
  useEffect(() => {
    if (config.status !== "success" || !config.data || form) return
    const data = config.data
    setForm({
      enabled: data.enabled,
      day_offset_minutes: String(data.day_offset_minutes),
      weight_message: String(data.weight_message),
      cap_message: String(data.cap_message),
      weight_voice_minute: String(data.weight_voice_minute),
      cap_voice_minutes: String(data.cap_voice_minutes),
      weight_reaction: String(data.weight_reaction),
      cap_reactions: String(data.cap_reactions),
      weight_login: String(data.weight_login),
      points_rate: String(data.points_rate),
      bonus_per_level_pct: String(data.bonus_per_level_pct),
      max_bonus_pct: String(data.max_bonus_pct),
      thresholds: JSON.stringify(data.level_thresholds),
    })
  }, [config.status, config.data, form])

  const numericInvalid = useMemo(() => {
    if (!form) return true
    return (Object.keys(form) as (keyof ConfigForm)[]).some(key => {
      if (key === "enabled" || key === "thresholds") return false
      const value = Number(form[key])
      return form[key].trim() === "" || !Number.isFinite(value) || value < 0
    })
  }, [form])

  function setField(key: NumericKey, value: string) {
    setForm(current => (current ? { ...current, [key]: value } : current))
  }

  async function onSave() {
    if (!form) return
    const parsed = parseThresholds(form.thresholds)
    if (parsed.error || !parsed.values) {
      setThresholdError(parsed.error ?? "门槛数组非法")
      return
    }
    const body: ActivityConfigWrite = {
      enabled: form.enabled,
      day_offset_minutes: Number(form.day_offset_minutes),
      weight_message: Number(form.weight_message),
      cap_message: Number(form.cap_message),
      weight_voice_minute: Number(form.weight_voice_minute),
      cap_voice_minutes: Number(form.cap_voice_minutes),
      weight_reaction: Number(form.weight_reaction),
      cap_reactions: Number(form.cap_reactions),
      weight_login: Number(form.weight_login),
      points_rate: Number(form.points_rate),
      bonus_per_level_pct: Number(form.bonus_per_level_pct),
      max_bonus_pct: Number(form.max_bonus_pct),
      level_thresholds: parsed.values,
    }
    setSaving(true)
    try {
      await putActivityConfig(body)
      toast.success("活跃度配置已保存")
      config.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  if (config.status === "loading" && !form) return <LoadingState rows={6} />
  if (config.status === "error" && !form) return <ErrorState message={config.error} onRetry={() => config.reload()} />
  if (!form) return null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">计分与发放配置</CardTitle>
        <CardDescription>
          每日按维度计数 × 权重（受日上限约束）累加得分，按换算率折算积分并叠加等级加成后发放。
          {config.data?.updated_at && (
            <> 上次更新：{new Date(config.data.updated_at).toLocaleString()}</>
          )}
        </CardDescription>
        <CardAction>
          <div className="flex items-center gap-2">
            <Switch
              checked={form.enabled}
              onCheckedChange={next => setForm({ ...form, enabled: Boolean(next) })}
              aria-label="活跃度总开关"
            />
            <span key={String(form.enabled)} className="t-text-swap text-sm">
              {form.enabled ? "已启用" : "已停用"}
            </span>
          </div>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        <div className="grid gap-4 md:grid-cols-2">
          {DIMENSIONS.map(dim => (
            <div key={dim.weight} className="flex flex-col gap-3 rounded-xl border p-4">
              <p className="text-sm font-medium">{dim.label}</p>
              <div className="grid grid-cols-2 gap-3">
                <div className="flex flex-col gap-2">
                  <Label htmlFor={`activity-${dim.weight}`}>权重</Label>
                  <Input
                    id={`activity-${dim.weight}`}
                    type="number"
                    min={0}
                    step="any"
                    value={form[dim.weight]}
                    onChange={event => setField(dim.weight, event.target.value)}
                  />
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor={`activity-cap-${dim.weight}`}>日上限</Label>
                  {dim.cap ? (
                    <Input
                      id={`activity-cap-${dim.weight}`}
                      type="number"
                      min={0}
                      step={1}
                      value={form[dim.cap]}
                      onChange={event => setField(dim.cap!, event.target.value)}
                    />
                  ) : (
                    <Input id={`activity-cap-${dim.weight}`} value="1（固定）" readOnly disabled />
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>

        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="activity-points-rate">积分换算率</Label>
            <Input
              id="activity-points-rate"
              type="number"
              min={0}
              step="any"
              value={form.points_rate}
              onChange={event => setField("points_rate", event.target.value)}
            />
            <p className="text-xs text-muted-foreground">发放积分 = 当日得分 × 换算率</p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="activity-bonus-per-level">每级加成 %</Label>
            <Input
              id="activity-bonus-per-level"
              type="number"
              min={0}
              step="any"
              value={form.bonus_per_level_pct}
              onChange={event => setField("bonus_per_level_pct", event.target.value)}
            />
            <p className="text-xs text-muted-foreground">每提升一级额外加成的百分比</p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="activity-max-bonus">加成封顶 %</Label>
            <Input
              id="activity-max-bonus"
              type="number"
              min={0}
              step="any"
              value={form.max_bonus_pct}
              onChange={event => setField("max_bonus_pct", event.target.value)}
            />
            <p className="text-xs text-muted-foreground">等级加成的最大百分比上限</p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="activity-day-offset">日偏移分钟</Label>
            <Input
              id="activity-day-offset"
              type="number"
              min={0}
              step={1}
              value={form.day_offset_minutes}
              onChange={event => setField("day_offset_minutes", event.target.value)}
            />
            <p className="text-xs text-muted-foreground">480 = 北京时间 UTC+8</p>
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="activity-thresholds">等级门槛（JSON 数组）</Label>
          <Textarea
            id="activity-thresholds"
            value={form.thresholds}
            spellCheck={false}
            rows={3}
            onChange={event => {
              setForm({ ...form, thresholds: event.target.value })
              if (thresholdError) setThresholdError("")
            }}
            onBlur={() => setThresholdError(parseThresholds(form.thresholds).error ?? "")}
            className={cn("font-mono text-sm", thresholdError && "border-destructive focus-visible:ring-destructive/30")}
            aria-invalid={Boolean(thresholdError)}
          />
          {thresholdError ? (
            <p className="text-xs text-destructive">{thresholdError}</p>
          ) : (
            <p className="text-xs text-muted-foreground">第 i 项 = 达到 Lv i+1 所需累计总分；必须严格递增，如 [10, 30, 60, 120]。</p>
          )}
        </div>

        <div className="flex justify-end">
          <Button
            onClick={onSave}
            disabled={saving || numericInvalid || Boolean(thresholdError)}
            className="transition-transform active:scale-[0.96]"
          >
            {saving ? (
              <Loader2Icon data-icon="inline-start" className="animate-spin" />
            ) : (
              <SaveIcon data-icon="inline-start" />
            )}
            {saving ? "保存中…" : "保存配置"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 手动结算
// ---------------------------------------------------------------------------

function SettleCard() {
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  async function onConfirmSettle() {
    setBusy(true)
    try {
      const result = await triggerActivitySettle()
      toast.success(`已结算 ${result.settled} 行`)
      setConfirmOpen(false)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "结算失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">手动结算</CardTitle>
        <CardDescription>正常情况下由后台定时结算；此按钮立即补跑一轮，结算所有已过界未发放的活跃日并发放积分。</CardDescription>
        <CardAction>
          <Button
            variant="outline"
            onClick={() => setConfirmOpen(true)}
            className="transition-transform active:scale-[0.96]"
          >
            <CalculatorIcon data-icon="inline-start" />
            手动结算
          </Button>
        </CardAction>
      </CardHeader>
      <Dialog open={confirmOpen} onOpenChange={next => !busy && setConfirmOpen(next)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>确认手动结算？</DialogTitle>
            <DialogDescription>
              将立即补跑一轮结算：所有已过业务日界、尚未发放的活跃日会被计分并向对应用户发放积分。
              该操作幂等，不会重复发放已结算的日期。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)} disabled={busy}>
              取消
            </Button>
            <Button
              onClick={onConfirmSettle}
              disabled={busy}
              className="transition-transform active:scale-[0.96]"
            >
              {busy ? (
                <Loader2Icon data-icon="inline-start" className="animate-spin" />
              ) : (
                <CalculatorIcon data-icon="inline-start" />
              )}
              {busy ? "结算中…" : "确认结算"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 用户明细查询
// ---------------------------------------------------------------------------

function UserDetailCard() {
  const [query, setQuery] = useState("")
  const [search, setSearch] = useState("")
  const [selected, setSelected] = useState<PlatformUser | null>(null)

  // 用户防抖搜索（复用平台用户目录接口）
  useEffect(() => {
    const timer = setTimeout(() => setSearch(query.trim()), 300)
    return () => clearTimeout(timer)
  }, [query])

  const users = useAsyncData(
    search ? () => listPlatformUsers({ q: search, limit: 20 }).then(raw => raw.users) : null,
    [search]
  )

  // 搜索候选 + 已选中用户的头像框（bot 用户不查询）
  const avatarFrames = useAvatarFrames(
    [...(users.data ?? []), ...(selected ? [selected] : [])]
      .filter(user => !user.is_bot)
      .map(user => user.id)
  )

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">用户活跃明细</CardTitle>
        <CardDescription>按用户名 / 邮箱搜索并选中用户，查看其活跃总分、等级与近 30 天逐日明细。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="relative w-full max-w-md">
          <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            aria-label="搜索用户"
            placeholder="搜索用户名 / 邮箱"
            value={query}
            onChange={event => setQuery(event.target.value)}
            className="pl-8"
          />
        </div>
        {selected && (
          <div className="flex items-center gap-3 rounded-xl border border-primary/40 bg-primary/5 px-4 py-3">
            <FramedAvatar frame={avatarFrames[selected.id]}>
              <Avatar className="size-9">
                {selected.avatar_url && <AvatarImage src={selected.avatar_url} alt="" />}
                <AvatarFallback>{selected.username.slice(0, 2).toUpperCase()}</AvatarFallback>
              </Avatar>
            </FramedAvatar>
            <div className="min-w-0 flex-1">
              <p className="flex items-center gap-1.5 truncate text-sm font-medium">
                {selected.username}
                <Badge variant="default" className="gap-1">
                  <CheckCircle2Icon className="size-3" />
                  已选中
                </Badge>
              </p>
              <p className="truncate font-mono text-xs text-muted-foreground">
                {selected.email} · {selected.id}
              </p>
            </div>
            <Button variant="ghost" size="xs" onClick={() => setSelected(null)}>
              取消选择
            </Button>
          </div>
        )}
        {users.status === "loading" && <LoadingState rows={3} />}
        {users.status === "error" && <ErrorState message={users.error} onRetry={() => users.reload()} />}
        {users.status === "success" && (users.data ?? []).length === 0 && (
          <EmptyState title="没有匹配的用户" description="换个关键词试试。" className="py-8" />
        )}
        {users.status === "success" &&
          (users.data ?? [])
            .filter(user => user.id !== selected?.id)
            .map((user, index) => (
              <button
                key={user.id}
                type="button"
                onClick={() => setSelected(user)}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className={cn(
                  "anim-item flex items-center gap-3 rounded-xl border px-4 py-2.5 text-left transition-[background-color,transform] hover:bg-muted/50 active:scale-[0.99]",
                  "outline-none focus-visible:ring-3 focus-visible:ring-ring/30"
                )}
              >
                <FramedAvatar frame={avatarFrames[user.id]}>
                  <Avatar className="size-8">
                    {user.avatar_url && <AvatarImage src={user.avatar_url} alt="" />}
                    <AvatarFallback>{user.username.slice(0, 2).toUpperCase()}</AvatarFallback>
                  </Avatar>
                </FramedAvatar>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{user.username}</p>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {user.email} · {user.id}
                  </p>
                </div>
                {user.is_bot && <Badge variant="secondary">机器人</Badge>}
              </button>
            ))}

        {selected ? (
          <UserActivityDetail user={selected} />
        ) : (
          <EmptyState
            icon={UserSearchIcon}
            title="先选择一个用户"
            description="搜索并选中用户后展示其活跃度汇总与逐日明细。"
            className="py-8"
          />
        )}
      </CardContent>
    </Card>
  )
}

const userTrendConfig = {
  score: { label: "得分", color: "var(--chart-1)" },
} satisfies ChartConfig

/** 用户趋势图 tooltip：日期 / 得分 / 发放状态（不单靠颜色区分已发放与否） */
function UserTrendTooltip({
  active,
  payload,
}: {
  active?: boolean
  payload?: Array<{ payload?: { day: string; score: number; granted: boolean; granted_points: number } }>
}) {
  const entry = payload?.[0]?.payload
  if (!active || !entry) return null
  return (
    <div className="rounded-lg border bg-popover px-2.5 py-1.5 text-xs shadow-md">
      <p className="font-mono tabular-nums">{entry.day}</p>
      <p className="mt-0.5 text-muted-foreground">
        得分 <span className="text-foreground tabular-nums">{entry.score}</span>
      </p>
      <p className="text-muted-foreground">
        {entry.granted ? `已发放 +${entry.granted_points} 积分` : "未发放"}
      </p>
    </div>
  )
}

function UserActivityDetail({ user }: { user: PlatformUser }) {
  const detail = useAsyncData(() => getActivityUserDetail(user.id), [user.id])

  if (detail.status === "loading") return <LoadingState rows={4} />
  if (detail.status === "error") return <ErrorState message={detail.error} onRetry={() => detail.reload()} />
  if (!detail.data) return null

  const data = detail.data
  const trend = [...data.days].reverse()
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-4 rounded-xl border px-4 py-3">
        <div>
          <p className="text-xs text-muted-foreground">累计总分</p>
          <p className="text-xl font-semibold tabular-nums">
            <span key={data.total_score} className="t-number-pop">
              {data.total_score}
            </span>
          </p>
        </div>
        <div>
          <p className="text-xs text-muted-foreground">当前等级</p>
          <p className="text-xl font-semibold tabular-nums">
            <span key={data.level} className="t-number-pop">
              Lv {data.level}
            </span>
          </p>
        </div>
        <p className="ml-auto text-xs text-muted-foreground">近 {data.days.length} 天明细</p>
      </div>
      {trend.length > 1 ? (
        <div className="rounded-xl border px-4 pt-3 pb-1">
          <ChartContainer config={userTrendConfig} className="h-[140px] w-full">
            <BarChart data={trend} margin={{ top: 4, left: 0, right: 0 }}>
              <XAxis
                dataKey="day"
                tickFormatter={value => (typeof value === "string" && value.length >= 10 ? value.slice(5) : String(value))}
                tickLine={false}
                axisLine={false}
                fontSize={10}
                interval="preserveStartEnd"
              />
              <ChartTooltip cursor={false} content={<UserTrendTooltip />} />
              <Bar dataKey="score" radius={[3, 3, 0, 0]} maxBarSize={20}>
                {trend.map(entry => (
                  <Cell key={entry.day} fill="var(--color-score)" fillOpacity={entry.granted ? 0.9 : 0.35} />
                ))}
              </Bar>
            </BarChart>
          </ChartContainer>
        </div>
      ) : null}
      {data.days.length === 0 ? (
        <EmptyState title="暂无活跃记录" description="该用户在查询周期内没有活跃数据。" className="py-8" />
      ) : (
        <div className="overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>日期</TableHead>
                <TableHead className="text-right">消息</TableHead>
                <TableHead className="text-right">语音分钟</TableHead>
                <TableHead className="text-right">回应</TableHead>
                <TableHead className="text-right">登录</TableHead>
                <TableHead className="text-right">得分</TableHead>
                <TableHead className="text-right">发放积分</TableHead>
                <TableHead>状态</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.days.map(row => (
                <TableRow key={row.day}>
                  <TableCell className="font-mono text-xs">{row.day}</TableCell>
                  <TableCell className="text-right tabular-nums">{row.msg_count}</TableCell>
                  <TableCell className="text-right tabular-nums">{row.voice_minutes}</TableCell>
                  <TableCell className="text-right tabular-nums">{row.reaction_count}</TableCell>
                  <TableCell className="text-right tabular-nums">{row.login_count}</TableCell>
                  <TableCell className="text-right tabular-nums">{row.score}</TableCell>
                  <TableCell className="text-right tabular-nums">{row.granted_points}</TableCell>
                  <TableCell>
                    {row.granted ? (
                      <Badge variant="default" title={row.granted_at ? new Date(row.granted_at).toLocaleString() : undefined}>
                        已发放
                      </Badge>
                    ) : (
                      <Badge variant="outline">未发放</Badge>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
