import { useEffect, useState } from "react"

import { formatCountdown } from "~/lib/format"
import { cn } from "~/lib/utils"

/** 剩余时间倒计时（每秒刷新，tabular-nums 防抖动） */
export function Countdown({ expiresAt, onExpire, className }: { expiresAt?: string | null; onExpire?: () => void; className?: string }) {
  const [text, setText] = useState(() => formatCountdown(expiresAt))

  useEffect(() => {
    setText(formatCountdown(expiresAt))
    if (!expiresAt) return
    const timer = setInterval(() => {
      const next = formatCountdown(expiresAt)
      setText(previous => {
        if (previous !== null && next === null) onExpire?.()
        return next
      })
    }, 1000)
    return () => clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expiresAt])

  return (
    <span className={cn("tabular-nums", text === null && "text-muted-foreground", className)}>
      {text === null ? "已过期" : text}
    </span>
  )
}
