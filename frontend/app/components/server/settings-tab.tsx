import { useEffect, useState } from "react"
import { useNavigate } from "react-router"
import { AlertTriangleIcon, CrownIcon, DatabaseIcon, Settings2Icon, UploadIcon, Volume2Icon } from "lucide-react"
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
  deleteGuild,
  getGuildVoicePack,
  getMessageRetention,
  getUploadLimit,
  patchGuildVoicePack,
  patchMessageRetention,
  patchUploadLimit,
  transferGuildOwnership,
  updateGuild,
  type Guild,
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
        <RetentionCard guildID={guild.id} />
        {isSystemAdmin && <UploadLimitCard guildID={guild.id} />}
      </div>
      <VoicePackCard guildID={guild.id} />
      <DangerZoneCard guild={guild} members={members} onChanged={onChanged} />
    </div>
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
