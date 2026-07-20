import { useEffect, useMemo, useState } from "react"
import { useOutletContext } from "react-router"
import { ChevronDownIcon, ChevronUpIcon, ScrollTextIcon } from "lucide-react"
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
  type AuditLogEntry,
  type AuditLogFilters,
  type AuditLogPage,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatFullTime, formatRelative } from "~/lib/format"

/** action → 中文标签；未收录的 action 原样展示 */
const ACTION_LABELS: Record<string, string> = {
  "rbac.role_create": "创建角色",
  "rbac.role_update": "修改角色",
  "rbac.member_role_assign": "绑定成员角色",
  "rbac.member_role_remove": "移除成员角色",
  "rbac.channel_create": "创建频道",
  "rbac.channel_overwrite_update": "更新频道权限覆盖",
  "restriction.create": "创建限制",
  "restriction.update": "修改限制",
  "restriction.lift": "解除限制",
  "restriction.expire": "限制到期",
  "moderation.kick": "踢出成员",
  "moderation.ban": "封禁用户",
  "moderation.unban": "解除封禁",
  "moderation.invite_create": "创建邀请",
  "moderation.member_join": "成员加入",
  "message.delete_by_moderator": "管理删除消息",
  "message.upload_limit": "调整上传限制",
  "message.retention": "调整消息保留",
  "voicepack.guild_config": "语音包服务器配置",
  "voicepack.channel_toggle": "语音包频道开关",
  "stage.config_update": "舞台配置变更",
  "stage.bring_down": "舞台抱下麦",
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
}

/** 操作类型过滤：按 action 前缀分组 */
const ACTION_FILTERS = [
  { value: "all", label: "全部操作" },
  { value: "rbac.", label: "角色与频道（RBAC）" },
  { value: "restriction.", label: "限制" },
  { value: "moderation.", label: "成员治理" },
  { value: "message.", label: "消息" },
  { value: "voicepack.", label: "语音包" },
  { value: "stage.", label: "舞台" },
  { value: "screen.", label: "屏幕共享" },
  { value: "sfu_node.", label: "SFU 节点" },
  { value: "sfu_pool.", label: "节点池" },
  { value: "voice.", label: "语音" },
]

const RANGE_FILTERS = [
  { value: "all", label: "全部时间", hours: 0 },
  { value: "24h", label: "近 24 小时", hours: 24 },
  { value: "7d", label: "近 7 天", hours: 24 * 7 },
  { value: "30d", label: "近 30 天", hours: 24 * 30 },
]

const ALL_GUILDS = "__all__"

export default function AuditPage() {
  const { user, guilds } = useOutletContext<ConsoleContext>()
  // 系统管理员默认看全量流水；普通用户只能按服查看（后端校验 VIEW_AUDIT_LOG）。
  const [guildID, setGuildID] = useState<string | null>(user.system_admin ? ALL_GUILDS : (guilds[0]?.id ?? null))
  const [actionPrefix, setActionPrefix] = useState("all")
  const [range, setRange] = useState("all")

  useEffect(() => {
    if (!guildID && guilds[0]) setGuildID(user.system_admin ? ALL_GUILDS : guilds[0].id)
  }, [guildID, guilds, user.system_admin])

  const filters = useMemo((): AuditLogFilters => {
    const result: AuditLogFilters = {}
    if (actionPrefix !== "all") result.action = actionPrefix
    const hours = RANGE_FILTERS.find(item => item.value === range)?.hours ?? 0
    if (hours > 0) result.since = new Date(Date.now() - hours * 3_600_000).toISOString()
    return result
  }, [actionPrefix, range])

  const fetchPage = useMemo(() => {
    if (!guildID) return null
    if (guildID === ALL_GUILDS) {
      return (before?: string) => listAdminAuditLogs({ ...filters, before })
    }
    const gid = guildID
    return (before?: string) => listGuildAuditLogs(gid, { ...filters, before })
  }, [guildID, filters])

  const firstPage = useAsyncData<AuditLogPage>(fetchPage ? () => fetchPage(undefined) : null, [fetchPage])

  // 「加载更多」追加页：过滤条件变化时清空
  const [more, setMore] = useState<{ items: AuditLogEntry[]; cursor?: string; hasMore: boolean } | null>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  useEffect(() => setMore(null), [fetchPage])

  const items = useMemo(
    () => [...(firstPage.data?.items ?? []), ...(more?.items ?? [])],
    [firstPage.data, more],
  )
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

  const guildOptions = [
    ...(user.system_admin ? [{ value: ALL_GUILDS, label: "全部服务器" }] : []),
    ...guilds.map(guild => ({ value: guild.id, label: guild.name })),
  ]

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="审计日志"
        description="节点 Enrollment / 启停、限制与封禁、角色与覆盖变更等敏感操作的完整审计流水，支持按服务器、操作类型与时间范围检索。"
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
            title="没有匹配的审计记录"
            description="调整过滤条件，或等待新的敏感操作产生审计事件。"
          />
        )}

        {firstPage.status === "success" && items.length > 0 && (
          <>
            {/* 时间线：左侧竖线串联事件圆点 */}
            <ol className="relative flex flex-col gap-0 border-l pl-5 [margin-left:0.4375rem]">
              {items.map((entry, index) => (
                <AuditTimelineItem key={entry.id} entry={entry} index={index} showGuild={guildID === ALL_GUILDS} />
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

function AuditTimelineItem({ entry, index, showGuild }: { entry: AuditLogEntry; index: number; showGuild: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const actionLabel = ACTION_LABELS[entry.action] ?? entry.action
  const actorLabel = entry.actor_username || (entry.actor_id ? entry.actor_id.slice(0, 8) : "系统")
  const actorType = ACTOR_TYPE_LABELS[entry.actor_type] ?? entry.actor_type
  const targetType = TARGET_TYPE_LABELS[entry.target_type] ?? entry.target_type
  const targetLabel = entry.target_summary || entry.target_id
  const hasDetail = entry.detail && Object.keys(entry.detail).length > 0

  return (
    <li
      style={{ "--stagger-index": index } as React.CSSProperties}
      className="anim-item relative pb-5 last:pb-0"
    >
      <span className="absolute top-1.5 -left-[1.5625rem] size-2.5 rounded-full border-2 border-background bg-primary/70" aria-hidden />
      <div className="flex flex-col gap-1 rounded-xl border px-4 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{actionLabel}</span>
          <Badge variant="secondary" className="font-normal">{actorType}</Badge>
          {showGuild && entry.guild_name && (
            <Badge variant="outline" className="font-normal">{entry.guild_name}</Badge>
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
        {hasDetail && (
          <div>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs text-muted-foreground"
              onClick={() => setExpanded(current => !current)}
            >
              {expanded ? <ChevronUpIcon data-icon="inline-start" /> : <ChevronDownIcon data-icon="inline-start" />}
              {expanded ? "收起详情" : "查看详情"}
            </Button>
            {expanded && (
              <pre className="mt-1 max-h-72 overflow-auto rounded-lg bg-muted/60 p-3 text-xs leading-relaxed">
                {JSON.stringify(entry.detail, null, 2)}
              </pre>
            )}
          </div>
        )}
      </div>
    </li>
  )
}
