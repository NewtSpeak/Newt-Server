import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react"
import { useOutletContext } from "react-router"
import { HashIcon, HistoryIcon, SendIcon } from "lucide-react"
import { toast } from "sonner"

import { MessageItem } from "~/components/message-item"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
import { Input } from "~/components/ui/input"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { useGuildID } from "~/hooks/use-guild-id"
import {
  listChannels,
  listMembers,
  listMessages,
  memberName,
  sendMessage,
  type Channel,
  type GuildMember,
  type Message,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"

const PAGE_SIZE = 50

export default function MessagesPage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)
  const [channelID, setChannelID] = useState<string | null>(null)

  const channels = useAsyncData<Channel[]>(
    guildID ? () => listChannels(guildID) : null,
    [guildID]
  )
  const textChannels = useMemo(
    () => (channels.data ?? []).filter((channel) => channel.type === "TEXT"),
    [channels.data]
  )
  // 成员名映射：ephemeral「仅 @xxx 可见」显示名字（加载失败静默回退 uuid 前 8 位）
  const members = useAsyncData<GuildMember[]>(
    guildID ? () => listMembers(guildID) : null,
    [guildID]
  )
  const memberNames = useMemo(() => {
    const map: Record<string, string> = {}
    for (const member of members.data ?? [])
      map[member.user_id] = memberName(member)
    return map
  }, [members.data])
  const activeChannel = channelID ?? textChannels[0]?.id ?? null

  // 消息流手动管理：支持「加载更早」游标分页与实时追加
  const [messages, setMessages] = useState<Message[]>([])
  const [status, setStatus] = useState<
    "idle" | "loading" | "success" | "error"
  >("idle")
  const [error, setError] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const channelRef = useRef(activeChannel)
  channelRef.current = activeChannel

  const loadLatest = useCallback(async (channel: string, silent = false) => {
    if (!silent) setStatus("loading")
    setError("")
    try {
      const list = await listMessages(channel, { limit: PAGE_SIZE })
      if (channelRef.current !== channel) return
      setMessages(list)
      setHasMore(list.length >= PAGE_SIZE)
      setStatus("success")
    } catch (reason) {
      if (channelRef.current !== channel) return
      setError(reason instanceof Error ? reason.message : "消息加载失败")
      setStatus("error")
    }
  }, [])

  useEffect(() => {
    if (!activeChannel) {
      setMessages([])
      setStatus("idle")
      return
    }
    loadLatest(activeChannel)
  }, [activeChannel, loadLatest])

  useGatewayEvent(
    ["MESSAGE_CREATE", "MESSAGE_UPDATE", "MESSAGE_DELETE"],
    (payload) => {
      const data = payload as { channel_id?: string } | undefined
      if (
        activeChannel &&
        (!data?.channel_id || data.channel_id === activeChannel)
      )
        loadLatest(activeChannel, true)
    }
  )

  async function loadOlder() {
    if (!activeChannel || messages.length === 0) return
    setLoadingMore(true)
    try {
      const oldest = messages[messages.length - 1]
      const older = await listMessages(activeChannel, {
        before: oldest.id,
        limit: PAGE_SIZE,
      })
      setMessages((current) => [...current, ...older])
      setHasMore(older.length >= PAGE_SIZE)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "加载更早消息失败")
    } finally {
      setLoadingMore(false)
    }
  }

  const [sending, setSending] = useState(false)

  async function onSend(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!activeChannel) return
    const form = event.currentTarget
    const content = String(
      new FormData(form).get("message-content") ?? ""
    ).trim()
    if (!content) return
    setSending(true)
    try {
      await sendMessage(activeChannel, content)
      form.reset()
      loadLatest(activeChannel, true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "消息发送失败")
    } finally {
      setSending(false)
    }
  }

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="频道消息"
        description="按频道浏览消息流：编辑次数徽章可查看全文快照历史，软删消息保留墓碑记录。"
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={(next) => {
              setGuildID(next)
              setChannelID(null)
            }}
            options={guilds.map((guild) => ({
              value: guild.id,
              label: guild.name,
            }))}
            className="w-52"
          />
          <SimpleSelect
            ariaLabel="选择文字频道"
            placeholder="选择文字频道"
            value={activeChannel}
            onChange={setChannelID}
            options={textChannels.map((channel) => ({
              value: channel.id,
              label: `# ${channel.name}`,
            }))}
            className="w-52"
            disabled={textChannels.length === 0}
          />
        </div>

        {channels.status === "success" && textChannels.length === 0 && (
          <EmptyState
            icon={HashIcon}
            title="该服务器没有文字频道"
            description="先在服务器详情中创建文字频道。"
          />
        )}

        {activeChannel && (
          <>
            <form onSubmit={onSend} className="flex items-center gap-2">
              <Input
                name="message-content"
                aria-label="消息内容"
                placeholder="以管理员身份发送一条消息…"
                maxLength={4000}
                className="flex-1"
              />
              <Button type="submit" disabled={sending}>
                <SendIcon data-icon="inline-start" />
                {sending ? "发送中…" : "发送"}
              </Button>
            </form>

            {status === "loading" && <LoadingState rows={6} />}
            {status === "error" && (
              <ErrorState
                message={error}
                onRetry={() => loadLatest(activeChannel)}
              />
            )}
            {status === "success" && messages.length === 0 && (
              <EmptyState
                icon={HistoryIcon}
                title="频道还没有消息"
                description="发送第一条消息，或等待成员发言。"
              />
            )}
            {status === "success" && messages.length > 0 && (
              <div className="flex flex-col gap-2">
                {messages.map((message, index) => (
                  <MessageItem
                    key={message.id}
                    message={message}
                    index={index}
                    memberNames={memberNames}
                  />
                ))}
                {hasMore && (
                  <Button
                    variant="outline"
                    onClick={loadOlder}
                    disabled={loadingMore}
                    className="mx-auto mt-2"
                  >
                    {loadingMore ? "加载中…" : "加载更早消息"}
                  </Button>
                )}
              </div>
            )}
          </>
        )}
      </section>
    </main>
  )
}
