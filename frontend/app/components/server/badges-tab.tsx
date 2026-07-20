import { useEffect, useState, type FormEvent } from "react"
import { AwardIcon, PlusIcon, Trash2Icon, UserPlusIcon, XIcon } from "lucide-react"
import { toast } from "sonner"

import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
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
import { useAsyncData } from "~/hooks/use-async-data"
import {
  createBadge,
  deleteBadge,
  grantBadge,
  listBadgeGrants,
  listBadges,
  memberName,
  revokeBadge,
  updateBadge,
  type BadgeGrant,
  type GuildBadge,
  type GuildMember,
} from "~/lib/api"
import { cn } from "~/lib/utils"

type ExpiryMode = "permanent" | "days" | "until"

function formatExpiry(expiresAt: string | null | undefined) {
  if (!expiresAt) return "永久"
  const date = new Date(expiresAt)
  return Number.isNaN(date.getTime()) ? "永久" : `${date.toLocaleString()} 到期`
}

/** 徽章标签页：定义管理（名称/图标/颜色）+ 分配（永久 / 天数 / 截止日期） */
export function BadgesTab({ guildID, members }: { guildID: string; members: GuildMember[] }) {
  const badges = useAsyncData<GuildBadge[]>(guildID ? () => listBadges(guildID) : null, [guildID])
  const [selectedID, setSelectedID] = useState<string | null>(null)
  const list = badges.data ?? []
  const selected = list.find(badge => badge.id === selectedID) ?? list[0] ?? null

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<GuildBadge | null>(null)
  const [grantOpen, setGrantOpen] = useState(false)

  const grants = useAsyncData<BadgeGrant[]>(
    selected ? () => listBadgeGrants(guildID, selected.id) : null,
    [guildID, selected?.id]
  )

  async function onSaveBadge(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const body = {
      name: String(form.get("badge-name") ?? "").trim(),
      description: String(form.get("badge-description") ?? "").trim(),
      emoji: String(form.get("badge-emoji") ?? "").trim(),
      icon_url: String(form.get("badge-icon-url") ?? "").trim(),
      color: String(form.get("badge-color") ?? "").trim(),
    }
    if (!body.name) return
    try {
      if (editing) {
        await updateBadge(guildID, editing.id, body)
        toast.success(`徽章「${body.name}」已更新`)
      } else {
        const created = await createBadge(guildID, body)
        setSelectedID(created.id)
        toast.success(`徽章「${body.name}」已创建`)
      }
      setEditorOpen(false)
      badges.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "徽章保存失败")
    }
  }

  async function onDeleteBadge(badge: GuildBadge) {
    if (!window.confirm(`确定删除徽章「${badge.name}」？所有授予记录将一并删除。`)) return
    try {
      await deleteBadge(guildID, badge.id)
      toast.success("徽章已删除")
      if (selectedID === badge.id) setSelectedID(null)
      badges.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "徽章删除失败")
    }
  }

  async function onRevoke(grant: BadgeGrant) {
    if (!selected) return
    try {
      await revokeBadge(guildID, selected.id, grant.user_id)
      toast.success("徽章已回收")
      grants.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "徽章回收失败")
    }
  }

  if (badges.status === "loading") return <LoadingState rows={4} />
  if (badges.status === "error") return <ErrorState message={badges.error} onRetry={() => badges.reload()} />

  return (
    <div className="grid gap-5 lg:grid-cols-[260px_1fr]">
      <aside className="flex flex-col gap-2">
        <Button
          variant="outline"
          size="sm"
          className="justify-start"
          onClick={() => {
            setEditing(null)
            setEditorOpen(true)
          }}
        >
          <PlusIcon data-icon="inline-start" />
          新建徽章
        </Button>
        <div className="flex flex-col gap-1" role="listbox" aria-label="徽章列表">
          {list.map((badge, index) => (
            <button
              key={badge.id}
              type="button"
              role="option"
              aria-selected={selected?.id === badge.id}
              onClick={() => setSelectedID(badge.id)}
              style={{ "--stagger-index": index } as React.CSSProperties}
              className={cn(
                "anim-item flex min-h-10 items-center gap-2 rounded-lg border px-3 py-2 text-left text-sm transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none",
                selected?.id === badge.id ? "border-primary/50 bg-primary/5 font-medium" : "border-transparent hover:bg-muted/60"
              )}
            >
              {badge.icon_url ? (
                <img src={badge.icon_url} alt="" className="size-4 shrink-0 rounded-full object-cover" />
              ) : badge.emoji ? (
                <span aria-hidden>{badge.emoji}</span>
              ) : (
                <AwardIcon className="size-4 shrink-0" style={badge.color ? { color: badge.color } : undefined} />
              )}
              <span className="truncate" style={badge.color ? { color: badge.color } : undefined}>
                {badge.name}
              </span>
            </button>
          ))}
          {list.length === 0 && (
            <EmptyState icon={AwardIcon} title="暂无徽章" description="创建徽章后即可分配给成员。" className="py-8" />
          )}
        </div>
      </aside>

      {selected ? (
        <section key={selected.id} className="t-text-swap flex min-w-0 flex-col gap-4">
          <div className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3">
            <span
              className="inline-flex size-10 items-center justify-center rounded-full border text-lg"
              style={selected.color ? { borderColor: `${selected.color}66`, backgroundColor: `${selected.color}14` } : undefined}
            >
              {selected.icon_url ? (
                <img src={selected.icon_url} alt="" className="size-6 rounded-full object-cover" />
              ) : (
                selected.emoji || <AwardIcon className="size-5" style={selected.color ? { color: selected.color } : undefined} />
              )}
            </span>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-semibold" style={selected.color ? { color: selected.color } : undefined}>
                {selected.name}
              </p>
              <p className="truncate text-xs text-muted-foreground">{selected.description || "暂无描述"}</p>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setEditing(selected)
                setEditorOpen(true)
              }}
            >
              编辑
            </Button>
            <Button variant="destructive" size="icon-sm" aria-label="删除徽章" onClick={() => onDeleteBadge(selected)}>
              <Trash2Icon />
            </Button>
          </div>

          <div className="flex items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              已授予 <span className="tabular-nums">{(grants.data ?? []).length}</span> 名成员
            </p>
            <Button size="sm" onClick={() => setGrantOpen(true)}>
              <UserPlusIcon data-icon="inline-start" />
              分配徽章
            </Button>
          </div>

          {grants.status === "loading" && <LoadingState rows={3} />}
          {grants.status === "error" && <ErrorState message={grants.error} onRetry={() => grants.reload()} />}
          {grants.status === "success" && (grants.data ?? []).length === 0 && (
            <EmptyState title="尚未授予任何成员" description="点击「分配徽章」选择成员与有效期。" className="py-8" />
          )}
          {grants.status === "success" &&
            (grants.data ?? []).map((grant, index) => (
              <div
                key={grant.id}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex items-center gap-3 rounded-xl border px-4 py-2.5"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{grant.username ?? grant.user_id}</p>
                  <p className="truncate font-mono text-xs text-muted-foreground">{grant.user_id}</p>
                </div>
                <span className="text-xs text-muted-foreground">{formatExpiry(grant.expires_at)}</span>
                <Button variant="ghost" size="icon-sm" aria-label="回收徽章" onClick={() => onRevoke(grant)}>
                  <XIcon />
                </Button>
              </div>
            ))}

          <GrantDialog
            open={grantOpen}
            onOpenChange={setGrantOpen}
            guildID={guildID}
            badge={selected}
            members={members}
            onGranted={() => grants.reload(true)}
          />
        </section>
      ) : (
        <EmptyState title="选择或创建徽章" description="徽章可随时分配给成员，支持永久、天数与截止日期。" />
      )}

      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing ? `编辑徽章「${editing.name}」` : "新建徽章"}</DialogTitle>
            <DialogDescription>徽章会展示在成员信息里；图标可用 emoji 或图片 URL。</DialogDescription>
          </DialogHeader>
          <form key={editing?.id ?? "new"} onSubmit={onSaveBadge} className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="badge-name">名称</Label>
              <Input id="badge-name" name="badge-name" defaultValue={editing?.name ?? ""} required maxLength={64} autoFocus />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="badge-description">描述</Label>
              <Input id="badge-description" name="badge-description" defaultValue={editing?.description ?? ""} maxLength={255} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-2">
                <Label htmlFor="badge-emoji">Emoji 图标</Label>
                <Input id="badge-emoji" name="badge-emoji" defaultValue={editing?.emoji ?? ""} placeholder="🏆" maxLength={32} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="badge-color">主题色</Label>
                <div className="flex items-center gap-2">
                  <input
                    type="color"
                    aria-label="选择主题色"
                    defaultValue={editing?.color || "#f59e0b"}
                    onChange={event => {
                      const input = document.getElementById("badge-color") as HTMLInputElement | null
                      if (input) input.value = event.target.value
                    }}
                    className="size-9 cursor-pointer rounded-md border bg-transparent p-0.5"
                  />
                  <Input
                    id="badge-color"
                    name="badge-color"
                    defaultValue={editing?.color ?? ""}
                    placeholder="#f59e0b"
                    maxLength={7}
                    className="font-mono"
                  />
                </div>
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="badge-icon-url">图片图标 URL（可选，优先于 emoji）</Label>
              <Input id="badge-icon-url" name="badge-icon-url" defaultValue={editing?.icon_url ?? ""} maxLength={512} />
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditorOpen(false)}>
                取消
              </Button>
              <Button type="submit">{editing ? "保存" : "创建"}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** 分配徽章弹窗：成员选择 + 有效期（永久 / 天数 / 截止日期） */
function GrantDialog({
  open,
  onOpenChange,
  guildID,
  badge,
  members,
  onGranted,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  guildID: string
  badge: GuildBadge
  members: GuildMember[]
  onGranted: () => void
}) {
  const [userID, setUserID] = useState<string | null>(null)
  const [mode, setMode] = useState<ExpiryMode>("permanent")
  const [days, setDays] = useState(30)
  const [until, setUntil] = useState("")
  const [granting, setGranting] = useState(false)

  useEffect(() => {
    if (open) {
      setUserID(null)
      setMode("permanent")
      setDays(30)
      setUntil("")
    }
  }, [open])

  async function onGrant() {
    if (!userID) return
    setGranting(true)
    try {
      const body: { days?: number; until?: string } = {}
      if (mode === "days") body.days = days
      if (mode === "until") {
        if (!until) {
          toast.error("请选择截止日期")
          setGranting(false)
          return
        }
        body.until = new Date(until).toISOString()
      }
      await grantBadge(guildID, badge.id, userID, body)
      toast.success("徽章已分配")
      onOpenChange(false)
      onGranted()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "徽章分配失败")
    } finally {
      setGranting(false)
    }
  }

  const modeOptions: { value: ExpiryMode; label: string; note: string }[] = [
    { value: "permanent", label: "永久", note: "手动回收前一直有效" },
    { value: "days", label: "按天数", note: "自授予起 N 天" },
    { value: "until", label: "到某天为止", note: "指定截止时间" },
  ]

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>分配「{badge.name}」</DialogTitle>
          <DialogDescription>选择成员与有效期；重复分配会覆盖原有效期。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label>成员</Label>
            <SimpleSelect
              ariaLabel="选择成员"
              placeholder="选择成员"
              value={userID}
              onChange={setUserID}
              options={members.map(member => ({ value: member.user_id, label: memberName(member) }))}
            />
          </div>
          <div className="grid gap-2">
            <Label>有效期</Label>
            <div role="radiogroup" aria-label="有效期" className="grid grid-cols-3 gap-2">
              {modeOptions.map(option => (
                <button
                  key={option.value}
                  type="button"
                  role="radio"
                  aria-checked={mode === option.value}
                  onClick={() => setMode(option.value)}
                  className={cn(
                    "flex flex-col items-center gap-0.5 rounded-xl border px-2 py-2.5 transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none",
                    mode === option.value ? "border-primary/60 bg-primary/5" : "hover:bg-muted/50"
                  )}
                >
                  <span className="text-sm font-medium">{option.label}</span>
                  <span className="text-[10px] text-muted-foreground">{option.note}</span>
                </button>
              ))}
            </div>
          </div>
          {mode === "days" && (
            <div className="grid gap-2">
              <Label htmlFor="grant-days">有效天数</Label>
              <Input
                id="grant-days"
                type="number"
                min={1}
                max={3650}
                value={days}
                onChange={event => setDays(Math.max(1, Number(event.target.value) || 1))}
                className="w-32 tabular-nums"
              />
            </div>
          )}
          {mode === "until" && (
            <div className="grid gap-2">
              <Label htmlFor="grant-until">截止时间</Label>
              <Input
                id="grant-until"
                type="datetime-local"
                value={until}
                onChange={event => setUntil(event.target.value)}
                className="w-60"
              />
            </div>
          )}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={onGrant} disabled={!userID || granting}>
            {granting ? "分配中…" : "分配"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
