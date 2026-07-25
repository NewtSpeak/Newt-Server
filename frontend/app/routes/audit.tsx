import { useEffect, useMemo, useState } from "react"
import { useOutletContext } from "react-router"
import { ChevronDownIcon, ChevronUpIcon, ScrollTextIcon, Undo2Icon } from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  listAdminAuditLogs,
  listGuildAuditLogs,
  undoAdminAuditLog,
  undoGuildAuditLog,
  type AuditLogEntry,
  type AuditLogFilters,
  type AuditLogPage,
  type AuditUndoStatus,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatFullTime, formatRelative } from "~/lib/format"

/** action → 中文标签；优先用服务端 action_label */
const ACTION_LABELS: Record<string, string> = {
  "rbac.role_create": "创建角色",
  "rbac.role_update": "修改角色",
  "rbac.role_delete": "删除角色",
  "rbac.role_reorder": "角色排序",
  "rbac.member_role_assign": "绑定成员角色",
  "rbac.member_role_remove": "移除成员角色",
  "rbac.channel_create": "创建频道",
  "rbac.channel_update": "修改频道",
  "rbac.channel_delete": "删除频道",
  "rbac.channel_overwrite_update": "更新频道权限覆盖",
  "rbac.channel_overwrite_delete": "删除频道权限覆盖",
  "restriction.create": "创建限制",
  "restriction.update": "修改限制",
  "restriction.lift": "解除限制",
  "restriction.expire": "限制到期",
  "moderation.kick": "踢出成员",
  "moderation.ban": "封禁用户",
  "moderation.unban": "解除封禁",
  "moderation.invite_create": "创建邀请",
  "moderation.member_join": "成员加入",
  "moderation.nickname_update": "修改昵称",
  "guild.create": "创建服务器",
  "guild.update": "更新服务器",
  "guild.delete": "删除服务器",
  "guild.transfer_ownership": "转让所有权",
  "message.delete_by_moderator": "管理删除消息",
  "message.upload_limit": "调整上传限制",
  "message.retention": "调整消息保留",
  "voicepack.guild_config": "语音包服务器配置",
  "voicepack.channel_toggle": "语音包频道开关",
  "stage.config_update": "舞台配置变更",
  "stage.bring_down": "舞台抱下麦",
  "stage.bring_up": "舞台抱上麦",
  "screen.stop_user": "强制结束共享",
  "screen.guild_quota_update": "调整屏幕配额",
  "screen.platform_settings_update": "平台共享设置",
  "sfu_node.create": "创建 SFU 节点",
  "sfu_node.enroll": "节点 Enrollment",
  "sfu_node.renew_certificate": "节点证书续期",
  "sfu_node.revoke": "吊销节点",
  "sfu_node.drain": "节点排空",
  "sfu_node.undrain": "取消排空",
  "sfu_node.disable": "停用节点",
  "sfu_node.enable": "启用节点",
  "sfu_node.update": "更新节点",
  "sfu_node.drain_command_failed": "排空指令失败",
  "sfu_pool.admin_update": "节点池变更（系统管）",
  "sfu_pool.guild_update": "节点池变更（服管）",
  "voice.admin_disconnect": "管理断开语音",
  "voice.server_state_update": "服务端语音状态",
  "voice.migration.created": "语音迁移创建",
  "voice.migration.completed": "语音迁移完成",
  "voice.migration.failed": "语音迁移失败",
  "bot.install": "安装机器人",
  "bot.uninstall": "卸载机器人",
  "bot.create": "创建机器人",
  "bot.delete": "删除机器人",
  "sticker.pack.guild_ban": "服内封禁贴图包",
  "sticker.pack.guild_unban": "解除贴图包封禁",
  "audit.undo": "撤销操作",
  "platform.user_disable": "禁用用户",
  "platform.user_enable": "启用用户",
}

const ACTOR_TYPE_LABELS: Record<string, string> = {
  user: "用户",
  system_admin: "系统管理员",
  guild_admin: "服务器管理员",
  auto: "系统自动",
  node: "节点",
}

const TARGET_TYPE_LABELS: Record<string, string> = {
  user: "用户",
  member: "成员",
  role: "角色",
  channel: "频道",
  guild: "服务器",
  message: "消息",
  restriction: "限制",
  invite: "邀请",
  sfu_node: "SFU 节点",
  guild_node_pool: "节点池",
  voice_migration: "语音迁移",
  platform: "平台",
  bot: "机器人",
}

const ACTION_FILTERS = [
  { value: "all", label: "全部操作" },
  { value: "rbac.", label: "角色与频道（RBAC）" },
  { value: "restriction.", label: "限制" },
  { value: "moderation.", label: "成员治理" },
  { value: "guild.", label: "服务器" },
  { value: "bot.", label: "机器人" },
  { value: "message.", label: "消息" },
  { value: "voicepack.", label: "语音包" },
  { value: "stage.", label: "舞台" },
  { value: "screen.", label: "屏幕共享" },
  { value: "sfu_node.", label: "SFU 节点" },
  { value: "sfu_pool.", label: "节点池" },
  { value: "voice.", label: "语音" },
  { value: "audit.undo", label: "撤销记录" },
]

const RANGE_FILTERS = [
  { value: "all", label: "全部时间", hours: 0 },
  { value: "24h", label: "近 24 小时", hours: 24 },
  { value: "7d", label: "近 7 天", hours: 24 * 7 },
  { value: "30d", label: "近 30 天", hours: 24 * 30 },
]

const STATUS_FILTERS = [
  { value: "all", label: "全部状态" },
  { value: "available", label: "可撤销" },
  { value: "undone", label: "已撤销" },
  { value: "irreversible", label: "不可逆" },
]

const ALL_GUILDS = "__all__"

function labelOf(entry: AuditLogEntry): string {
  if (entry.action_label && entry.action_label !== entry.action) return entry.action_label
  return ACTION_LABELS[entry.action] ?? entry.action
}

function statusMeta(status?: AuditUndoStatus): { label: string; variant: "default" | "secondary" | "outline" | "destructive" } {
  switch (status) {
    case "available":
      return { label: "可撤销", variant: "default" }
    case "undone":
      return { label: "已撤销", variant: "secondary" }
    case "irreversible":
      return { label: "不可逆", variant: "outline" }
    case "blocked":
      return { label: "暂不可撤", variant: "outline" }
    default:
      return { label: "记录", variant: "secondary" }
  }
}

export default function AuditPage() {
  const { user, guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useState<string | null>(user.system_admin ? ALL_GUILDS : (guilds[0]?.id ?? null))
  const [actionPrefix, setActionPrefix] = useState("all")
  const [range, setRange] = useState("all")
  const [undoStatus, setUndoStatus] = useState("all")
  const [localOverrides, setLocalOverrides] = useState<Record<string, AuditLogEntry>>({})
  const [prepended, setPrepended] = useState<AuditLogEntry[]>([])

  useEffect(() => {
    if (!guildID && guilds[0]) setGuildID(user.system_admin ? ALL_GUILDS : guilds[0].id)
  }, [guildID, guilds, user.system_admin])

  const filters = useMemo((): AuditLogFilters => {
    const result: AuditLogFilters = {}
    if (actionPrefix !== "all") result.action = actionPrefix
    if (undoStatus !== "all") result.undo_status = undoStatus as AuditUndoStatus
    const hours = RANGE_FILTERS.find(item => item.value === range)?.hours ?? 0
    if (hours > 0) result.since = new Date(Date.now() - hours * 3_600_000).toISOString()
    return result
  }, [actionPrefix, range, undoStatus])

  const fetchPage = useMemo(() => {
    if (!guildID) return null
    if (guildID === ALL_GUILDS) {
      return (before?: string) => listAdminAuditLogs({ ...filters, before })
    }
    const gid = guildID
    return (before?: string) => listGuildAuditLogs(gid, { ...filters, before })
  }, [guildID, filters])

  const firstPage = useAsyncData<AuditLogPage>(fetchPage ? () => fetchPage(undefined) : null, [fetchPage])

  const [more, setMore] = useState<{ items: AuditLogEntry[]; cursor?: string; hasMore: boolean } | null>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  useEffect(() => {
    setMore(null)
    setLocalOverrides({})
    setPrepended([])
  }, [fetchPage])

  const items = useMemo(() => {
    const base = [...prepended, ...(firstPage.data?.items ?? []), ...(more?.items ?? [])]
    const seen = new Set<string>()
    const out: AuditLogEntry[] = []
    for (const item of base) {
      const merged = localOverrides[item.id] ?? item
      if (seen.has(merged.id)) continue
      seen.add(merged.id)
      out.push(merged)
    }
    return out
  }, [firstPage.data, more, localOverrides, prepended])

  const nextCursor = more ? more.cursor : firstPage.data?.next_cursor
  const hasMore = more ? more.hasMore : (firstPage.data?.has_more ?? false)

  async function loadMore() {
    if (!fetchPage || !nextCursor || loadingMore) return
    setLoadingMore(true)
    try {
      const page = await fetchPage(nextCursor)
      setMore(current => ({
        items: [...(current?.items ?? []), ...page.items],
        cursor: page.next_cursor,
        hasMore: page.has_more,
      }))
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "加载更多失败")
    } finally {
      setLoadingMore(false)
    }
  }

  async function handleUndo(entry: AuditLogEntry) {
    const hint = entry.undo_hint || `将撤销「${labelOf(entry)}」`
    if (!window.confirm(`${hint}\n\n确认撤销？此操作会写入新的操作日志。`)) return
    try {
      const res =
        guildID === ALL_GUILDS
          ? await undoAdminAuditLog(entry.id)
          : await undoGuildAuditLog(guildID!, entry.id)
      setLocalOverrides(current => ({
        ...current,
        [res.original.id]: res.original,
      }))
      setPrepended(current => [res.undo, ...current])
      toast.success("已撤销该操作")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "撤销失败")
    }
  }

  const guildOptions = [
    ...(user.system_admin ? [{ value: ALL_GUILDS, label: "全部服务器" }] : []),
    ...guilds.map(guild => ({ value: guild.id, label: guild.name })),
  ]

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="操作日志"
        description="全部管理操作的可检索时间线。可撤销的操作可在卡片上一键撤回；不可逆操作会明确标注。"
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={setGuildID}
            options={guildOptions}
            className="w-44"
          />
          <SimpleSelect
            ariaLabel="操作类型"
            value={actionPrefix}
            onChange={setActionPrefix}
            options={ACTION_FILTERS}
            className="w-44"
          />
          <SimpleSelect
            ariaLabel="撤销状态"
            value={undoStatus}
            onChange={setUndoStatus}
            options={STATUS_FILTERS}
            className="w-36"
          />
          <SimpleSelect
            ariaLabel="时间范围"
            value={range}
            onChange={setRange}
            options={RANGE_FILTERS.map(({ value, label }) => ({ value, label }))}
            className="w-36"
          />
        </div>

        {firstPage.status === "loading" && <LoadingState rows={6} />}
        {firstPage.status === "error" && <ErrorState message={firstPage.error} onRetry={() => firstPage.reload()} />}
        {firstPage.status === "success" && items.length === 0 && (
          <EmptyState
            icon={ScrollTextIcon}
            title="没有匹配的操作记录"
            description="调整过滤条件，或等待新的管理操作产生日志。"
          />
        )}

        {firstPage.status === "success" && items.length > 0 && (
          <>
            <ol className="relative flex flex-col gap-0 border-l pl-5 [margin-left:0.4375rem]">
              {items.map((entry, index) => (
                <AuditTimelineItem
                  key={entry.id}
                  entry={entry}
                  index={index}
                  showGuild={guildID === ALL_GUILDS}
                  onUndo={() => void handleUndo(entry)}
                />
              ))}
            </ol>
            {hasMore && (
              <Button variant="outline" className="self-center" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? "加载中…" : "加载更多"}
              </Button>
            )}
          </>
        )}
      </section>
    </main>
  )
}

function AuditTimelineItem({
  entry,
  index,
  showGuild,
  onUndo,
}: {
  entry: AuditLogEntry
  index: number
  showGuild: boolean
  onUndo: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  const actionLabel = labelOf(entry)
  const actorLabel = entry.actor_username || (entry.actor_id ? entry.actor_id.slice(0, 8) : "系统")
  const actorType = ACTOR_TYPE_LABELS[entry.actor_type] ?? entry.actor_type
  const targetType = TARGET_TYPE_LABELS[entry.target_type] ?? entry.target_type
  const targetLabel = entry.target_summary || entry.target_id
  const hasDetail = entry.detail && Object.keys(entry.detail).length > 0
  const status = entry.undo_status ?? "none"
  const meta = statusMeta(status)
  const canUndo = entry.reversible === true || status === "available"
  const faded = status === "undone"

  return (
    <li
      style={{ "--stagger-index": index } as React.CSSProperties}
      className={`anim-item relative pb-5 last:pb-0 ${faded ? "opacity-60" : ""}`}
    >
      <span
        className={`absolute top-1.5 -left-[1.5625rem] size-2.5 rounded-full border-2 border-background ${
          canUndo ? "bg-emerald-500" : status === "undone" ? "bg-muted-foreground/40" : "bg-primary/70"
        }`}
        aria-hidden
      />
      <div className="flex flex-col gap-1 rounded-xl border px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{actionLabel}</span>
          <Badge variant={meta.variant} className="font-normal">
            {meta.label}
          </Badge>
          <Badge variant="secondary" className="font-normal">
            {actorType}
          </Badge>
          {showGuild && entry.guild_name && (
            <Badge variant="outline" className="font-normal">
              {entry.guild_name}
            </Badge>
          )}
          <span className="ml-auto text-xs text-muted-foreground" title={formatFullTime(entry.created_at)}>
            {formatRelative(entry.created_at)}
          </span>
        </div>
        <p className="text-xs text-muted-foreground">
          <span className="text-foreground/80">{actorLabel}</span>
          {" 对 "}
          <span className="text-foreground/80">
            {targetType}
            {targetLabel ? `「${targetLabel}」` : ""}
          </span>
          {" 执行了 "}
          <code className="rounded bg-muted px-1 py-0.5 text-[11px]">{entry.action}</code>
        </p>
        {entry.undo_hint && status !== "none" && status !== "available" && (
          <p className="text-[11px] text-muted-foreground/80">{entry.undo_hint}</p>
        )}
        <div className="mt-1 flex flex-wrap items-center gap-2">
          {canUndo && (
            <Button size="sm" className="h-7 gap-1 px-2.5 text-xs" onClick={onUndo}>
              <Undo2Icon className="size-3.5" data-icon="inline-start" />
              撤销
            </Button>
          )}
          {hasDetail && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs text-muted-foreground"
              onClick={() => setExpanded(current => !current)}
            >
              {expanded ? <ChevronUpIcon data-icon="inline-start" /> : <ChevronDownIcon data-icon="inline-start" />}
              {expanded ? "收起详情" : "查看详情"}
            </Button>
          )}
        </div>
        {expanded && hasDetail && (
          <pre className="mt-1 max-h-72 overflow-auto rounded-lg bg-muted/60 p-3 text-xs leading-relaxed">
            {JSON.stringify(entry.detail, null, 2)}
          </pre>
        )}
      </div>
    </li>
  )
}
