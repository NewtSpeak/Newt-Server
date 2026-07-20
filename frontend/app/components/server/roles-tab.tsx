import { useEffect, useState, type FormEvent } from "react"
import { PaletteIcon, PlusIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react"
import { toast } from "sonner"

import { PermissionMatrix } from "~/components/server/permission-matrix"
import { RoleStyleEditor } from "~/components/server/role-style-editor"
import { StyledName } from "~/components/server/styled-name"
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
  DialogTrigger,
} from "~/components/ui/dialog"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { Switch } from "~/components/ui/switch"
import {
  createRole,
  deleteRole,
  getRoleFeatureBits,
  parseRoleStyle,
  patchRoleFeatureBits,
  updateRole,
  updateRoleStyle,
  type Role,
  type RoleFeatureBits,
  type RoleStyle,
} from "~/lib/api"
import { describePermissions } from "~/lib/permissions"
import { cn } from "~/lib/utils"

export function RolesTab({
  guildID,
  roles,
  status,
  error,
  reload,
}: {
  guildID: string
  roles: Role[]
  status: "idle" | "loading" | "success" | "error"
  error: string
  reload: () => void
}) {
  const sorted = [...roles].sort((a, b) => b.position - a.position)
  const [selectedID, setSelectedID] = useState<string | null>(null)
  const selected = sorted.find(role => role.id === selectedID) ?? sorted[0] ?? null

  const [draftMask, setDraftMask] = useState(0)
  const [draftName, setDraftName] = useState("")
  const [draftPosition, setDraftPosition] = useState(0)
  const [draftColor, setDraftColor] = useState("")
  const [draftHoist, setDraftHoist] = useState(false)
  const [draftMentionable, setDraftMentionable] = useState(false)
  const [saving, setSaving] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)

  useEffect(() => {
    if (selected) {
      setDraftMask(selected.permissions)
      setDraftName(selected.name)
      setDraftPosition(selected.position)
      setDraftColor(selected.color ?? "")
      setDraftHoist(Boolean(selected.hoist))
      setDraftMentionable(Boolean(selected.mentionable))
    }
  }, [selected?.id, selected?.permissions, selected?.name, selected?.position, selected?.color, selected?.hoist, selected?.mentionable])

  const dirty =
    selected !== null &&
    (draftMask !== selected.permissions ||
      draftName !== selected.name ||
      draftPosition !== selected.position ||
      draftColor !== (selected.color ?? "") ||
      draftHoist !== Boolean(selected.hoist) ||
      draftMentionable !== Boolean(selected.mentionable))

  async function onSave() {
    if (!selected) return
    setSaving(true)
    try {
      await updateRole(guildID, selected.id, {
        name: draftName,
        position: draftPosition,
        permissions: draftMask,
        color: draftColor,
        hoist: draftHoist,
        mentionable: draftMentionable,
      })
      toast.success(`角色「${draftName}」已保存`)
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色保存失败")
    } finally {
      setSaving(false)
    }
  }

  async function onDelete(role: Role) {
    if (!window.confirm(`确定删除角色「${role.name}」？成员绑定与频道覆盖将一并清理。`)) return
    try {
      await deleteRole(guildID, role.id)
      toast.success(`角色「${role.name}」已删除`)
      setSelectedID(null)
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色删除失败")
    }
  }

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const name = String(form.get("role-name") ?? "").trim()
    const position = Number(form.get("role-position") ?? 1)
    if (!name) return
    setCreating(true)
    try {
      const role = await createRole(guildID, { name, position, permissions: 0 })
      toast.success(`角色「${name}」已创建，权限默认为空`)
      setCreateOpen(false)
      setSelectedID(role.id)
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色创建失败")
    } finally {
      setCreating(false)
    }
  }

  if (status === "loading") return <LoadingState rows={5} />
  if (status === "error") return <ErrorState message={error} onRetry={reload} />

  return (
    <div className="grid gap-5 lg:grid-cols-[240px_1fr]">
      <aside className="flex flex-col gap-2">
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger render={<Button variant="outline" size="sm" className="justify-start" />}>
            <PlusIcon data-icon="inline-start" />
            新建角色
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>新建角色</DialogTitle>
              <DialogDescription>新角色权限默认为 0，创建后在右侧勾选权限位。</DialogDescription>
            </DialogHeader>
            <form onSubmit={onCreate} className="grid gap-4">
              <div className="grid gap-2">
                <Label htmlFor="role-name">角色名称</Label>
                <Input id="role-name" name="role-name" placeholder="如：协管员" required maxLength={64} autoFocus />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="role-position">层级（position，越大越高）</Label>
                <Input id="role-position" name="role-position" type="number" min={1} max={999} defaultValue={1} required />
              </div>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                  取消
                </Button>
                <Button type="submit" disabled={creating}>
                  {creating ? "创建中…" : "创建"}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>

        <div className="flex flex-col gap-1" role="listbox" aria-label="角色列表">
          {sorted.map((role, index) => (
            <button
              key={role.id}
              type="button"
              role="option"
              aria-selected={selected?.id === role.id}
              onClick={() => setSelectedID(role.id)}
              style={{ "--stagger-index": index } as React.CSSProperties}
              className={cn(
                "anim-item flex min-h-10 items-center gap-2 rounded-lg border px-3 py-2 text-left text-sm transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none",
                selected?.id === role.id ? "border-primary/50 bg-primary/5 font-medium" : "border-transparent hover:bg-muted/60"
              )}
            >
              <ShieldCheckIcon
                className="size-4 shrink-0 text-muted-foreground"
                style={role.color ? { color: role.color } : undefined}
              />
              <StyledName nameStyle={parseRoleStyle(role.style)} className="truncate">
                {role.name}
              </StyledName>
              {role.hoist && (
                <Badge variant="outline" className="shrink-0 px-1 text-[9px]">
                  分组
                </Badge>
              )}
              <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">P{role.position}</span>
            </button>
          ))}
          {sorted.length === 0 && <EmptyState title="暂无角色" description="创建第一个自定义角色。" className="py-8" />}
        </div>
      </aside>

      {selected ? (
        <section key={selected.id} className="t-text-swap flex min-w-0 flex-col gap-4">
          <div className="flex flex-wrap items-end gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="edit-role-name">角色名称</Label>
              <Input
                id="edit-role-name"
                value={draftName}
                onChange={event => setDraftName(event.target.value)}
                disabled={selected.is_everyone}
                className="w-52"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="edit-role-position">层级</Label>
              <Input
                id="edit-role-position"
                type="number"
                min={0}
                max={999}
                value={draftPosition}
                onChange={event => setDraftPosition(Number(event.target.value))}
                disabled={selected.is_everyone}
                className="w-24 tabular-nums"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="edit-role-color">颜色</Label>
              <div className="flex items-center gap-1.5">
                <input
                  id="edit-role-color"
                  type="color"
                  aria-label="角色颜色"
                  value={draftColor || "#99aab5"}
                  onChange={event => setDraftColor(event.target.value)}
                  disabled={selected.is_everyone}
                  className="h-9 w-12 cursor-pointer rounded-md border bg-background p-1"
                />
                {draftColor && !selected.is_everyone && (
                  <Button variant="ghost" size="sm" onClick={() => setDraftColor("")}>
                    清除
                  </Button>
                )}
              </div>
            </div>
            {selected.is_everyone && <Badge variant="outline">@everyone · 权限基线</Badge>}
            {!selected.is_everyone && (
              <div className="flex items-center gap-4 pb-1.5">
                <label className="flex items-center gap-2 text-sm">
                  <Switch checked={draftHoist} onCheckedChange={next => setDraftHoist(Boolean(next))} />
                  成员列表单独分组
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <Switch checked={draftMentionable} onCheckedChange={next => setDraftMentionable(Boolean(next))} />
                  允许任何人 @提及
                </label>
              </div>
            )}
            <div className="ml-auto flex items-center gap-2">
              <span className="text-xs text-muted-foreground">
                已选 <span className="tabular-nums">{describePermissions(draftMask).length}</span> 项权限
              </span>
              {!selected.is_everyone && (
                <Button variant="destructive" size="icon-sm" aria-label="删除角色" onClick={() => onDelete(selected)}>
                  <Trash2Icon />
                </Button>
              )}
              <Button onClick={onSave} disabled={!dirty || saving}>
                {saving ? "保存中…" : "保存修改"}
              </Button>
            </div>
          </div>
          <RoleStyleSection key={`style-${selected.id}`} guildID={guildID} role={selected} reload={reload} />
          <FeatureBitsSection key={`bits-${selected.id}`} guildID={guildID} role={selected} />
          <PermissionMatrix value={draftMask} onChange={setDraftMask} />
        </section>
      ) : (
        <EmptyState title="选择一个角色" description="左侧选择角色后可编辑其 64 位权限位。" />
      )}
    </div>
  )
}

/** 角色名样式（纯色/线性/多色/径向渐变）编辑与保存 */
function RoleStyleSection({ guildID, role, reload }: { guildID: string; role: Role; reload: () => void }) {
  const [style, setStyle] = useState<RoleStyle>(() => parseRoleStyle(role.style))
  const [saving, setSaving] = useState(false)
  const dirty = JSON.stringify(style) !== JSON.stringify(parseRoleStyle(role.style))

  async function onSave() {
    setSaving(true)
    try {
      await updateRoleStyle(guildID, role.id, style)
      toast.success("角色名样式已保存")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "样式保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <fieldset className="rounded-2xl border p-4">
      <legend className="flex items-center gap-2 px-2">
        <PaletteIcon className="size-4 text-muted-foreground" />
        <span className="text-sm font-semibold">用户名样式</span>
        <span className="text-xs text-muted-foreground">拥有此角色的成员按此渲染名字</span>
      </legend>
      <div className="flex flex-col gap-4">
        <RoleStyleEditor value={style} onChange={setStyle} previewText={role.name} />
        <div className="flex justify-end">
          <Button size="sm" onClick={onSave} disabled={!dirty || saving}>
            {saving ? "保存中…" : "保存样式"}
          </Button>
        </div>
      </div>
    </fieldset>
  )
}

const FEATURE_BIT_ITEMS: { key: keyof RoleFeatureBits; label: string; description: string }[] = [
  { key: "manage_customization", label: "管理展示自定义", description: "编辑角色名颜色/渐变等样式" },
  { key: "manage_badges", label: "管理徽章", description: "创建徽章并分配/回收（永久或限时）" },
  { key: "manage_bots", label: "管理机器人", description: "创建与配置本服机器人集成" },
]

/** 扩展管理权限位（52–54，超出 JS 数值精度，走独立布尔端点） */
function FeatureBitsSection({ guildID, role }: { guildID: string; role: Role }) {
  const [bits, setBits] = useState<RoleFeatureBits | null>(null)

  useEffect(() => {
    let cancelled = false
    getRoleFeatureBits(guildID, role.id)
      .then(next => {
        if (!cancelled) setBits(next)
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [guildID, role.id])

  async function toggle(key: keyof RoleFeatureBits, value: boolean) {
    const previous = bits
    setBits(current => (current ? { ...current, [key]: value } : current))
    try {
      const next = await patchRoleFeatureBits(guildID, role.id, { [key]: value })
      setBits(next)
    } catch (reason) {
      setBits(previous)
      toast.error(reason instanceof Error ? reason.message : "扩展权限保存失败")
    }
  }

  return (
    <fieldset className="rounded-2xl border p-4">
      <legend className="px-2 text-sm font-semibold">扩展管理权限</legend>
      <div className="grid gap-1.5 sm:grid-cols-2 xl:grid-cols-3">
        {FEATURE_BIT_ITEMS.map(item => (
          <label
            key={item.key}
            className={cn(
              "flex min-h-10 cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2 transition-[background-color,border-color]",
              bits?.[item.key] ? "border-primary/40 bg-primary/5" : "hover:bg-muted/60"
            )}
          >
            <Switch
              checked={Boolean(bits?.[item.key])}
              disabled={bits === null}
              onCheckedChange={next => toggle(item.key, Boolean(next))}
              className="mt-0.5"
            />
            <span className="min-w-0">
              <span className="block text-sm leading-5">{item.label}</span>
              <span className="block text-xs leading-4 text-muted-foreground">{item.description}</span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  )
}
