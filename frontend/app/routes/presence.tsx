import { useEffect, useState } from "react"
import { useOutletContext } from "react-router"
import { DownloadIcon, EyeIcon, EyeOffIcon, MicIcon, RadioIcon, SendIcon } from "lucide-react"
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
import { useGuildID } from "~/hooks/use-guild-id"
import {
  downloadAuditRecord,
  getChannelAudit,
  getPlatformAudit,
  getVoiceStealth,
  listAuditRecords,
  listChannels,
  postPresenceMessage,
  putChannelAudit,
  putPlatformAudit,
  putVoiceStealth,
  type AuditRecord,
  type Channel,
  type ChannelAuditConfig,
  type PlatformAuditConfig,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatBytes, formatTime } from "~/lib/format"

export default function PresencePage() {
  const { user, guilds } = useOutletContext<ConsoleContext>()

  if (!user.system_admin) {
    return (
      <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
        <PageHeader title="临场与音频审计" description="仅系统管理员可使用临场与审计能力。" />
        <section className="px-4 lg:px-6">
          <EmptyState icon={EyeOffIcon} title="权限不足" description="该功能仅对系统管理员开放。" />
        </section>
      </main>
    )
  }

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="临场与音频审计"
        description="系统管理员可随时临场任意频道：文本频道以本人身份发言、语音频道可隐身；语音频道支持全局/单频道音频审计录制到主节点，并可选择是否提示用户。"
      />
      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <PlatformAuditCard />
        <GuildPresenceCard guilds={guilds} />
        <AuditRecordsCard guilds={guilds} />
      </section>
    </main>
  )
}

// ---------------------------------------------------------------------------
// 平台级审计默认
// ---------------------------------------------------------------------------

function PlatformAuditCard() {
  const platform = useAsyncData<PlatformAuditConfig>(() => getPlatformAudit(), [])
  const [record, setRecord] = useState(false)
  const [notify, setNotify] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (platform.data) {
      setRecord(platform.data.record_default)
      setNotify(platform.data.notify_default)
    }
  }, [platform.data])

  async function onSave() {
    setSaving(true)
    try {
      await putPlatformAudit({ record_default: record, notify_default: notify })
      toast.success("平台审计默认已更新")
      platform.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  const ingestEnabled = platform.data?.ingest_enabled !== false

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">平台级音频审计默认</CardTitle>
        <CardDescription>所有语音频道的默认策略；单个频道可在下方独立覆盖。已在房用户会立即收到 token 刷新与提示。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {platform.status === "loading" && <LoadingState rows={2} />}
        {platform.status === "error" && <ErrorState message={platform.error} onRetry={() => platform.reload()} />}
        {platform.status === "success" && (
          <>
            {platform.data && platform.data.ingest_enabled === false && (
              <div
                role="status"
                className="rounded-xl border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-900 dark:text-amber-200"
              >
                <p className="font-medium">审计上传管线未就绪</p>
                <p className="mt-1 text-xs opacity-90">
                  主节点未配置 <code className="rounded bg-muted px-1">AUDIT_INGEST_TOKEN</code>
                  ，SFU 即使录制也无法上传。请在 Owl-Server 设置该环境变量，并在 SFU 配置相同的{" "}
                  <code className="rounded bg-muted px-1">audit_ingest_token</code> 与{" "}
                  <code className="rounded bg-muted px-1">audit_ingest_url</code>
                  （如 <code className="rounded bg-muted px-1">https://你的域名/audit-api/records</code>）。
                </p>
              </div>
            )}
            <ToggleRow
              title="默认录制音频到主节点（审计）"
              description={
                ingestEnabled
                  ? "开启后语音会话的上行音频将录制并上传主节点；中途开关会对在房用户立即生效。"
                  : "开启后 SFU 会本地录制，但因上传密钥未配置，录音不会出现在下方列表。"
              }
              checked={record}
              onChange={setRecord}
              icon={RadioIcon}
            />
            <ToggleRow
              title="默认提示用户正在被审计"
              description="关闭后为静默审计：用户不会收到「本频道被审计」提示。"
              checked={notify}
              onChange={setNotify}
              icon={EyeIcon}
            />
            <Button className="w-fit" onClick={onSave} disabled={saving}>
              {saving ? "保存中…" : "保存平台默认"}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 单服：隐身 + 频道审计覆盖 + 临场发言
// ---------------------------------------------------------------------------

function GuildPresenceCard({ guilds }: { guilds: ConsoleContext["guilds"] }) {
  const [guildID, setGuildID] = useGuildID(guilds)
  const channels = useAsyncData<Channel[]>(guildID ? () => listChannels(guildID) : null, [guildID])
  const stealth = useAsyncData(guildID ? () => getVoiceStealth(guildID) : null, [guildID])

  const [hidden, setHidden] = useState(false)
  useEffect(() => {
    if (stealth.data) setHidden(stealth.data.hidden)
  }, [stealth.data])

  const textChannels = (channels.data ?? []).filter(ch => ch.type === "TEXT")
  const voiceChannels = (channels.data ?? []).filter(ch => ch.type === "VOICE")

  async function toggleStealth(next: boolean) {
    if (!guildID) return
    setHidden(next)
    try {
      await putVoiceStealth(guildID, next)
      toast.success(next ? "已开启语音隐身临场" : "已关闭语音隐身临场")
    } catch (reason) {
      setHidden(!next)
      toast.error(reason instanceof Error ? reason.message : "设置失败")
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">服务器临场</CardTitle>
        <CardDescription>选择服务器后：切换语音隐身、逐个语音频道配置审计、向文本频道临场发言。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <SimpleSelect
          ariaLabel="选择服务器"
          placeholder="选择服务器"
          value={guildID}
          onChange={setGuildID}
          options={guilds.map(g => ({ value: g.id, label: g.name }))}
          className="w-56"
        />

        {guilds.length === 0 && <EmptyState icon={MicIcon} title="暂无服务器" description="先创建服务器。" />}

        {guildID && (
          <>
            <ToggleRow
              title="语音隐身临场"
              description="开启后你加入语音频道时不出现在成员列表、不广播状态；仍可正常收听与发言。"
              checked={hidden}
              onChange={toggleStealth}
              icon={hidden ? EyeOffIcon : EyeIcon}
            />

            <div className="grid gap-2">
              <Label>语音频道音频审计</Label>
              {channels.status === "loading" && <LoadingState rows={2} />}
              {voiceChannels.length === 0 && channels.status === "success" && (
                <p className="text-sm text-muted-foreground">该服务器还没有语音频道。</p>
              )}
              {voiceChannels.map(ch => (
                <ChannelAuditRow key={ch.id} channel={ch} />
              ))}
            </div>

            <PresenceComposer textChannels={textChannels} />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function ChannelAuditRow({ channel }: { channel: Channel }) {
  const cfg = useAsyncData<ChannelAuditConfig>(() => getChannelAudit(channel.id), [channel.id])

  async function update(next: { inherit?: boolean; record?: boolean; notify?: boolean }) {
    try {
      await putChannelAudit(channel.id, next)
      toast.success(`「${channel.name}」审计已更新`)
      cfg.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3">
      <MicIcon className="size-4 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate text-sm font-medium">{channel.name}</span>
      {cfg.status === "success" && cfg.data && (
        <div className="flex flex-wrap items-center gap-4">
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            录制
            <Switch checked={cfg.data.record} onCheckedChange={v => update({ record: v, notify: cfg.data!.notify })} aria-label="录制音频" />
          </label>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            提示用户
            <Switch checked={cfg.data.notify} onCheckedChange={v => update({ record: cfg.data!.record, notify: v })} aria-label="提示用户" />
          </label>
          {cfg.data.has_override ? (
            <Button variant="ghost" size="xs" onClick={() => update({ inherit: true })}>
              跟随平台默认
            </Button>
          ) : (
            <span className="rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground">继承平台</span>
          )}
        </div>
      )}
    </div>
  )
}

function PresenceComposer({ textChannels }: { textChannels: Channel[] }) {
  const [channelID, setChannelID] = useState<string | null>(null)
  const [content, setContent] = useState("")
  const [sending, setSending] = useState(false)

  useEffect(() => {
    if (!channelID && textChannels[0]) setChannelID(textChannels[0].id)
  }, [channelID, textChannels])

  async function onSend() {
    if (!channelID || !content.trim()) return
    setSending(true)
    try {
      await postPresenceMessage(channelID, content.trim())
      toast.success("已以系统管理员身份发言")
      setContent("")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "发送失败")
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="grid gap-2">
      <Label>文本频道临场发言（以系统管理员身份）</Label>
      {textChannels.length === 0 ? (
        <p className="text-sm text-muted-foreground">该服务器还没有文本频道。</p>
      ) : (
        <div className="flex flex-wrap items-end gap-2">
          <SimpleSelect
            ariaLabel="选择文本频道"
            placeholder="选择文本频道"
            value={channelID}
            onChange={setChannelID}
            options={textChannels.map(ch => ({ value: ch.id, label: ch.name }))}
            className="w-44"
          />
          <Input
            value={content}
            onChange={e => setContent(e.target.value)}
            placeholder="输入要发送的消息…"
            maxLength={4000}
            className="min-w-48 flex-1"
            onKeyDown={e => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault()
                onSend()
              }
            }}
          />
          <Button onClick={onSend} disabled={sending || !content.trim()}>
            <SendIcon data-icon="inline-start" />
            {sending ? "发送中…" : "发送"}
          </Button>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 审计录音列表
// ---------------------------------------------------------------------------

function AuditRecordsCard({ guilds }: { guilds: ConsoleContext["guilds"] }) {
  const [guildID, setGuildID] = useState<string>("")
  const records = useAsyncData<AuditRecord[]>(
    () => listAuditRecords(guildID ? { guild_id: guildID } : {}),
    [guildID]
  )
  const guildName = new Map(guilds.map(g => [g.id, g.name]))

  // 录音仅在说话者结束上行轨 / 离房后由 SFU 上传；定时轻量刷新便于管理员看到新段。
  useEffect(() => {
    const timer = window.setInterval(() => {
      records.reload(true)
    }, 15_000)
    return () => window.clearInterval(timer)
    // guildID 变化时 useAsyncData 会重拉；此处仅保持轮询句柄与当前 records 绑定
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guildID])

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">审计录音</CardTitle>
        <CardDescription>
          被审计频道的上行音频录制，按说话者一段会话存于主节点（离房或停麦后上传），可下载留存。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="按服务器筛选"
            placeholder="全部服务器"
            value={guildID || null}
            onChange={v => setGuildID(v ?? "")}
            options={[{ value: "", label: "全部服务器" }, ...guilds.map(g => ({ value: g.id, label: g.name }))]}
            className="w-56"
          />
          <Button variant="outline" size="sm" onClick={() => records.reload(true)}>
            刷新
          </Button>
        </div>
        {records.status === "loading" && <LoadingState rows={3} />}
        {records.status === "error" && <ErrorState message={records.error} onRetry={() => records.reload()} />}
        {records.status === "success" && (records.data?.length ?? 0) === 0 && (
          <EmptyState
            icon={RadioIcon}
            title="暂无审计录音"
            description="开启频道审计并确保 SFU 配置了上传地址后，用户结束发言/离房时会在此生成录音。"
          />
        )}
        {records.status === "success" &&
          (records.data ?? []).map(rec => (
            <div key={rec.id} className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 text-sm">
              <RadioIcon className="size-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <p className="truncate">
                  {guildName.get(rec.guild_id) ?? rec.guild_id} ·{" "}
                  <span title={rec.user_id}>{auditSpeakerLabel(rec)}</span>
                </p>
                <p className="text-xs text-muted-foreground tabular-nums">
                  {formatTime(rec.started_at)} → {formatTime(rec.ended_at)} · {formatBytes(rec.size)}
                </p>
              </div>
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  downloadAuditRecord(rec.id).catch(reason =>
                    toast.error(reason instanceof Error ? reason.message : "下载失败")
                  )
                }
              >
                <DownloadIcon data-icon="inline-start" />
                下载
              </Button>
            </div>
          ))}
      </CardContent>
    </Card>
  )
}

/** 审计录音说话者展示：优先用户名 → 显示名 → UUID 短前缀兜底 */
function auditSpeakerLabel(rec: AuditRecord): string {
  const username = rec.username?.trim()
  if (username) return username
  const display = rec.display_name?.trim()
  if (display) return display
  return `用户 ${rec.user_id.slice(0, 8)}`
}

// ---------------------------------------------------------------------------
// 通用开关行
// ---------------------------------------------------------------------------

function ToggleRow({
  title,
  description,
  checked,
  onChange,
  icon: Icon,
}: {
  title: string
  description: string
  checked: boolean
  onChange: (v: boolean) => void
  icon: typeof EyeIcon
}) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-xl border px-4 py-3">
      <div className="flex items-start gap-3">
        <Icon className="mt-0.5 size-4 text-muted-foreground" />
        <div>
          <p className="text-sm font-medium">{title}</p>
          <p className="text-xs text-muted-foreground">{description}</p>
        </div>
      </div>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={title} />
    </div>
  )
}
