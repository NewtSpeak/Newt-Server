const dateTimeFormatter = new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
})

const fullFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
})

export function formatTime(value?: string | null) {
  if (!value) return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "—"
  return dateTimeFormatter.format(date)
}

export function formatFullTime(value?: string | null) {
  if (!value) return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "—"
  return fullFormatter.format(date)
}

export function formatRelative(value?: string | null) {
  if (!value) return "—"
  const time = new Date(value).getTime()
  if (Number.isNaN(time)) return "—"
  const diff = Date.now() - time
  const abs = Math.abs(diff)
  const suffix = diff >= 0 ? "前" : "后"
  if (abs < 60_000) return diff >= 0 ? "刚刚" : "1 分钟内"
  if (abs < 3_600_000) return `${Math.floor(abs / 60_000)} 分钟${suffix}`
  if (abs < 86_400_000) return `${Math.floor(abs / 3_600_000)} 小时${suffix}`
  return `${Math.floor(abs / 86_400_000)} 天${suffix}`
}

/** 剩余时间倒计时文案；已过期返回 null */
export function formatCountdown(expiresAt?: string | null): string | null {
  if (!expiresAt) return "长期"
  const remaining = new Date(expiresAt).getTime() - Date.now()
  if (Number.isNaN(remaining) || remaining <= 0) return null
  const seconds = Math.floor(remaining / 1000)
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  const secs = seconds % 60
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分`
  if (minutes > 0) return `${minutes} 分 ${secs} 秒`
  return `${secs} 秒`
}

/** 两个时间点之间的耗时文案（如「1 分 47 秒」）；无效返回 "—" */
export function formatDuration(from?: string | null, to?: string | null): string {
  if (!from) return "—"
  const start = new Date(from).getTime()
  const end = to ? new Date(to).getTime() : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end)) return "—"
  return formatSeconds(Math.max(0, Math.round((end - start) / 1000)))
}

/** 秒数 → 「1 小时 2 分」/「1 分 47 秒」/「12 秒」 */
export function formatSeconds(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—"
  const hours = Math.floor(seconds / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  const secs = Math.floor(seconds % 60)
  if (hours > 0) return `${hours} 小时 ${minutes} 分`
  if (minutes > 0) return `${minutes} 分 ${secs} 秒`
  return `${secs} 秒`
}

/** 秒数 → 计时器格式 mm:ss（超过 1 小时为 h:mm:ss）；配 tabular-nums 使用 */
export function formatElapsed(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "00:00"
  const total = Math.floor(seconds)
  const hours = Math.floor(total / 3_600)
  const minutes = Math.floor((total % 3_600) / 60)
  const secs = total % 60
  const pad = (n: number) => String(n).padStart(2, "0")
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(secs)}` : `${pad(minutes)}:${pad(secs)}`
}

export function formatBytes(size?: number) {
  if (!size && size !== 0) return "—"
  if (size < 1024) return `${size} B`
  if (size < 1024 ** 2) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 ** 3) return `${(size / 1024 ** 2).toFixed(1)} MB`
  return `${(size / 1024 ** 3).toFixed(1)} GB`
}
