import { useEffect, useState } from "react"
import { useOutletContext } from "react-router"
import { NetworkIcon, SaveIcon } from "lucide-react"
import { toast } from "sonner"

import { NodeStatusBadge } from "~/components/node-status-badge"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
import { Checkbox } from "~/components/ui/checkbox"
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { useGuildID } from "~/hooks/use-guild-id"
import { getGuildNodePool, listSfuNodes, putGuildNodePool, type NodePool, type SfuNode } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { cn } from "~/lib/utils"

export default function NodePoolsPage() {
  const { user, guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)

  const nodes = useAsyncData<SfuNode[]>(() => listSfuNodes(), [])
  const pool = useAsyncData<NodePool>(guildID ? () => getGuildNodePool(guildID) : null, [guildID])

  // 实时同步：他端/系统管改动本服节点池后立即刷新勾选状态。
  useGatewayEvent("VOICE_NODE_POOL_UPDATE", payload => {
    const gid = (payload as { guild_id?: string } | undefined)?.guild_id
    if (!gid || gid === guildID) pool.reload(true)
  })

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setSelected(new Set(pool.data?.node_ids ?? []))
  }, [pool.data])

  const dirty =
    pool.status === "success" &&
    (selected.size !== (pool.data?.node_ids?.length ?? 0) || (pool.data?.node_ids ?? []).some(id => !selected.has(id)))

  async function onSave() {
    if (!guildID) return
    setSaving(true)
    try {
      await putGuildNodePool(guildID, Array.from(selected), user.system_admin)
      toast.success("节点池已保存，该服语音仅在池内调度")
      pool.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "节点池保存失败")
    } finally {
      setSaving(false)
    }
  }

  function toggle(nodeID: string, on: boolean) {
    setSelected(current => {
      const next = new Set(current)
      if (on) next.add(nodeID)
      else next.delete(nodeID)
      return next
    })
  }

  const nodeList = nodes.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="节点池"
        description="为服务器圈定可用 SFU 节点集合：该服语音频道只在池内调度，池外节点不可见（含跨境/跨区节点划分）。"
        actions={
          <Button onClick={onSave} disabled={!dirty || saving}>
            <SaveIcon data-icon="inline-start" />
            {saving ? "保存中…" : "保存节点池"}
          </Button>
        }
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={setGuildID}
            options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
            className="w-60"
          />
          {pool.status === "success" && (
            <span className="text-xs text-muted-foreground">
              已选 <span className="font-medium text-foreground tabular-nums">{selected.size}</span> / {nodeList.length} 个节点
            </span>
          )}
        </div>

        {guilds.length === 0 && <EmptyState title="暂无服务器" description="先创建服务器，再为其配置节点池。" />}
        {guildID && (nodes.status === "loading" || pool.status === "loading") && <LoadingState rows={4} />}
        {guildID && nodes.status === "error" && <ErrorState message={nodes.error} onRetry={() => nodes.reload()} />}
        {guildID && nodes.status === "success" && pool.status === "error" && (
          <ErrorState message={pool.error} onRetry={() => pool.reload()} />
        )}

        {guildID && nodes.status === "success" && pool.status === "success" && (
          nodeList.length === 0 ? (
            <EmptyState icon={NetworkIcon} title="平台还没有 SFU 节点" description="先到「SFU 节点」页面完成节点 Enrollment。" />
          ) : (
            <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
              {nodeList.map((node, index) => {
                const checked = selected.has(node.node_id)
                const schedulable = node.status === "ONLINE" && node.enabled_for_scheduling
                return (
                  <Label
                    key={node.node_id}
                    style={{ "--stagger-index": index } as React.CSSProperties}
                    className={cn(
                      "anim-item flex cursor-pointer items-start gap-3 rounded-xl border p-4 transition-[background-color,border-color]",
                      checked ? "border-primary/50 bg-primary/5" : "hover:bg-muted/50"
                    )}
                  >
                    <Checkbox checked={checked} onCheckedChange={next => toggle(node.node_id, Boolean(next))} className="mt-0.5" />
                    <span className="min-w-0 flex-1">
                      <span className="flex items-center justify-between gap-2">
                        <span className="truncate text-sm font-medium">{node.display_name}</span>
                        <NodeStatusBadge status={node.status} />
                      </span>
                      <span className="mt-1 block truncate font-mono text-[10px] text-muted-foreground">{node.node_id}</span>
                      <span className="mt-1.5 flex items-center gap-3 text-xs text-muted-foreground">
                        <span>地域 {node.labels?.region ?? "—"}</span>
                        {!schedulable && <span className="text-amber-600 dark:text-amber-400">暂不可调度</span>}
                      </span>
                    </span>
                  </Label>
                )
              })}
            </div>
          )
        )}
      </section>
    </main>
  )
}
