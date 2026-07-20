import { useEffect, useRef, useState } from "react"
import { useOutletContext } from "react-router"
import { SearchIcon } from "lucide-react"

import { MessageItem } from "~/components/message-item"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Input } from "~/components/ui/input"
import { searchMessages, type Message } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"

const ALL_GUILDS = "__all__"

export default function SearchPage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const [query, setQuery] = useState("")
  const [guildFilter, setGuildFilter] = useState<string>(ALL_GUILDS)

  const [results, setResults] = useState<Message[] | null>(null)
  const [status, setStatus] = useState<"idle" | "loading" | "success" | "error">("idle")
  const [error, setError] = useState("")
  const versionRef = useRef(0)

  // 输入防抖 350ms 后触发全系统搜索（结果已按调用者可见范围过滤）
  useEffect(() => {
    const trimmed = query.trim()
    if (!trimmed) {
      setResults(null)
      setStatus("idle")
      return
    }
    const version = ++versionRef.current
    setStatus("loading")
    setError("")
    const timer = setTimeout(async () => {
      try {
        const list = await searchMessages({
          q: trimmed,
          guild_id: guildFilter === ALL_GUILDS ? undefined : guildFilter,
          limit: 50,
        })
        if (versionRef.current !== version) return
        setResults(list)
        setStatus("success")
      } catch (reason) {
        if (versionRef.current !== version) return
        setError(reason instanceof Error ? reason.message : "搜索失败")
        setStatus("error")
      }
    }, 350)
    return () => clearTimeout(timer)
  }, [query, guildFilter])

  const guildNames = new Map(guilds.map(guild => [guild.id, guild.name]))

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="全系统搜索"
        description="跨服务器检索消息正文（仅当前正文，不含历史编辑版本）；结果强制按你的可见范围过滤。"
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-64 flex-1 sm:max-w-md">
            <SearchIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="search"
              aria-label="搜索消息"
              placeholder="输入关键词搜索消息…"
              value={query}
              onChange={event => setQuery(event.target.value)}
              className="pl-9"
              autoFocus
            />
          </div>
          <SimpleSelect
            ariaLabel="服务器范围"
            value={guildFilter}
            onChange={setGuildFilter}
            options={[{ value: ALL_GUILDS, label: "全部服务器" }, ...guilds.map(guild => ({ value: guild.id, label: guild.name }))]}
            className="w-44"
          />
          {status === "success" && results && (
            <span className="text-xs text-muted-foreground">
              命中 <span className="font-medium text-foreground tabular-nums">{results.length}</span> 条
            </span>
          )}
        </div>

        {status === "idle" && (
          <EmptyState icon={SearchIcon} title="输入关键词开始搜索" description="支持按服务器过滤；软删消息与无权限频道不会出现在结果中。" />
        )}
        {status === "loading" && <LoadingState rows={5} />}
        {status === "error" && <ErrorState message={error} onRetry={() => setQuery(current => `${current} `.trimEnd())} />}
        {status === "success" && results && results.length === 0 && (
          <EmptyState title="没有匹配的消息" description="换个关键词试试，或放宽服务器范围。" />
        )}
        {status === "success" && results && results.length > 0 && (
          <div className="flex flex-col gap-2">
            {results.map((message, index) => (
              <div key={message.id} className="flex flex-col gap-1">
                {message.guild_id && (
                  <p className="px-1 text-[10px] text-muted-foreground">{guildNames.get(message.guild_id) ?? message.guild_id}</p>
                )}
                <MessageItem message={message} index={index} />
              </div>
            ))}
          </div>
        )}
      </section>
    </main>
  )
}
