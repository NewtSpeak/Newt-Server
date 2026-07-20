import { useEffect, useState } from "react"
import { InfoIcon, PlusIcon, ShieldCheckIcon, Trash2Icon, UserIcon } from "lucide-react"
import { toast } from "sonner"

import { OverwriteMatrix } from "~/components/server/permission-matrix"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  deleteChannelOverwrite,
  listChannelOverwrites,
  putChannelOverwrite,
  type Channel,
  type ChannelOverwriteView,
  type MemberDisplay,
  type OverwriteType,
  type Role,
} from "~/lib/api"
import { describePermissions } from "~/lib/permissions"
import { cn } from "~/lib/utils"

/**
 * 权限覆盖标签页：选中频道后读回既有覆盖列表（可编辑/删除），
 * 也可新增覆盖目标；保存为整体替换该目标在此频道的覆盖记录。
 */
export function OverwritesTab({
  guildID,
  channels,
  roles,
  members,
}: {
  guildID: string
  channels: Channel[]
  roles: Role[]
  members: MemberDisplay[]
}) {
  const [channelID, setChannelID] = useState<string | null>(channels[0]?.id ?? null)
  const overwrites = useAsyncData<ChannelOverwriteView[]>(
    channelID ? () => listChannelOverwrites(guildID, channelID) : null,
    [guildID, channelID]
  )

  // 编辑器状态：选中既有覆盖，或新增（targetID + type）。
  const [targetType, setTargetType] = useState<OverwriteType>("ROLE")
  const [targetID, setTargetID] = useState<string | null>(null)
  const [allow, setAllow] = useState(0)
  const [deny, setDeny] = useState(0)
  const [saving, setSaving] = useState(false)
  const [adding, setAdding] = useState(false)

  useEffect(() => {
    if (!channelID && channels[0]) setChannelID(channels[0].id)
  }, [channelID, channels])

  // 切换频道时重置编辑器。
  useEffect(() => {
    setTargetID(null)
    setAdding(false)
    setAllow(0)
    setDeny(0)
  }, [channelID])

  const list = overwrites.data ?? []
  const selected = list.find(item => item.target_id === targetID && item.type === targetType) ?? null

  function selectExisting(item: ChannelOverwriteView) {
    setAdding(false)
    setTargetType(item.type)
    setTargetID(item.target_id)
    setAllow(item.allow)
    setDeny(item.deny)
  }

  const existingKeys = new Set(list.map(item => `${item.type}:${item.target_id}`))
  const addOptions =
    targetType === "ROLE"
      ? roles
          .filter(role => !existingKeys.has(`ROLE:${role.id}`))
          .map(role => ({ value: role.id, label: `${role.name}（P${role.position}）` }))
      : members
          .filter(member => !existingKeys.has(`MEMBER:${member.id}`))
          .map(member => ({ value: member.id, label: member.nickname || member.username }))

  async function onSave() {
    if (!channelID || !targetID) return
    setSaving(true)
    try {
      await putChannelOverwrite(guildID, channelID, targetID, { type: targetType, allow, deny })
      toast.success("频道权限覆盖已保存")
      setAdding(false)
      overwrites.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "覆盖保存失败")
    } finally {
      setSaving(false)
    }
  }

  async function onDelete(item: ChannelOverwriteView) {
    if (!channelID) return
    if (!window.confirm(`确定删除对「${item.target_name || item.target_id}」的覆盖？`)) return
    try {
      await deleteChannelOverwrite(guildID, channelID, item.target_id, item.type)
      toast.success("覆盖已删除")
      if (targetID === item.target_id) {
        setTargetID(null)
        setAllow(0)
        setDeny(0)
      }
      overwrites.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "覆盖删除失败")
    }
  }

  if (channels.length === 0) {
    return <EmptyState title="暂无频道" description="先在「频道」标签页创建频道，再配置权限覆盖。" />
  }

  return (
    <div className="grid gap-5 lg:grid-cols-[300px_1fr]">
      <aside className="flex flex-col gap-3">
        <SimpleSelect
          ariaLabel="选择频道"
          placeholder="选择频道"
          value={channelID}
          onChange={setChannelID}
          options={channels.map(channel => ({
            value: channel.id,
            label: `${channel.type === "TEXT" ? "#" : "🔊"} ${channel.name}`,
          }))}
        />

        {overwrites.status === "loading" && <LoadingState rows={3} />}
        {overwrites.status === "error" && <ErrorState message={overwrites.error} onRetry={() => overwrites.reload()} />}
        {overwrites.status === "success" && (
          <div className="flex flex-col gap-1" role="listbox" aria-label="既有覆盖">
            <p className="px-1 text-xs text-muted-foreground">
              既有覆盖（<span className="tabular-nums">{list.length}</span>）
            </p>
            {list.map(item => {
              const active = !adding && targetID === item.target_id && targetType === item.type
              const allowCount = describePermissions(item.allow).length
              const denyCount = describePermissions(item.deny).length
              return (
                <div
                  key={item.id}
                  className={cn(
                    "flex items-center gap-2 rounded-lg border px-2.5 py-2 transition-[background-color,border-color]",
                    active ? "border-primary/50 bg-primary/5" : "border-transparent hover:bg-muted/60"
                  )}
                >
                  <button
                    type="button"
                    role="option"
                    aria-selected={active}
                    onClick={() => selectExisting(item)}
                    className="flex min-w-0 flex-1 items-center gap-2 text-left focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none"
                  >
                    {item.type === "ROLE" ? (
                      <ShieldCheckIcon className="size-4 shrink-0 text-muted-foreground" />
                    ) : (
                      <UserIcon className="size-4 shrink-0 text-muted-foreground" />
                    )}
                    <span className="min-w-0">
                      <span className="block truncate text-sm">{item.target_name || item.target_id}</span>
                      <span className="block text-[10px] text-muted-foreground">
                        允许 {allowCount} · 拒绝 {denyCount}
                      </span>
                    </span>
                  </button>
                  <Button variant="ghost" size="icon-xs" aria-label="删除覆盖" onClick={() => onDelete(item)}>
                    <Trash2Icon />
                  </Button>
                </div>
              )
            })}
            {list.length === 0 && <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">此频道暂无覆盖</p>}
          </div>
        )}

        <div className="flex flex-col gap-2 rounded-xl border p-3">
          <p className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
            <PlusIcon className="size-3.5" />
            新增覆盖
          </p>
          <SimpleSelect
            ariaLabel="目标类型"
            value={targetType}
            onChange={next => {
              setTargetType(next as OverwriteType)
              setTargetID(null)
            }}
            options={[
              { value: "ROLE", label: "按角色" },
              { value: "MEMBER", label: "按成员" },
            ]}
          />
          <SimpleSelect
            ariaLabel="选择目标"
            placeholder={targetType === "ROLE" ? "选择角色" : "选择成员"}
            value={adding ? targetID : null}
            onChange={next => {
              setAdding(true)
              setTargetID(next)
              setAllow(0)
              setDeny(0)
            }}
            options={addOptions}
          />
        </div>
      </aside>

      <section className="flex min-w-0 flex-col gap-4">
        <p className="flex items-start gap-1.5 rounded-lg border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
          <InfoIcon className="mt-0.5 size-3.5 shrink-0" />
          覆盖仅对所选频道生效：先应用「拒绝」再应用「允许」；成员覆盖优先级最高；拥有管理员位的角色不受频道拒绝限制。保存将整体替换该目标在此频道的覆盖记录。
        </p>
        {targetID ? (
          <>
            <div className="flex items-center gap-2">
              <Badge variant="secondary">
                {targetType === "ROLE" ? "角色" : "成员"} ·{" "}
                {selected?.target_name ??
                  (targetType === "ROLE"
                    ? roles.find(role => role.id === targetID)?.name
                    : members.find(member => member.id === targetID)?.username) ??
                  targetID}
              </Badge>
              <Button onClick={onSave} disabled={saving} className="ml-auto">
                {saving ? "保存中…" : "保存覆盖"}
              </Button>
            </div>
            <OverwriteMatrix
              allow={allow}
              deny={deny}
              onChange={(nextAllow, nextDeny) => {
                setAllow(nextAllow)
                setDeny(nextDeny)
              }}
            />
          </>
        ) : (
          <EmptyState title="选择覆盖目标" description="从左侧选择既有覆盖进行编辑，或新增角色/成员覆盖。" />
        )}
      </section>
    </div>
  )
}
