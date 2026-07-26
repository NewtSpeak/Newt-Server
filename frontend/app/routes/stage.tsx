import { useMemo, useState } from "react"
import { useOutletContext } from "react-router"
import {
  ArrowDownToLineIcon,
  ArrowUpFromLineIcon,
  MicIcon,
  TriangleAlertIcon,
  Users2Icon,
  Volume2Icon,
  XIcon,
} from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Label } from "~/components/ui/label"
import { Slider } from "~/components/ui/slider"
import { Switch } from "~/components/ui/switch"
import { FramedAvatar } from "~/components/user-avatar-frame"
import { useAsyncData } from "~/hooks/use-async-data"
import { useAvatarFrames } from "~/hooks/use-avatar-frames"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { useGuildID } from "~/hooks/use-guild-id"
import {
  getStageQueue,
  listChannels,
  listVoiceStates,
  patchVoiceStage,
  stageBringDown,
  stageBringUp,
  stageRemoveFromQueue,
  type Channel,
  type StageQueueEntry,
  type VoiceChannelMode,
  type VoiceState,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatRelative } from "~/lib/format"
import { cn } from "~/lib/utils"

const QUEUE_SOURCE_LABELS = { USER_APPLY: "用户申请", CAPACITY_QUEUE: "容量禁说", ADMIN: "管理加入" } as const

export default function StagePage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)
  const [channelID, setChannelID] = useState<string | null>(null)

  const channels = useAsyncData<Channel[]>(guildID ? () => listChannels(guildID) : null, [guildID])
  const voiceChannels = useMemo(() => (channels.data ?? []).filter(channel => channel.type === "VOICE"), [channels.data])
  const activeChannel = channelID ?? voiceChannels[0]?.id ?? null

  const queue = useAsyncData<StageQueueEntry[]>(activeChannel ? () => getStageQueue(activeChannel) : null, [activeChannel])
  const states = useAsyncData<VoiceState[]>(
    guildID && activeChannel ? () => listVoiceStates(guildID, activeChannel) : null,
    [guildID, activeChannel]
  )

  useGatewayEvent(["STAGE_QUEUE_UPDATE", "STAGE_INSTANCE_UPDATE"], () => queue.reload(true))
  useGatewayEvent("VOICE_STATE_UPDATE", () => states.reload(true))

  // 舞台配置（契约暂无回读接口，按文档默认值初始化，「应用配置」下发 PATCH）
  const [mode, setMode] = useState<VoiceChannelMode>("STAGE")
  const [maxSpeakers, setMaxSpeakers] = useState(20)
  const [requestToSpeak, setRequestToSpeak] = useState(true)
  const [applying, setApplying] = useState(false)

  async function onApply() {
    if (!activeChannel) return
    setApplying(true)
    try {
      await patchVoiceStage(activeChannel, { mode, max_speakers: maxSpeakers, request_to_speak_enabled: requestToSpeak })
      toast.success("舞台配置已应用")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "配置应用失败")
    } finally {
      setApplying(false)
    }
  }

  async function onBringUp(userID: string, name: string) {
    if (!activeChannel) return
    try {
      await stageBringUp(activeChannel, userID)
      toast.success(`已将「${name}」抱上麦`)
      queue.reload(true)
      states.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "抱上失败（台上可能已满员）")
    }
  }

  async function onBringDown(userID: string, name: string) {
    if (!activeChannel) return
    try {
      await stageBringDown(activeChannel, userID)
      toast.success(`已将「${name}」抱下（不回申请队列）`)
      queue.reload(true)
      states.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "抱下失败")
    }
  }

  async function onRemoveFromQueue(userID: string, name: string) {
    if (!activeChannel) return
    if (!window.confirm(`确定将「${name}」移出申请队列？对方可重新申请。`)) return
    try {
      await stageRemoveFromQueue(activeChannel, userID)
      toast.success(`已将「${name}」移出队列`)
      queue.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "移出队列失败")
    }
  }

  const speakers = (states.data ?? []).filter(state => state.stage_role === "SPEAKER")
  const queueList = queue.data ?? []
  // 台上成员的头像框（申请队列行无头像，不查询）
  const avatarFrames = useAvatarFrames(speakers.map(speaker => speaker.user_id))

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="舞台管理"
        description="语音频道双模式：自由讨论 / 舞台。>50 人自动强制舞台并按 FIFO 容量禁说；台上默认 20 席、硬顶 50。"
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={next => {
              setGuildID(next)
              setChannelID(null)
            }}
            options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
            className="w-52"
          />
          <SimpleSelect
            ariaLabel="选择语音频道"
            placeholder="选择语音频道"
            value={activeChannel}
            onChange={setChannelID}
            options={voiceChannels.map(channel => ({ value: channel.id, label: `🔊 ${channel.name}` }))}
            className="w-52"
            disabled={voiceChannels.length === 0}
          />
        </div>

        {channels.status === "success" && voiceChannels.length === 0 && (
          <EmptyState icon={Volume2Icon} title="该服务器没有语音频道" description="先在服务器详情中创建语音频道。" />
        )}

        {activeChannel && (
          <div className="grid gap-4 xl:grid-cols-3">
            {/* 频道配置 */}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">频道模式</CardTitle>
                <CardDescription>协管默认可改模式（可由服配置关闭）；超过 50 人时禁止切回自由讨论。</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-5">
                <div role="radiogroup" aria-label="频道模式" className="grid grid-cols-2 gap-2">
                  {(
                    [
                      { value: "FREE_DISCUSSION", label: "自由讨论", note: "有发言权限即可开麦" },
                      { value: "STAGE", label: "舞台", note: "仅台上可发言，申请上麦" },
                    ] as const
                  ).map(option => (
                    <button
                      key={option.value}
                      type="button"
                      role="radio"
                      aria-checked={mode === option.value}
                      onClick={() => setMode(option.value)}
                      className={cn(
                        "flex flex-col gap-1 rounded-xl border p-3 text-left transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none active:scale-[0.98]",
                        mode === option.value ? "border-primary/60 bg-primary/5" : "hover:bg-muted/50"
                      )}
                    >
                      <span className="text-sm font-medium">{option.label}</span>
                      <span className="text-xs text-muted-foreground">{option.note}</span>
                    </button>
                  ))}
                </div>

                <div className="grid gap-2">
                  <div className="flex items-baseline justify-between">
                    <Label>台上人数上限（max_speakers）</Label>
                    <span key={maxSpeakers} className="t-number-pop text-sm font-semibold tabular-nums">
                      {maxSpeakers}
                    </span>
                  </div>
                  <Slider
                    aria-label="台上人数上限"
                    min={1}
                    max={50}
                    step={1}
                    value={maxSpeakers}
                    onValueChange={value => setMaxSpeakers(Array.isArray(value) ? value[0] : value)}
                  />
                  <div className="flex justify-between text-[10px] text-muted-foreground tabular-nums">
                    <span>1</span>
                    <span>软限 20</span>
                    <span>硬顶 50</span>
                  </div>
                  {maxSpeakers > 20 && (
                    <p className="t-text-swap flex items-start gap-1.5 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
                      <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
                      超过推荐软限 20：台上路数越多，听众下行带宽越大，请评估节点容量。
                    </p>
                  )}
                </div>

                <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
                  <div>
                    <p className="text-sm font-medium">开放申请上麦</p>
                    <p className="text-xs text-muted-foreground">关闭后仅可由管理/协管直接抱麦</p>
                  </div>
                  <Switch checked={requestToSpeak} onCheckedChange={setRequestToSpeak} aria-label="开放申请上麦" />
                </div>

                <Button onClick={onApply} disabled={applying}>
                  {applying ? "应用中…" : "应用配置"}
                </Button>
              </CardContent>
            </Card>

            {/* 台上成员 */}
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <MicIcon className="size-4" />
                  台上成员
                  <Badge variant="secondary" className="tabular-nums">
                    {speakers.length} / {maxSpeakers}
                  </Badge>
                </CardTitle>
                <CardDescription>静音与抱下分离：静音仍占席位，抱下释放席位且不回队列。</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {states.status === "loading" && <LoadingState rows={3} />}
                {states.status === "error" && <ErrorState message={states.error} onRetry={() => states.reload()} />}
                {states.status === "success" && speakers.length === 0 && (
                  <EmptyState title="台上暂无成员" description="从右侧申请队列抱上第一位发言者。" className="py-8" />
                )}
                {speakers.map((speaker, index) => {
                  const name = speaker.username ?? speaker.user_id
                  return (
                    <div
                      key={speaker.user_id}
                      style={{ "--stagger-index": index } as React.CSSProperties}
                      className="anim-item flex items-center gap-3 rounded-xl border px-3 py-2.5"
                    >
                      <FramedAvatar frame={avatarFrames[speaker.user_id]}>
                        <Avatar className="size-8">
                          <AvatarFallback>{name.slice(0, 2).toUpperCase()}</AvatarFallback>
                        </Avatar>
                      </FramedAvatar>
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm font-medium">{name}</p>
                        {(speaker.server_mute || speaker.self_mute) && (
                          <p className="text-xs text-destructive">已静音（仍占席位）</p>
                        )}
                      </div>
                      <Button variant="outline" size="sm" onClick={() => onBringDown(speaker.user_id, name)}>
                        <ArrowDownToLineIcon data-icon="inline-start" />
                        抱下
                      </Button>
                    </div>
                  )
                })}
              </CardContent>
            </Card>

            {/* 申请队列 */}
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <Users2Icon className="size-4" />
                  申请队列
                  <Badge variant="secondary" className="tabular-nums">
                    {queueList.length} / 100
                  </Badge>
                </CardTitle>
                <CardDescription>FIFO 先进先出；容量禁说者自动进入队尾；条目 30 分钟未处理过期。</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {queue.status === "loading" && <LoadingState rows={3} />}
                {queue.status === "error" && <ErrorState message={queue.error} onRetry={() => queue.reload()} />}
                {queue.status === "success" && queueList.length === 0 && (
                  <EmptyState title="队列为空" description="用户申请上麦后会按先后顺序显示在这里。" className="py-8" />
                )}
                {queueList.map((entry, index) => {
                  const name = entry.username ?? entry.user_id
                  const full = speakers.length >= maxSpeakers
                  return (
                    <div
                      key={entry.user_id}
                      style={{ "--stagger-index": index } as React.CSSProperties}
                      className="anim-item flex items-center gap-3 rounded-xl border px-3 py-2.5"
                    >
                      <span className="grid size-6 shrink-0 place-items-center rounded-full bg-muted font-mono text-xs tabular-nums">
                        {index + 1}
                      </span>
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm font-medium">{name}</p>
                        <p className="flex items-center gap-2 text-xs text-muted-foreground">
                          {entry.source && <span>{QUEUE_SOURCE_LABELS[entry.source]}</span>}
                          <span>{formatRelative(entry.requested_at)}</span>
                        </p>
                      </div>
                      <Button size="sm" onClick={() => onBringUp(entry.user_id, name)} disabled={full}>
                        <ArrowUpFromLineIcon data-icon="inline-start" />
                        {full ? "台上已满" : "抱上"}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`将 ${name} 移出队列`}
                        onClick={() => onRemoveFromQueue(entry.user_id, name)}
                      >
                        <XIcon />
                      </Button>
                    </div>
                  )
                })}
              </CardContent>
            </Card>
          </div>
        )}
      </section>
    </main>
  )
}
