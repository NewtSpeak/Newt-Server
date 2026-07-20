import { useEffect, useRef, useState } from "react"
import { useNavigate } from "react-router"
import {
  AlertTriangleIcon,
  ArrowDownIcon,
  ArrowUpIcon,
  CrownIcon,
  DatabaseIcon,
  ImageIcon,
  ImagesIcon,
  Settings2Icon,
  Trash2Icon,
  UploadIcon,
  Volume2Icon,
} from "lucide-react"
import { toast } from "sonner"

import { SimpleSelect } from "~/components/simple-select"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
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
import { Switch } from "~/components/ui/switch"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  addGuildBanner,
  deleteGuild,
  deleteGuildBanner,
  deleteGuildImage,
  getGuildVoicePack,
  getMessageRetention,
  getUploadLimit,
  listGuildBanners,
  patchGuildVoicePack,
  patchMessageRetention,
  patchUploadLimit,
  reorderGuildBanners,
  transferGuildOwnership,
  updateGuild,
  uploadGuildImage,
  type Guild,
  type GuildBanner,
  type MemberDisplay,
} from "~/lib/api"

/**
 * 服务器设置 Tab：基础信息（改名/简介）、策略（上传上限/消息保留/语音包）、
 * 危险区（转让所有权/删除服务器）。
 */
export function SettingsTab({
  guild,
  members,
  isSystemAdmin,
  onChanged,
}: {
  guild: Guild
  members: MemberDisplay[]
  isSystemAdmin: boolean
  onChanged: () => void
}) {
  return (
    <div className="flex flex-col gap-5">
      <OverviewCard guild={guild} onChanged={onChanged} />
      <div className="grid gap-5 lg:grid-cols-2">
        <BrandingCard guild={guild} onChanged={onChanged} />
        <ModerationPolicyCard guild={guild} isSystemAdmin={isSystemAdmin} onChanged={onChanged} />
      </div>
      <BannersCard guildID={guild.id} onChanged={onChanged} />
      <div className="grid gap-5 lg:grid-cols-2">
        <RetentionCard guildID={guild.id} />
        {isSystemAdmin && <UploadLimitCard guildID={guild.id} />}
      </div>
      <VoicePackCard guildID={guild.id} />
      <DangerZoneCard guild={guild} members={members} onChanged={onChanged} />
    </div>
  )
}

/** 服务器图标与横幅上传（docs 02 FR-13/§8-9，需 MANAGE_GUILD） */
function BrandingCard({ guild, onChanged }: { guild: Guild; onChanged: () => void }) {
  const iconInput = useRef<HTMLInputElement>(null)
  const bannerInput = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState<"icon" | "banner" | null>(null)

  async function onUpload(kind: "icon" | "banner", file: File | undefined) {
    if (!file) return
    setBusy(kind)
    try {
      await uploadGuildImage(guild.id, kind, file)
      toast.success(kind === "icon" ? "服务器图标已更新" : "服务器横幅已更新")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "上传失败")
    } finally {
      setBusy(null)
    }
  }

  async function onRemove(kind: "icon" | "banner") {
    setBusy(kind)
    try {
      await deleteGuildImage(guild.id, kind)
      toast.success(kind === "icon" ? "服务器图标已移除" : "服务器横幅已移除")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "移除失败")
    } finally {
      setBusy(null)
    }
  }

  const rows: { kind: "icon" | "banner"; label: string; url?: string; input: typeof iconInput }[] = [
    { kind: "icon", label: "服务器图标", url: guild.icon_url, input: iconInput },
    { kind: "banner", label: "服务器横幅", url: guild.banner_url, input: bannerInput },
  ]

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ImageIcon className="size-4" />
          图标与横幅
        </CardTitle>
        <CardDescription>PNG/JPEG/WebP/GIF，单张不超过 8MB；图标显示在服务器栏，横幅显示在邀请页与频道顶部。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {rows.map(row => (
          <div key={row.kind} className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3">
            {row.url ? (
              <img
                src={row.url}
                alt={row.label}
                className={row.kind === "icon" ? "size-10 rounded-full border object-cover" : "h-10 w-24 rounded-md border object-cover"}
              />
            ) : (
              <div className={`grid place-items-center border border-dashed text-[10px] text-muted-foreground ${row.kind === "icon" ? "size-10 rounded-full" : "h-10 w-24 rounded-md"}`}>
                未设置
              </div>
            )}
            <p className="min-w-0 flex-1 text-sm font-medium">{row.label}</p>
            <input
              ref={row.input}
              type="file"
              accept="image/png,image/jpeg,image/webp,image/gif"
              className="hidden"
              onChange={event => {
                void onUpload(row.kind, event.target.files?.[0])
                event.target.value = ""
              }}
            />
            <Button variant="outline" size="sm" disabled={busy === row.kind} onClick={() => row.input.current?.click()}>
              {busy === row.kind ? "处理中…" : "上传"}
            </Button>
            {row.url && (
              <Button variant="ghost" size="sm" disabled={busy === row.kind} onClick={() => onRemove(row.kind)}>
                移除
              </Button>
            )}
          </div>
        ))}
      </CardContent>
    </Card>
  )
}

/**
 * 服务器多 banner 图库（docs 协议/服务器外观资产.md）：上传（追加末尾）、
 * 删除、上下移重排序（全量有序 ID 数组提交）。详情页头部按 position 轮播展示。
 */
function BannersCard({ guildID, onChanged }: { guildID: string; onChanged: () => void }) {
  const data = useAsyncData(() => listGuildBanners(guildID), [guildID])
  const fileInput = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)

  const banners = data.data?.banners ?? []
  const limit = data.data?.limit ?? 10

  async function run(action: () => Promise<unknown>, successText: string) {
    setBusy(true)
    try {
      await action()
      toast.success(successText)
      data.reload(true)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "操作失败")
    } finally {
      setBusy(false)
    }
  }

  function onMove(index: number, delta: -1 | 1) {
    const target = index + delta
    if (target < 0 || target >= banners.length) return
    const ids = banners.map(banner => banner.id)
    ;[ids[index], ids[target]] = [ids[target], ids[index]]
    void run(() => reorderGuildBanners(guildID, ids), "banner 顺序已保存")
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ImagesIcon className="size-4" />
          Banner 图库
        </CardTitle>
        <CardDescription>
          详情页顶部按顺序轮播展示（第 1 张为封面）；PNG/JPEG/WebP/GIF，单张不超过 8MB，最多 {limit} 张。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {banners.length === 0 && data.status === "success" && (
          <p className="rounded-xl border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
            还没有 banner，上传第一张作为服务器封面。
          </p>
        )}
        {banners.map((banner: GuildBanner, index: number) => (
          <div key={banner.id} className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3">
            <img src={banner.url} alt={`banner ${index + 1}`} className="h-12 w-28 rounded-md border object-cover" />
            <p className="min-w-0 flex-1 text-sm text-muted-foreground">
              第 <span className="tabular-nums">{index + 1}</span> 张{index === 0 && "（封面）"}
            </p>
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="上移"
                disabled={busy || index === 0}
                onClick={() => onMove(index, -1)}
              >
                <ArrowUpIcon />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="下移"
                disabled={busy || index === banners.length - 1}
                onClick={() => onMove(index, 1)}
              >
                <ArrowDownIcon />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="删除该 banner"
                className="text-destructive hover:text-destructive"
                disabled={busy}
                onClick={() => run(() => deleteGuildBanner(guildID, banner.id), "banner 已删除")}
              >
                <Trash2Icon />
              </Button>
            </div>
          </div>
        ))}
        <input
          ref={fileInput}
          type="file"
          accept="image/png,image/jpeg,image/webp,image/gif"
          className="hidden"
          onChange={event => {
            const file = event.target.files?.[0]
            if (file) void run(() => addGuildBanner(guildID, file), "banner 已上传")
            event.target.value = ""
          }}
        />
        <div className="flex items-center justify-between">
          <p className="text-xs text-muted-foreground tabular-nums">
            {banners.length} / {limit}
          </p>
          <Button
            variant="outline"
            size="sm"
            disabled={busy || banners.length >= limit || data.status !== "success"}
            onClick={() => fileInput.current?.click()}
          >
            <UploadIcon data-icon="inline-start" />
            {busy ? "处理中…" : "上传 banner"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

/** 治理策略：受限徽章展示开关（MANAGE_GUILD）与 reason 强制开关（系统管理员） */
function ModerationPolicyCard({
  guild,
  isSystemAdmin,
  onChanged,
}: {
  guild: Guild
  isSystemAdmin: boolean
  onChanged: () => void
}) {
  const [busyKey, setBusyKey] = useState<string | null>(null)

  async function onToggle(key: "restriction_badge_visible" | "restriction_reason_required", value: boolean) {
    setBusyKey(key)
    try {
      await updateGuild(guild.id, { [key]: value })
      toast.success("治理策略已保存")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setBusyKey(null)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Settings2Icon className="size-4" />
          治理策略
        </CardTitle>
        <CardDescription>受限徽章与 Restriction reason 政策（docs 08 AM.4 / AI.2）。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <label className="flex items-start gap-2.5 rounded-xl border px-4 py-3 text-sm">
          <Switch
            checked={guild.restriction_badge_visible ?? true}
            disabled={busyKey !== null}
            onCheckedChange={next => onToggle("restriction_badge_visible", Boolean(next))}
            className="mt-0.5"
          />
          <span>
            <span className="block leading-5">成员列表显示「受限」徽章</span>
            <span className="block text-xs leading-4 text-muted-foreground">关闭后客户端不渲染受限标识（脱敏事件仍照常下发）。</span>
          </span>
        </label>
        <label className={`flex items-start gap-2.5 rounded-xl border px-4 py-3 text-sm ${isSystemAdmin ? "" : "opacity-60"}`}>
          <Switch
            checked={guild.restriction_reason_required ?? true}
            disabled={!isSystemAdmin || busyKey !== null}
            onCheckedChange={next => onToggle("restriction_reason_required", Boolean(next))}
            className="mt-0.5"
          />
          <span>
            <span className="block leading-5">创建限制必须填写原因</span>
            <span className="block text-xs leading-4 text-muted-foreground">仅系统管理员可修改；关闭后 reason 变为可选。</span>
          </span>
        </label>
      </CardContent>
    </Card>
  )
}

function OverviewCard({ guild, onChanged }: { guild: Guild; onChanged: () => void }) {
  const [name, setName] = useState(guild.name)
  const [description, setDescription] = useState(guild.description ?? "")
  const [saving, setSaving] = useState(false)
  useEffect(() => {
    setName(guild.name)
    setDescription(guild.description ?? "")
  }, [guild.id, guild.name, guild.description])
  const dirty = name !== guild.name || description !== (guild.description ?? "")

  async function onSave() {
    setSaving(true)
    try {
      await updateGuild(guild.id, { name: name.trim(), description })
      toast.success("服务器信息已保存")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Settings2Icon className="size-4" />
          基础信息
        </CardTitle>
        <CardDescription>名称与简介（需 MANAGE_GUILD）；简介会出现在邀请落地页之外的服务器概览场景。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-2 sm:max-w-md">
          <Label htmlFor="guild-name">服务器名称</Label>
          <Input id="guild-name" value={name} onChange={event => setName(event.target.value)} minLength={2} maxLength={100} />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="guild-description">服务器简介</Label>
          <textarea
            id="guild-description"
            value={description}
            onChange={event => setDescription(event.target.value)}
            rows={3}
            maxLength={1024}
            className="w-full rounded-lg border bg-transparent px-3 py-2 text-sm transition-[border-color,box-shadow] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none"
          />
        </div>
        <div className="flex justify-end">
          <Button size="sm" onClick={onSave} disabled={!dirty || saving || name.trim().length < 2}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

const GB = 1024 * 1024

function UploadLimitCard({ guildID }: { guildID: string }) {
  const limit = useAsyncData(() => getUploadLimit(guildID), [guildID])
  const [mb, setMb] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (limit.data) setMb(limit.data.upload_limit_bytes > 0 ? Math.round(limit.data.upload_limit_bytes / GB) : 0)
  }, [limit.data])

  async function onSave() {
    if (mb === null) return
    setSaving(true)
    try {
      await patchUploadLimit(guildID, mb > 0 ? mb * GB : 0)
      toast.success("上传上限已保存")
      limit.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  const effectiveMB = limit.data ? Math.round(limit.data.effective_bytes / GB) : null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <UploadIcon className="size-4" />
          附件上传上限
        </CardTitle>
        <CardDescription>单文件大小上限（系统管理员）；0 表示跟随平台默认（25 MB）。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-end gap-3">
        <div className="grid gap-2">
          <Label htmlFor="upload-limit">上限（MB，0 = 默认）</Label>
          <Input
            id="upload-limit"
            type="number"
            min={0}
            max={2048}
            value={mb ?? ""}
            onChange={event => setMb(Math.max(0, Number(event.target.value) || 0))}
            className="w-36 tabular-nums"
            disabled={limit.status !== "success"}
          />
        </div>
        <p className="pb-2 text-xs text-muted-foreground">
          当前生效：<span className="tabular-nums">{effectiveMB ?? "—"}</span> MB
        </p>
        <Button size="sm" onClick={onSave} disabled={saving || mb === null} className="ml-auto">
          {saving ? "保存中…" : "保存"}
        </Button>
      </CardContent>
    </Card>
  )
}

function RetentionCard({ guildID }: { guildID: string }) {
  const retention = useAsyncData(() => getMessageRetention(guildID), [guildID])
  const [days, setDays] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (retention.data) setDays(retention.data.retention_days)
  }, [retention.data])

  async function onSave() {
    if (days === null) return
    setSaving(true)
    try {
      await patchMessageRetention(guildID, days)
      toast.success("消息保留策略已保存")
      retention.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DatabaseIcon className="size-4" />
          消息保留策略
        </CardTitle>
        <CardDescription>超过天数的消息（含附件/编辑历史/反应）由后台任务硬删；0 表示永久保留。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-end gap-3">
        <div className="grid gap-2">
          <Label htmlFor="retention-days">保留天数（0 = 永久）</Label>
          <Input
            id="retention-days"
            type="number"
            min={0}
            max={3650}
            value={days ?? ""}
            onChange={event => setDays(Math.max(0, Number(event.target.value) || 0))}
            className="w-36 tabular-nums"
            disabled={retention.status !== "success"}
          />
        </div>
        <Button size="sm" onClick={onSave} disabled={saving || days === null} className="ml-auto">
          {saving ? "保存中…" : "保存"}
        </Button>
      </CardContent>
    </Card>
  )
}

const VOICE_PACK_SCOPES = [
  { value: "SAME_CHANNEL", label: "仅同频道成员" },
  { value: "GUILD_ONLINE", label: "全服在线成员" },
]
const VOICE_PACK_TRIGGERS = [
  { value: "FIRST_GUILD_JOIN", label: "本服首次进房" },
  { value: "CHANNEL_JOIN", label: "每次进入频道" },
]

function VoicePackCard({ guildID }: { guildID: string }) {
  const pack = useAsyncData(() => getGuildVoicePack(guildID), [guildID])
  const [enabled, setEnabled] = useState(false)
  const [audioURL, setAudioURL] = useState("")
  const [scope, setScope] = useState("SAME_CHANNEL")
  const [trigger, setTrigger] = useState("FIRST_GUILD_JOIN")
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (pack.data) {
      setEnabled(pack.data.enabled)
      setAudioURL(pack.data.audio_url)
      setScope(pack.data.scope || "SAME_CHANNEL")
      setTrigger(pack.data.trigger || "FIRST_GUILD_JOIN")
    }
  }, [pack.data])

  async function onSave() {
    setSaving(true)
    try {
      await patchGuildVoicePack(guildID, { enabled, audio_url: audioURL, scope, trigger })
      toast.success("语音包配置已保存")
      pack.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Volume2Icon className="size-4" />
          进房语音包
        </CardTitle>
        <CardDescription>成员进入语音频道时播放的提示音（需 MANAGE_GUILD）；音频 URL 支持外链。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-6">
          <label className="flex items-center gap-2.5 text-sm">
            <Switch checked={enabled} onCheckedChange={next => setEnabled(Boolean(next))} aria-label="启用语音包" />
            启用语音包
          </label>
          <div className="flex items-center gap-2">
            <Label>播放范围</Label>
            <SimpleSelect ariaLabel="播放范围" value={scope} onChange={setScope} options={VOICE_PACK_SCOPES} className="w-40" />
          </div>
          <div className="flex items-center gap-2">
            <Label>触发时机</Label>
            <SimpleSelect ariaLabel="触发时机" value={trigger} onChange={setTrigger} options={VOICE_PACK_TRIGGERS} className="w-40" />
          </div>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="voice-pack-url">音频 URL</Label>
          <Input
            id="voice-pack-url"
            value={audioURL}
            onChange={event => setAudioURL(event.target.value)}
            placeholder="https://…/join.ogg"
            maxLength={1024}
          />
        </div>
        <div className="flex justify-end">
          <Button size="sm" onClick={onSave} disabled={saving || pack.status !== "success"}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function DangerZoneCard({
  guild,
  members,
  onChanged,
}: {
  guild: Guild
  members: MemberDisplay[]
  onChanged: () => void
}) {
  const navigate = useNavigate()
  const [transferOpen, setTransferOpen] = useState(false)
  const [transferTarget, setTransferTarget] = useState<string | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [confirmName, setConfirmName] = useState("")
  const [busy, setBusy] = useState(false)

  const candidates = members.filter(member => member.user_id !== guild.owner_user_id && !member.is_bot)

  async function onTransfer() {
    if (!transferTarget) return
    setBusy(true)
    try {
      await transferGuildOwnership(guild.id, transferTarget)
      toast.success("所有权已转让")
      setTransferOpen(false)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "转让失败")
    } finally {
      setBusy(false)
    }
  }

  async function onDelete() {
    setBusy(true)
    try {
      await deleteGuild(guild.id, confirmName)
      toast.success(`服务器「${guild.name}」已删除`)
      setDeleteOpen(false)
      onChanged()
      navigate("/servers")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "删除失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card className="border-destructive/40">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base text-destructive">
          <AlertTriangleIcon className="size-4" />
          危险区
        </CardTitle>
        <CardDescription>以下操作仅服务器所有者可执行且不可撤销，请谨慎操作。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">转让所有权</p>
            <p className="text-xs text-muted-foreground">新所有者必须是本服成员；转让后你保留成员身份与既有角色。</p>
          </div>
          <Button variant="outline" size="sm" onClick={() => setTransferOpen(true)} disabled={candidates.length === 0}>
            <CrownIcon data-icon="inline-start" />
            转让
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-3 rounded-xl border border-destructive/30 px-4 py-3">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">删除服务器</p>
            <p className="text-xs text-muted-foreground">删除全部频道/角色/成员关系并断开语音会话；需输入服务器名称确认。</p>
          </div>
          <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
            删除服务器
          </Button>
        </div>
      </CardContent>

      <Dialog open={transferOpen} onOpenChange={setTransferOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>转让所有权</DialogTitle>
            <DialogDescription>选择新的所有者（机器人与当前所有者除外）。</DialogDescription>
          </DialogHeader>
          <SimpleSelect
            ariaLabel="选择新所有者"
            placeholder="选择成员"
            value={transferTarget}
            onChange={setTransferTarget}
            options={candidates.map(member => ({
              value: member.user_id,
              label: member.nickname || member.username,
            }))}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setTransferOpen(false)}>
              取消
            </Button>
            <Button onClick={onTransfer} disabled={!transferTarget || busy}>
              {busy ? "转让中…" : "确认转让"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>删除服务器</DialogTitle>
            <DialogDescription>
              此操作不可撤销。输入服务器名称「{guild.name}」以确认删除。
            </DialogDescription>
          </DialogHeader>
          <Input
            aria-label="输入服务器名称确认"
            value={confirmName}
            onChange={event => setConfirmName(event.target.value)}
            placeholder={guild.name}
            autoFocus
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>
              取消
            </Button>
            <Button variant="destructive" onClick={onDelete} disabled={confirmName !== guild.name || busy}>
              {busy ? "删除中…" : "永久删除"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
