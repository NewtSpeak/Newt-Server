import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { ArrowDownIcon, ChevronDownIcon, ChevronUpIcon, DownloadIcon, SearchIcon } from "lucide-react"

import { CopyButton } from "~/components/copy-button"
import { linesToText, type LogLine } from "~/components/sfu-deploy/shared"
import { Button } from "~/components/ui/button"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { Switch } from "~/components/ui/switch"
import { ToggleGroup, ToggleGroupItem } from "~/components/ui/toggle-group"
import { formatBytes } from "~/lib/format"
import { gsap, MOTION, MOTION_OK } from "~/lib/gsap"
import { cn } from "~/lib/utils"

/** 默认渲染的尾部行数；更早的内容按需展开（同终端 scrollback 心智）。 */
const VISIBLE_TAIL = 1500
/** 距底部这个像素内视为「贴底」。 */
const BOTTOM_THRESHOLD = 24
/** 单次追加超过这个行数视为刷屏，不加入场动画。 */
const BURST_LINES = 8
/** 距上次追加短于这个间隔也视为刷屏。 */
const BURST_INTERVAL_MS = 250

type Props = {
  lines: LogLine[]
  running: boolean
  /** 下载文件名用与元信息头。 */
  meta?: { filename: string; header: string }
  /** 紧凑模式：隐藏级别过滤与匹配导航（弹窗内使用）。 */
  compact?: boolean
  className?: string
  /** 日志容器高度类，默认按断点自适应。 */
  heightClassName?: string
}

function reducedMotion() {
  return typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches
}

/** 把一行正文按查询词切分渲染，命中处包 <mark>。 */
function renderBody(body: string, query: string) {
  if (!query) return body
  const lower = body.toLowerCase()
  const needle = query.toLowerCase()
  const parts: React.ReactNode[] = []
  let cursor = 0
  let hit = lower.indexOf(needle)
  let key = 0
  while (hit >= 0) {
    if (hit > cursor) parts.push(body.slice(cursor, hit))
    parts.push(
      <mark key={key++} className="rounded-[3px] bg-amber-400/30 px-0.5 text-inherit">
        {body.slice(hit, hit + query.length)}
      </mark>
    )
    cursor = hit + query.length
    hit = lower.indexOf(needle, cursor)
  }
  if (cursor < body.length) parts.push(body.slice(cursor))
  return parts
}

export function DeployLog({ lines, running, meta, compact, className, heightClassName }: Props) {
  const [query, setQuery] = useState("")
  const [matchIndex, setMatchIndex] = useState(0)
  const [onlyIssues, setOnlyIssues] = useState(false)
  const [pinned, setPinned] = useState(true)
  const [unseen, setUnseen] = useState(0)
  const [showAll, setShowAll] = useState(false)

  const scrollRef = useRef<HTMLDivElement>(null)
  const cardRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const pillRef = useRef<HTMLButtonElement>(null)
  /** 本轮之前已渲染的行数，用于只给新行加入场动画。 */
  const animateFromRef = useRef(0)
  const lastAppendRef = useRef(0)
  const prevCountRef = useRef(0)
  const burstRef = useRef(false)
  /** 未读计数在渲染期累计、effect 里提交，避免在渲染中调用 setState。 */
  const pendingUnseenRef = useRef(0)
  const pinnedRef = useRef(true)
  pinnedRef.current = pinned

  const hidden = Math.max(0, lines.length - VISIBLE_TAIL)
  const windowed = showAll || hidden === 0 ? lines : lines.slice(-VISIBLE_TAIL)
  const visible = useMemo(
    () => (onlyIssues ? windowed.filter(l => l.level === "warn" || l.level === "error") : windowed),
    [windowed, onlyIssues]
  )

  const matches = useMemo(() => {
    if (!query.trim()) return []
    const needle = query.toLowerCase()
    return visible.filter(l => l.body.toLowerCase().includes(needle)).map(l => l.n)
  }, [visible, query])

  // 渲染期同步推进：新行阈值与刷屏判定都必须在本次渲染就绪，
  // 放到 effect 里会晚一帧，导致老行被误判为新行而重播动画。
  if (lines.length !== prevCountRef.current) {
    const added = lines.length - prevCountRef.current
    const now = Date.now()
    // 刷屏（如 apt 输出）时跳过入场动画：几十行同时跑既眼花又掉帧。
    burstRef.current = added >= BURST_LINES || now - lastAppendRef.current < BURST_INTERVAL_MS
    animateFromRef.current = prevCountRef.current
    lastAppendRef.current = now
    prevCountRef.current = lines.length
    if (added > 0 && !pinnedRef.current) pendingUnseenRef.current += added
  }
  const burst = burstRef.current

  useEffect(() => {
    if (pendingUnseenRef.current > 0) {
      const delta = pendingUnseenRef.current
      pendingUnseenRef.current = 0
      setUnseen(count => count + delta)
    }
  }, [lines.length])

  const scrollToBottom = useCallback(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [])

  // 追加时自动滚底；用户正在选择文本或正在搜索时让路。
  useEffect(() => {
    if (!pinned || query) return
    const selection = typeof window !== "undefined" ? window.getSelection() : null
    const selecting =
      selection && !selection.isCollapsed && scrollRef.current?.contains(selection.anchorNode ?? null)
    if (selecting) return
    scrollToBottom()
  }, [lines.length, pinned, query, scrollToBottom])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_THRESHOLD
    setPinned(prev => {
      if (atBottom && !prev) {
        setUnseen(0)
        return true
      }
      if (!atBottom && prev) return false
      return prev
    })
  }, [])

  // 回底 pill 出入场
  useEffect(() => {
    const el = pillRef.current
    if (!el) return
    const media = gsap.matchMedia()
    media.add(MOTION_OK, () => {
      gsap.fromTo(
        el,
        { autoAlpha: 0, y: 8, scale: 0.96 },
        { autoAlpha: 1, y: 0, scale: 1, duration: 0.22, ease: MOTION.ease, clearProps: "all" }
      )
    })
    return () => media.revert()
  }, [pinned, unseen > 0])

  const jumpToLine = useCallback((lineNumber: number) => {
    const el = scrollRef.current?.querySelector<HTMLElement>(`[data-line="${lineNumber}"]`)
    if (!el) return
    el.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "center" })
    const ctx = gsap.context(() => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.fromTo(
          el,
          { backgroundColor: "color-mix(in oklch, var(--ring) 28%, transparent)" },
          { backgroundColor: "rgba(0,0,0,0)", duration: 0.6, ease: "power2.out", clearProps: "backgroundColor" }
        )
      })
    })
    window.setTimeout(() => ctx.revert(), 900)
  }, [])

  const gotoMatch = useCallback(
    (delta: number) => {
      if (matches.length === 0) return
      const next = (matchIndex + delta + matches.length) % matches.length
      setMatchIndex(next)
      jumpToLine(matches[next])
    },
    [matches, matchIndex, jumpToLine]
  )

  useEffect(() => setMatchIndex(0), [query])

  // Ctrl/⌘+F 只在焦点位于日志卡内时拦截，否则放行浏览器原生查找。
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "f") return
      if (!cardRef.current?.contains(document.activeElement)) return
      event.preventDefault()
      searchRef.current?.focus()
      searchRef.current?.select()
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [])

  const download = useCallback(() => {
    const body = linesToText(lines)
    const content = meta?.header ? `${meta.header}\n${"─".repeat(48)}\n${body}\n` : `${body}\n`
    const blob = new Blob([content], { type: "text/plain;charset=utf-8" })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = meta?.filename ?? "newt-sfu-deploy.log"
    anchor.click()
    URL.revokeObjectURL(url)
  }, [lines, meta])

  const byteSize = useMemo(() => new Blob([linesToText(lines)]).size, [lines])

  return (
    <div ref={cardRef} className={cn("flex min-w-0 flex-col gap-2.5", className)}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="mr-auto flex items-baseline gap-2">
          <h3 className="text-sm font-medium">部署输出</h3>
          <p className="text-xs tabular-nums text-muted-foreground">
            {lines.length} 行 · {formatBytes(byteSize)}
          </p>
        </div>

        <div className="relative">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            ref={searchRef}
            type="search"
            value={query}
            onChange={event => setQuery(event.target.value)}
            onKeyDown={event => {
              if (event.key === "Enter") {
                event.preventDefault()
                gotoMatch(event.shiftKey ? -1 : 1)
              }
              if (event.key === "Escape") {
                setQuery("")
                scrollRef.current?.focus()
              }
            }}
            placeholder="搜索日志"
            aria-label="搜索日志"
            aria-describedby="log-match-count"
            className="h-8 w-40 pl-8 text-xs sm:w-52"
          />
        </div>

        {query && (
          <span id="log-match-count" aria-live="polite" className="text-xs tabular-nums text-muted-foreground">
            <span key={`${matchIndex}-${matches.length}`} className="t-text-swap">
              {matches.length === 0 ? "无匹配" : `${matchIndex + 1}/${matches.length}`}
            </span>
          </span>
        )}
        {query && matches.length > 0 && !compact && (
          <>
            <Button variant="ghost" size="icon-sm" aria-label="上一个匹配" onClick={() => gotoMatch(-1)}>
              <ChevronUpIcon />
            </Button>
            <Button variant="ghost" size="icon-sm" aria-label="下一个匹配" onClick={() => gotoMatch(1)}>
              <ChevronDownIcon />
            </Button>
          </>
        )}

        {!compact && (
          <ToggleGroup
            value={[onlyIssues ? "issues" : "all"]}
            onValueChange={value => setOnlyIssues(value[0] === "issues")}
            className="h-8"
          >
            <ToggleGroupItem value="all" className="h-8 px-2.5 text-xs">
              全部
            </ToggleGroupItem>
            <ToggleGroupItem value="issues" className="h-8 px-2.5 text-xs">
              警告+错误
            </ToggleGroupItem>
          </ToggleGroup>
        )}

        {running && (
          <div className="flex items-center gap-1.5">
            <Switch
              id="log-autoscroll"
              checked={pinned}
              onCheckedChange={next => {
                setPinned(next)
                if (next) {
                  setUnseen(0)
                  scrollToBottom()
                }
              }}
            />
            <Label htmlFor="log-autoscroll" className="text-xs font-normal text-muted-foreground">
              自动滚动
            </Label>
          </div>
        )}

        <CopyButton text={linesToText(visible)} label="复制日志" />
        <Button variant="outline" size="sm" onClick={download} className="h-8">
          <DownloadIcon data-icon="inline-start" />
          下载
        </Button>
      </div>

      <div className="relative min-w-0">
        {running && (
          <span
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-x-0 top-0 z-10 h-0.5 overflow-hidden rounded-t-xl",
              "transition-opacity duration-200",
              burst ? "opacity-100" : "opacity-0"
            )}
          >
            <span className="log-shimmer block h-full w-1/3 bg-gradient-to-r from-transparent via-primary/70 to-transparent" />
          </span>
        )}

        <div
          ref={scrollRef}
          onScroll={onScroll}
          tabIndex={0}
          role="log"
          aria-live="off"
          aria-label={`部署输出，共 ${lines.length} 行`}
          className={cn(
            "min-w-0 overflow-auto rounded-xl bg-muted/50 ring-1 ring-border ring-inset outline-none dark:bg-background",
            "focus-visible:ring-3 focus-visible:ring-ring/30",
            heightClassName ?? "h-[clamp(360px,48vh,640px)]"
          )}
        >
          {hidden > 0 && !showAll && (
            <button
              type="button"
              onClick={() => setShowAll(true)}
              className="w-full border-b border-border/60 px-3 py-2 text-xs text-muted-foreground transition-colors hover:bg-foreground/[0.04] hover:text-foreground"
            >
              加载更早的 {hidden} 行
            </button>
          )}

          {visible.length === 0 ? (
            <p className="px-3 py-4 font-mono text-xs text-muted-foreground">
              {lines.length === 0 ? "等待输出…" : "当前过滤条件下没有内容"}
            </p>
          ) : (
            <ol className="py-2 font-mono text-xs leading-[1.55] tabular-nums">
              {visible.map(line => (
                <li
                  key={line.n}
                  data-line={line.n}
                  data-level={line.level}
                  className={cn(
                    "grid grid-cols-[3ch_1fr] gap-3 border-l-2 border-transparent py-px pr-3 pl-[calc(0.75rem-2px)]",
                    "hover:bg-foreground/[0.035]",
                    !burst && line.n > animateFromRef.current && "log-line-in",
                    "data-[level=warn]:border-amber-500/70 data-[level=warn]:bg-amber-500/[0.05] data-[level=warn]:text-amber-700 dark:data-[level=warn]:text-amber-400",
                    "data-[level=error]:border-destructive data-[level=error]:bg-destructive/[0.07] data-[level=error]:text-destructive",
                    "data-[level=notice]:text-foreground",
                    "data-[level=info]:text-foreground/80",
                    "data-[level=meta]:text-muted-foreground data-[level=meta]:italic"
                  )}
                >
                  <span aria-hidden className="text-right text-muted-foreground/60 select-none">
                    {line.n}
                  </span>
                  <span className="break-words whitespace-pre-wrap">
                    {line.level === "notice" && (
                      <span aria-hidden className="mr-1.5 text-muted-foreground/50">
                        ›
                      </span>
                    )}
                    {line.prefix && <span className="font-semibold">{line.prefix} </span>}
                    {renderBody(line.body, query)}
                  </span>
                </li>
              ))}
            </ol>
          )}
        </div>

        {!pinned && unseen > 0 && (
          <button
            ref={pillRef}
            type="button"
            onClick={() => {
              setPinned(true)
              setUnseen(0)
              scrollToBottom()
            }}
            aria-label={`滚动到底部，有 ${unseen} 行新输出`}
            className="absolute inset-x-0 bottom-3 mx-auto flex w-fit items-center gap-1.5 rounded-3xl border bg-card/95 px-3 py-1.5 text-xs shadow-md backdrop-blur transition-colors hover:bg-muted active:scale-[0.96]"
          >
            <ArrowDownIcon className="size-3.5" />
            <span key={unseen} className="t-number-pop font-medium tabular-nums">
              {unseen}
            </span>{" "}
            行新输出
          </button>
        )}
      </div>
    </div>
  )
}

/** 供外部（终态卡「跳到第一条错误」）复用的滚动定位。 */
export function scrollToLogLine(container: HTMLElement | null, lineNumber: number) {
  const el = container?.querySelector<HTMLElement>(`[data-line="${lineNumber}"]`)
  el?.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "center" })
}
