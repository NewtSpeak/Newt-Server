import { useEffect, useState, type FormEvent } from "react"
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core"
import { restrictToVerticalAxis } from "@dnd-kit/modifiers"
import { SortableContext, arrayMove, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable"
import { CSS } from "@dnd-kit/utilities"
import { GripVerticalIcon, HashIcon, MicIcon, PencilIcon, PlusIcon, Trash2Icon, Volume2Icon } from "lucide-react"
import { toast } from "sonner"

import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
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
import {
  createChannel,
  deleteChannel,
  getVoiceStageConfig,
  patchVoiceStageConfig,
  reorderChannels,
  updateChannel,
  type Channel,
  type ChannelType,
  type VoiceStageConfig,
} from "~/lib/api"

/**
 * 频道标签页：创建、行内改名/主题、删除、拖拽排序（批量保存），
 * 语音频道可打开舞台配置（模式/麦位/申请上麦/屏幕并发）。
 */
export function ChannelsTab({
  guildID,
  channels,
  status,
  error,
  reload,
}: {
  guildID: string
  channels: Channel[]
  status: "idle" | "loading" | "success" | "error"
  error: string
  reload: () => void
}) {
  const [type, setType] = useState<ChannelType>("TEXT")
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Channel | null>(null)
  const [stageChannel, setStageChannel] = useState<Channel | null>(null)

  // 本地排序快照：拖拽即时反馈，「保存排序」时整体提交。
  const [order, setOrder] = useState<Channel[]>([])
  const [orderDirty, setOrderDirty] = useState(false)
  useEffect(() => {
    setOrder([...channels].sort((a, b) => a.position - b.position || a.name.localeCompare(b.name)))
    setOrderDirty(false)
  }, [channels])

  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }))

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const name = String(new FormData(form).get("channel-name") ?? "").trim()
    if (!name) return
    setCreating(true)
    try {
      await createChannel(guildID, { name, type })
      toast.success(`频道「${name}」已创建`)
      form.reset()
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "频道创建失败")
    } finally {
      setCreating(false)
    }
  }

  function onDragEnd(event: DragEndEvent, list: Channel[]) {
    const { active, over } = event
    if (!over || active.id === over.id) return
    const oldIndex = list.findIndex(channel => channel.id === active.id)
    const newIndex = list.findIndex(channel => channel.id === over.id)
    if (oldIndex < 0 || newIndex < 0) return
    const reordered = arrayMove(list, oldIndex, newIndex)
    // 同类型内重排：把重排后的子序列写回全量顺序。
    setOrder(current => {
      const rest = current.filter(channel => channel.type !== list[0].type)
      const next = [...rest, ...reordered]
      return next
    })
    setOrderDirty(true)
  }

  async function onSaveOrder() {
    // position 全量重编：文本区从 0 开始，语音区顺延（跨类型互不干扰）。
    const texts = order.filter(channel => channel.type === "TEXT")
    const voices = order.filter(channel => channel.type === "VOICE")
    const entries = [...texts, ...voices].map((channel, index) => ({ id: channel.id, position: index }))
    try {
      await reorderChannels(guildID, entries)
      toast.success("频道排序已保存")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "排序保存失败")
    }
  }

  async function onDelete(channel: Channel) {
    if (!window.confirm(`确定删除频道「${channel.name}」？语音频道内的用户将被断开。`)) return
    try {
      await deleteChannel(channel.id)
      toast.success("频道已删除")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "频道删除失败")
    }
  }

  const textChannels = order.filter(channel => channel.type === "TEXT")
  const voiceChannels = order.filter(channel => channel.type === "VOICE")

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-center gap-2">
        <form onSubmit={onCreate} className="flex flex-wrap items-center gap-2">
          <Input name="channel-name" aria-label="频道名称" placeholder="新频道名称" required maxLength={64} className="w-56" />
          <SimpleSelect
            ariaLabel="频道类型"
            value={type}
            onChange={next => setType(next as ChannelType)}
            options={[
              { value: "TEXT", label: "文字频道" },
              { value: "VOICE", label: "语音频道" },
            ]}
            className="w-32"
          />
          <Button type="submit" disabled={creating}>
            <PlusIcon data-icon="inline-start" />
            {creating ? "创建中…" : "创建频道"}
          </Button>
        </form>
        {orderDirty && (
          <Button variant="outline" size="sm" onClick={onSaveOrder} className="ml-auto">
            保存排序
          </Button>
        )}
      </div>

      {status === "loading" && <LoadingState rows={5} />}
      {status === "error" && <ErrorState message={error} onRetry={reload} />}
      {status === "success" && channels.length === 0 && (
        <EmptyState icon={HashIcon} title="还没有频道" description="创建第一个文字或语音频道。" />
      )}
      {status === "success" && channels.length > 0 && (
        <div className="grid gap-5 lg:grid-cols-2">
          {[
            { label: "文字频道", icon: HashIcon, list: textChannels },
            { label: "语音频道", icon: Volume2Icon, list: voiceChannels },
          ].map(section => (
            <section key={section.label}>
              <h3 className="mb-2 flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
                <section.icon className="size-4" />
                {section.label}
                <Badge variant="secondary" className="tabular-nums">
                  {section.list.length}
                </Badge>
                <span className="ml-auto text-[10px] font-normal">拖动手柄可排序</span>
              </h3>
              <DndContext
                sensors={sensors}
                collisionDetection={closestCenter}
                modifiers={[restrictToVerticalAxis]}
                onDragEnd={event => onDragEnd(event, section.list)}
              >
                <SortableContext items={section.list.map(channel => channel.id)} strategy={verticalListSortingStrategy}>
                  <div className="flex flex-col gap-1.5">
                    {section.list.length === 0 && (
                      <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">暂无</p>
                    )}
                    {section.list.map(channel => (
                      <ChannelRow
                        key={channel.id}
                        channel={channel}
                        icon={section.icon}
                        onEdit={() => setEditing(channel)}
                        onDelete={() => onDelete(channel)}
                        onStage={channel.type === "VOICE" ? () => setStageChannel(channel) : undefined}
                      />
                    ))}
                  </div>
                </SortableContext>
              </DndContext>
            </section>
          ))}
        </div>
      )}

      <EditChannelDialog channel={editing} onClose={() => setEditing(null)} onSaved={reload} />
      <StageConfigDialog channel={stageChannel} onClose={() => setStageChannel(null)} />
    </div>
  )
}

function ChannelRow({
  channel,
  icon: Icon,
  onEdit,
  onDelete,
  onStage,
}: {
  channel: Channel
  icon: typeof HashIcon
  onEdit: () => void
  onDelete: () => void
  onStage?: () => void
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: channel.id })
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={`flex min-h-11 items-center gap-2 rounded-lg border bg-background px-2 py-2 transition-[background-color] hover:bg-muted/50 ${isDragging ? "z-10 opacity-80 shadow-md" : ""}`}
    >
      <button
        type="button"
        aria-label={`拖动排序 ${channel.name}`}
        {...attributes}
        {...listeners}
        className="grid size-6 shrink-0 cursor-grab place-items-center rounded text-muted-foreground hover:bg-muted active:cursor-grabbing"
      >
        <GripVerticalIcon className="size-3.5" />
      </button>
      <Icon className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{channel.name}</p>
        {channel.topic && <p className="truncate text-xs text-muted-foreground">{channel.topic}</p>}
      </div>
      <span className="shrink-0 font-mono text-[10px] text-muted-foreground">#{channel.position}</span>
      {onStage && (
        <Button variant="ghost" size="icon-xs" aria-label="舞台配置" onClick={onStage}>
          <MicIcon />
        </Button>
      )}
      <Button variant="ghost" size="icon-xs" aria-label="编辑频道" onClick={onEdit}>
        <PencilIcon />
      </Button>
      <Button variant="ghost" size="icon-xs" aria-label="删除频道" onClick={onDelete}>
        <Trash2Icon />
      </Button>
    </div>
  )
}

function EditChannelDialog({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel | null
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState("")
  const [topic, setTopic] = useState("")
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (channel) {
      setName(channel.name)
      setTopic(channel.topic ?? "")
    }
  }, [channel])

  async function onSave() {
    if (!channel) return
    setSaving(true)
    try {
      await updateChannel(channel.id, { name: name.trim(), topic })
      toast.success("频道已更新")
      onClose()
      onSaved()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "频道更新失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={channel !== null} onOpenChange={open => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑频道</DialogTitle>
          <DialogDescription>修改名称与主题（需 MANAGE_CHANNELS）。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="edit-channel-name">名称</Label>
            <Input id="edit-channel-name" value={name} onChange={event => setName(event.target.value)} maxLength={100} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="edit-channel-topic">主题（文本频道展示）</Label>
            <Input id="edit-channel-topic" value={topic} onChange={event => setTopic(event.target.value)} maxLength={1024} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onSave} disabled={saving || !name.trim()}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 语音频道舞台配置弹窗：模式 / 麦位 / 申请上麦 / 协管改模式 / 屏幕并发上限 */
function StageConfigDialog({ channel, onClose }: { channel: Channel | null; onClose: () => void }) {
  const [config, setConfig] = useState<VoiceStageConfig | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!channel) {
      setConfig(null)
      return
    }
    setLoading(true)
    getVoiceStageConfig(channel.id)
      .then(setConfig)
      .catch(reason => toast.error(reason instanceof Error ? reason.message : "读取舞台配置失败"))
      .finally(() => setLoading(false))
  }, [channel?.id])

  async function onSave() {
    if (!channel || !config) return
    setSaving(true)
    try {
      await patchVoiceStageConfig(channel.id, {
        mode: config.mode,
        max_speakers: config.max_speakers,
        request_to_speak_enabled: config.request_to_speak_enabled,
        allow_co_mod_change_mode: config.allow_co_mod_change_mode,
        ...(config.max_concurrent_screens >= 0 ? { max_concurrent_screens: config.max_concurrent_screens } : {}),
      })
      toast.success("舞台配置已保存")
      onClose()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "舞台配置保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={channel !== null} onOpenChange={open => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>舞台配置 · {channel?.name}</DialogTitle>
          <DialogDescription>语音频道模式与麦位管理；人数超过 50 时强制舞台模式。</DialogDescription>
        </DialogHeader>
        {loading && <LoadingState rows={3} />}
        {config && (
          <div className="grid gap-4">
            <div className="flex items-center gap-3">
              <Label>频道模式</Label>
              <SimpleSelect
                ariaLabel="频道模式"
                value={config.mode}
                onChange={next => setConfig({ ...config, mode: next as VoiceStageConfig["mode"] })}
                options={[
                  { value: "FREE_DISCUSSION", label: "自由讨论" },
                  { value: "STAGE", label: "舞台模式" },
                ]}
                className="w-40"
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="stage-max-speakers">最大麦位数（1–50）</Label>
              <Input
                id="stage-max-speakers"
                type="number"
                min={1}
                max={50}
                value={config.max_speakers}
                onChange={event =>
                  setConfig({ ...config, max_speakers: Math.min(50, Math.max(1, Number(event.target.value) || 1)) })
                }
                className="w-32 tabular-nums"
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="stage-max-screens">屏幕共享并发上限（-1 = 跟随默认）</Label>
              <Input
                id="stage-max-screens"
                type="number"
                min={-1}
                max={10}
                value={config.max_concurrent_screens}
                onChange={event => setConfig({ ...config, max_concurrent_screens: Number(event.target.value) })}
                className="w-32 tabular-nums"
              />
            </div>
            <label className="flex items-center gap-2.5 text-sm">
              <Switch
                checked={config.request_to_speak_enabled}
                onCheckedChange={next => setConfig({ ...config, request_to_speak_enabled: Boolean(next) })}
              />
              开启申请上麦
            </label>
            <label className="flex items-center gap-2.5 text-sm">
              <Switch
                checked={config.allow_co_mod_change_mode}
                onCheckedChange={next => setConfig({ ...config, allow_co_mod_change_mode: Boolean(next) })}
              />
              允许协管切换频道模式
            </label>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onSave} disabled={!config || saving}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
