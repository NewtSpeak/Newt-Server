import { useMemo, useState } from "react"
import { useOutletContext } from "react-router"
import {
  ArrowLeftRightIcon,
  HeadphoneOffIcon,
  MicIcon,
  MicOffIcon,
  MonitorUpIcon,
  PhoneOffIcon,
  Volume2Icon,
} from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback } from "~/components/ui/avatar"
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
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { useGuildID } from "~/hooks/use-guild-id"
import {
  createVoiceMigration,
  disconnectVoiceUser,
  listChannels,
  listSfuNodes,
  listVoiceStates,
  type Channel,
  type SfuNode,
  type VoiceState,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"

const STAGE_ROLE_LABELS = { SPEAKER: "台上", QUEUED: "排队中", AUDIENCE: "听众" } as const

export default function VoiceStatesPage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)
  const [channelID, setChannelID] = useState<string | null>(null)

  const channels = useAsyncData<Channel[]>(guildID ? () => listChannels(guildID) : null, [guildID])
  const voiceChannels = useMemo(() => (channels.data ?? []).filter(channel => channel.type === "VOICE"), [channels.data])
  const activeChannel = channelID ?? voiceChannels[0]?.id ?? null

  const states = useAsyncData<VoiceState[]>(
    guildID && activeChannel ? () => listVoiceStates(guildID, activeChannel) : null,
    [guildID, activeChannel]
  )

  useGatewayEvent("VOICE_STATE_UPDATE", () => states.reload(true))

  const [migrateOpen, setMigrateOpen] = useState(false)
  const [targetNode, setTargetNode] = useState<string | null>(null)
  const [migrating, setMigrating] = useState(false)
  const nodes = useAsyncData<SfuNode[]>(migrateOpen ? () => listSfuNodes() : null, [migrateOpen])

  async function onDisconnect(state: VoiceState) {
    if (!guildID || !activeChannel) return
    const name = state.username ?? state.user_id
    if (!window.confirm(`确定断开「${name}」的语音连接？`)) return
    try {
      await disconnectVoiceUser(guildID, activeChannel, state.user_id)
      toast.success(`已断开「${name}」`)
      states.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "断开失败")
    }
  }

  async function onMigrate() {
    if (!guildID || !activeChannel) return
    const targets = states.data ?? []
    if (targets.length === 0) {
      toast.info("当前频道内没有可迁移的语音会话")
      return
    }
    setMigrating(true)
    try {
      // 热迁移以「用户语音会话」为粒度（docs 09 H.1），对频道内全部在房用户逐个发起
      const results = await Promise.allSettled(
        targets.map((state) =>
          createVoiceMigration({
            guild_id: guildID,
            user_id: state.user_id,
            to_node_id: targetNode ?? undefined,
          }),
        ),
      )
      const failed = results.filter((result) => result.status === "rejected").length
      if (failed === 0) toast.success(`已对 ${targets.length} 个语音会话发起迁移`)
      else toast.warning(`已发起迁移，其中 ${failed}/${targets.length} 个会话失败`)
      setMigrateOpen(false)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "迁移发起失败")
    } finally {
      setMigrating(false)
    }
  }

  const list = states.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="语音状态"
        description="查看语音频道在房用户与舞台角色，可管理员断开或发起热迁移（音画同迁、保留舞台状态）。"
        actions={
          <Button variant="outline" onClick={() => setMigrateOpen(true)} disabled={!activeChannel}>
            <ArrowLeftRightIcon data-icon="inline-start" />
            发起迁移
          </Button>
        }
      />

      <Dialog open={migrateOpen} onOpenChange={setMigrateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>发起热迁移</DialogTitle>
            <DialogDescription>
              将当前语音频道的会话迁移到节点池内其他节点。不选目标节点时由调度器按「最近 + 最空闲」打分自动选择。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label>目标节点（可选）</Label>
            <SimpleSelect
              ariaLabel="目标节点"
              placeholder="自动选择（推荐）"
              value={targetNode}
              onChange={setTargetNode}
              options={(nodes.data ?? [])
                .filter(node => node.status === "ONLINE")
                .map(node => ({ value: node.node_id, label: `${node.display_name}（${node.labels?.region ?? "未知地域"}）` }))}
              className="w-full"
            />
            {nodes.status === "error" && <p className="text-xs text-muted-foreground">节点列表暂不可用，将由调度器自动选择。</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setMigrateOpen(false)}>
              取消
            </Button>
            <Button onClick={onMigrate} disabled={migrating}>
              {migrating ? "发起中…" : "确认迁移"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={next => {
              setGuildID(next)
              setChannelID(null)
            }}
            options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
            className="w-52"
          />
          <SimpleSelect
            ariaLabel="选择语音频道"
            placeholder="选择语音频道"
            value={activeChannel}
            onChange={setChannelID}
            options={voiceChannels.map(channel => ({ value: channel.id, label: `🔊 ${channel.name}` }))}
            className="w-52"
            disabled={voiceChannels.length === 0}
          />
          {states.status === "success" && (
            <Badge variant="secondary" className="tabular-nums">
              在房 {list.length} 人
            </Badge>
          )}
        </div>

        {channels.status === "success" && voiceChannels.length === 0 && (
          <EmptyState icon={Volume2Icon} title="该服务器没有语音频道" description="先在服务器详情中创建语音频道。" />
        )}
        {states.status === "loading" && <LoadingState rows={5} />}
        {states.status === "error" && <ErrorState message={states.error} onRetry={() => states.reload()} />}
        {states.status === "success" && list.length === 0 && (
          <EmptyState icon={MicIcon} title="频道内暂无用户" description="用户进入语音频道后会实时显示在这里。" />
        )}

        {states.status === "success" && list.length > 0 && (
          <div className="flex flex-col gap-2">
            {list.map((state, index) => {
              const name = state.username ?? state.user_id
              const muted = state.server_mute || state.self_mute
              const deafened = state.server_deaf || state.self_deaf
              return (
                <div
                  key={state.user_id}
                  style={{ "--stagger-index": index } as React.CSSProperties}
                  className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 transition-[background-color] hover:bg-muted/40"
                >
                  <Avatar className="size-9">
                    <AvatarFallback>{name.slice(0, 2).toUpperCase()}</AvatarFallback>
                  </Avatar>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{name}</p>
                    <p className="truncate font-mono text-xs text-muted-foreground">{state.user_id}</p>
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    {state.stage_role && (
                      <Badge variant={state.stage_role === "SPEAKER" ? "default" : "secondary"}>
                        {STAGE_ROLE_LABELS[state.stage_role]}
                      </Badge>
                    )}
                    {state.capacity_muted && <Badge variant="outline">容量禁说</Badge>}
                    {muted && (
                      <Badge variant="destructive">
                        <MicOffIcon />
                        {state.server_mute ? "服务器静音" : "自我静音"}
                      </Badge>
                    )}
                    {deafened && (
                      <Badge variant="destructive">
                        <HeadphoneOffIcon />
                        {state.server_deaf ? "服务器聋麦" : "自我聋麦"}
                      </Badge>
                    )}
                    {state.self_stream && (
                      <Badge variant="secondary">
                        <MonitorUpIcon />
                        共享中
                      </Badge>
                    )}
                  </div>
                  <Button variant="destructive" size="sm" onClick={() => onDisconnect(state)}>
                    <PhoneOffIcon data-icon="inline-start" />
                    断开
                  </Button>
                </div>
              )
            })}
          </div>
        )}
      </section>
    </main>
  )
}
