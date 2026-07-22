import { useCallback, useEffect, useRef, useState } from "react"

import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import type { Guild } from "~/lib/api"
import { cn, isGuildMediaVideo } from "~/lib/utils"

/**
 * 服务器头像（Discord 风格）：有 icon_url 显示图片/MP4，否则回退服务器名首字
 * （中文取第一个字符，英文取首字母大写）。圆角方形与列表卡片视觉一致。
 * MP4 默认静音循环；悬浮/聚焦时解除静音。
 */
export function GuildAvatar({
  guild,
  className,
}: {
  guild: Pick<Guild, "name" | "icon_url">
  className?: string
}) {
  const initial = (guild.name ?? "?").trim().charAt(0).toUpperCase() || "?"
  const icon = guild.icon_url?.trim()
  const isVideo = Boolean(icon && isGuildMediaVideo(icon))
  const videoRef = useRef<HTMLVideoElement>(null)
  const [unmuted, setUnmuted] = useState(false)

  useEffect(() => {
    setUnmuted(false)
  }, [icon])

  useEffect(() => {
    const el = videoRef.current
    if (!el || !isVideo) return
    void el.play().catch(() => {
      /* 自动播放被拦截时忽略 */
    })
  }, [isVideo, unmuted, icon])

  const onHoverStart = useCallback(() => {
    if (!isVideo) return
    setUnmuted(true)
  }, [isVideo])

  const onHoverEnd = useCallback(() => {
    if (!isVideo) return
    setUnmuted(false)
  }, [isVideo])

  if (icon && isVideo) {
    return (
      <span
        className={cn("relative inline-flex shrink-0 overflow-hidden rounded-xl", className)}
        onMouseEnter={onHoverStart}
        onMouseLeave={onHoverEnd}
        onFocus={onHoverStart}
        onBlur={onHoverEnd}
      >
        <video
          ref={videoRef}
          src={icon}
          autoPlay
          loop
          muted={!unmuted}
          playsInline
          preload="metadata"
          aria-label={`${guild.name} 图标`}
          className="size-full object-cover"
        />
      </span>
    )
  }

  return (
    <Avatar className={cn("rounded-xl after:rounded-xl", className)}>
      {icon && <AvatarImage src={icon} alt={`${guild.name} 图标`} className="rounded-xl" />}
      <AvatarFallback className="rounded-xl bg-primary/10 font-medium text-primary">{initial}</AvatarFallback>
    </Avatar>
  )
}
