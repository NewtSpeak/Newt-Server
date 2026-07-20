import { useMemo, useState, type FormEvent } from "react"
import { useOutletContext } from "react-router"
import { EyeOffIcon, HeadphoneOffIcon, InfoIcon, MicOffIcon, PenOffIcon, PlusIcon, ShieldOffIcon } from "lucide-react"
import { toast } from "sonner"

import { Countdown } from "~/components/countdown"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
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
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { useGuildID } from "~/hooks/use-guild-id"
import {
  createRestriction,
  liftRestriction,
  listChannels,
  listMembers,
  listRestrictions,
  memberName,
  type Channel,
  type GuildMember,
  type Restriction,
  type RestrictionDeny,
  type RestrictionKind,
  type RestrictionScope,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatTime } from "~/lib/format"

const SCOPE_META: Record<RestrictionScope, { label: string; channelKind: "TEXT" | "VOICE" | null; dims: (keyof RestrictionDeny)[] }> = {
  TEXT_CHANNEL: { label: "单个文字频道", channelKind: "TEXT", dims: ["view_text", "send_text"] },
  VOICE_CHANNEL: { label: "单个语音频道", channelKind: "VOICE", dims: ["listen_voice", "speak_voice"] },
  GUILD_ALL_TEXT: { label: "全服文字频道", channelKind: null, dims: ["view_text", "send_text"] },
  GUILD_ALL_VOICE: { label: "全服语音频道", channelKind: null, dims: ["listen_voice", "speak_voice"] },
}

const DIM_META: Record<keyof RestrictionDeny, { label: string; icon: typeof EyeOffIcon }> = {
  view_text: { label: "禁止观看", icon: EyeOffIcon },
  send_text: { label: "禁止发言", icon: PenOffIcon },
  listen_voice: { label: "禁止收听", icon: HeadphoneOffIcon },
  speak_voice: { label: "禁止说话", icon: MicOffIcon },
}

const DURATION_PRESETS = [
  { label: "1 小时", minutes: 60 },
  { label: "24 小时", minutes: 60 * 24 },
  { label: "7 天", minutes: 60 * 24 * 7 },
  { label: "28 天", minutes: 60 * 24 * 28 },
  { label: "长期", minutes: null },
] as const

export default function RestrictionsPage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)
  const [onlyActive, setOnlyActive] = useState(true)

  const restrictions = useAsyncData<Restriction[]>(
    guildID ? () => listRestrictions(guildID, { active: onlyActive ? true : undefined }) : null,
    [guildID, onlyActive]
  )
  const members = useAsyncData<GuildMember[]>(guildID ? () => listMembers(guildID) : null, [guildID])
  const channels = useAsyncData<Channel[]>(guildID ? () => listChannels(guildID) : null, [guildID])

  useGatewayEvent(["RESTRICTION_CREATE", "RESTRICTION_UPDATE", "RESTRICTION_LIFT"], () => restrictions.reload(true))

  // ---------------- 创建表单 ----------------
  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [targetID, setTargetID] = useState<string | null>(null)
  const [scope, setScope] = useState<RestrictionScope>("TEXT_CHANNEL")
  const [channelTarget, setChannelTarget] = useState<string | null>(null)
  const [deny, setDeny] = useState<RestrictionDeny>({})
  const [kind, setKind] = useState<RestrictionKind>("SANCTION")
  const [durationMinutes, setDurationMinutes] = useState<number | null>(60)

  const scopeMeta = SCOPE_META[scope]
  const channelOptions = useMemo(
    () =>
      (channels.data ?? [])
        .filter(channel => channel.type === scopeMeta.channelKind)
        .map(channel => ({ value: channel.id, label: channel.name })),
    [channels.data, scopeMeta.channelKind]
  )

  function toggleDeny(dim: keyof RestrictionDeny, on: boolean) {
    setDeny(current => {
      const next = { ...current, [dim]: on }
      // 蕴含规则（文档 12 §2.3）：禁看 ⇒ 禁发；禁听 ⇒ 禁说
      if (dim === "view_text" && on) next.send_text = true
      if (dim === "listen_voice" && on) next.speak_voice = true
      // 反向约束：解除「禁发/禁说」时若仍勾选「禁看/禁听」则不允许
      if (dim === "send_text" && !on && current.view_text) next.send_text = true
      if (dim === "speak_voice" && !on && current.listen_voice) next.speak_voice = true
      return next
    })
  }

  function resetForm() {
    setTargetID(null)
    setScope("TEXT_CHANNEL")
    setChannelTarget(null)
    setDeny({})
    setKind("SANCTION")
    setDurationMinutes(60)
  }

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!guildID || !targetID) return
    const reason = String(new FormData(event.currentTarget).get("reason") ?? "").trim()
    const dims = scopeMeta.dims.filter(dim => deny[dim])
    if (dims.length === 0) {
      toast.error("请至少选择一个限制维度")
      return
    }
    if (scopeMeta.channelKind && !channelTarget) {
      toast.error("单频道限制需要选择目标频道")
      return
    }
    setCreating(true)
    try {
      await createRestriction(guildID, {
        target_user_id: targetID,
        scope,
        channel_id: scopeMeta.channelKind ? channelTarget! : undefined,
        deny: Object.fromEntries(dims.map(dim => [dim, true])),
        kind,
        reason,
        expires_at: durationMinutes === null ? null : new Date(Date.now() + durationMinutes * 60_000).toISOString(),
      })
      toast.success("限制已生效，将实时推送给当事人")
      setCreateOpen(false)
      resetForm()
      restrictions.reload(true)
    } catch (reason_) {
      toast.error(reason_ instanceof Error ? reason_.message : "限制创建失败")
    } finally {
      setCreating(false)
    }
  }

  async function onLift(item: Restriction) {
    if (!guildID) return
    if (!window.confirm("确定提前解除该限制？")) return
    try {
      await liftRestriction(guildID, item.id)
      toast.success("限制已解除")
      restrictions.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "解除失败")
    }
  }

  const memberByID = new Map((members.data ?? []).map(member => [member.user_id, member]))
  const channelByID = new Map((channels.data ?? []).map(channel => [channel.id, channel]))
  const list = restrictions.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="多维限制"
        description="叠加在 RBAC 之上的否定层（只收紧不放宽）：文字 看/说、语音 听/说 × 单频道或全服。"
        actions={
          <Button onClick={() => setCreateOpen(true)} disabled={!guildID}>
            <PlusIcon data-icon="inline-start" />
            创建限制
          </Button>
        }
      />

      <Dialog
        open={createOpen}
        onOpenChange={open => {
          setCreateOpen(open)
          if (!open) resetForm()
        }}
      >
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>创建限制</DialogTitle>
            <DialogDescription>reason 必填且对当事人可见；临时限制 60 秒 – 28 天，长期仅用于频道封禁或特许场景。</DialogDescription>
          </DialogHeader>
          <form onSubmit={onCreate} className="grid gap-4">
            <div className="grid gap-2 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label>目标成员 *</Label>
                <SimpleSelect
                  ariaLabel="目标成员"
                  placeholder="选择成员"
                  value={targetID}
                  onChange={setTargetID}
                  options={(members.data ?? []).map(member => ({ value: member.user_id, label: memberName(member) }))}
                  className="w-full"
                />
              </div>
              <div className="grid gap-1.5">
                <Label>作用域 *</Label>
                <SimpleSelect
                  ariaLabel="作用域"
                  value={scope}
                  onChange={next => {
                    setScope(next as RestrictionScope)
                    setChannelTarget(null)
                    setDeny({})
                  }}
                  options={Object.entries(SCOPE_META).map(([value, meta]) => ({ value, label: meta.label }))}
                  className="w-full"
                />
              </div>
            </div>

            {scopeMeta.channelKind && (
              <div className="grid gap-1.5">
                <Label>目标频道 *</Label>
                <SimpleSelect
                  ariaLabel="目标频道"
                  placeholder={scopeMeta.channelKind === "TEXT" ? "选择文字频道" : "选择语音频道"}
                  value={channelTarget}
                  onChange={setChannelTarget}
                  options={channelOptions}
                  className="w-full"
                />
              </div>
            )}

            <fieldset className="grid gap-2">
              <legend className="mb-1 text-sm font-medium">限制维度 *</legend>
              <div className="grid grid-cols-2 gap-2">
                {scopeMeta.dims.map(dim => {
                  const meta = DIM_META[dim]
                  const implied = (dim === "send_text" && deny.view_text) || (dim === "speak_voice" && deny.listen_voice)
                  return (
                    <Label
                      key={dim}
                      className={`flex min-h-11 cursor-pointer items-center gap-2.5 rounded-xl border px-3 py-2 transition-[background-color,border-color] ${deny[dim] ? "border-destructive/40 bg-destructive/5" : "hover:bg-muted/50"}`}
                    >
                      <Checkbox checked={Boolean(deny[dim])} disabled={implied} onCheckedChange={next => toggleDeny(dim, Boolean(next))} />
                      <meta.icon className="size-4 text-muted-foreground" />
                      <span className="text-sm">{meta.label}</span>
                      {implied && <span className="ml-auto text-[10px] text-muted-foreground">被蕴含</span>}
                    </Label>
                  )
                })}
              </div>
              <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
                <InfoIcon className="mt-0.5 size-3.5 shrink-0" />
                蕴含规则：禁看 ⇒ 自动禁发；禁听 ⇒ 自动禁说（禁听同时会阻止加入语音频道）。
              </p>
            </fieldset>

            <div className="grid gap-2 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label>类型</Label>
                <SimpleSelect
                  ariaLabel="限制类型"
                  value={kind}
                  onChange={next => setKind(next as RestrictionKind)}
                  options={[
                    { value: "SANCTION", label: "临时制裁（SANCTION）" },
                    { value: "CHANNEL_BAN", label: "频道封禁（CHANNEL_BAN）" },
                  ]}
                  className="w-full"
                />
              </div>
              <div className="grid gap-1.5">
                <Label>时长</Label>
                <div className="flex flex-wrap gap-1.5">
                  {DURATION_PRESETS.map(preset => (
                    <button
                      key={preset.label}
                      type="button"
                      onClick={() => setDurationMinutes(preset.minutes)}
                      className={`rounded-full border px-3 py-1.5 text-xs transition-[background-color,color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none ${durationMinutes === preset.minutes ? "border-primary/60 bg-primary/10 font-medium text-foreground" : "text-muted-foreground hover:bg-muted"}`}
                    >
                      {preset.label}
                    </button>
                  ))}
                </div>
              </div>
            </div>

            <div className="grid gap-1.5">
              <Label htmlFor="restriction-reason">原因 *（当事人可见，最多 512 字）</Label>
              <Input id="restriction-reason" name="reason" placeholder="如：频道内刷屏" required maxLength={512} />
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                取消
              </Button>
              <Button type="submit" variant="destructive" disabled={creating || !targetID}>
                {creating ? "创建中…" : "创建限制"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={setGuildID}
            options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
            className="w-52"
          />
          <Label className="flex cursor-pointer items-center gap-2 rounded-full border px-3 py-1.5 text-xs">
            <Checkbox checked={onlyActive} onCheckedChange={next => setOnlyActive(Boolean(next))} />
            仅显示生效中
          </Label>
        </div>

        {restrictions.status === "loading" && <LoadingState rows={4} />}
        {restrictions.status === "error" && <ErrorState message={restrictions.error} onRetry={() => restrictions.reload()} />}
        {restrictions.status === "success" && list.length === 0 && (
          <EmptyState icon={ShieldOffIcon} title="暂无限制记录" description="对成员施加的多维限制会显示在这里，可随时提前解除。" />
        )}

        {restrictions.status === "success" &&
          list.map((item, index) => {
            const member = memberByID.get(item.target_user_id)
            const channel = item.channel_id ? channelByID.get(item.channel_id) : null
            const dims = (Object.keys(DIM_META) as (keyof RestrictionDeny)[]).filter(dim => item.deny?.[dim])
            const lifted = Boolean(item.lifted_at) || item.active === false
            return (
              <article
                key={item.id}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className={`anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 ${lifted ? "opacity-60" : ""}`}
              >
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
                    {item.target_username ?? (member ? memberName(member) : item.target_user_id)}
                    <Badge variant={item.kind === "CHANNEL_BAN" ? "destructive" : "secondary"}>
                      {item.kind === "CHANNEL_BAN" ? "频道封禁" : "临时制裁"}
                    </Badge>
                    <Badge variant="outline">
                      {SCOPE_META[item.scope]?.label ?? item.scope}
                      {channel ? ` · ${channel.name}` : ""}
                    </Badge>
                  </p>
                  <p className="mt-1 truncate text-xs text-muted-foreground">
                    {item.reason ? `原因：${item.reason}` : "未填写原因"} · 创建于 {formatTime(item.created_at)}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-1.5">
                  {dims.map(dim => {
                    const meta = DIM_META[dim]
                    return (
                      <Badge key={dim} variant="destructive">
                        <meta.icon />
                        {meta.label}
                      </Badge>
                    )
                  })}
                </div>
                <div className="w-24 text-right text-xs">
                  <p className="text-muted-foreground">剩余</p>
                  <Countdown expiresAt={item.expires_at} onExpire={() => restrictions.reload(true)} className="text-sm font-medium" />
                </div>
                {!lifted && (
                  <Button variant="outline" size="sm" onClick={() => onLift(item)}>
                    解除
                  </Button>
                )}
              </article>
            )
          })}
      </section>
    </main>
  )
}
