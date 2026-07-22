import { useCallback, useEffect, useRef, useState } from "react"

import { cn, isGuildMediaVideo } from "~/lib/utils"
import type { Guild } from "~/lib/api"

/**
 * 服务器 banner 展示：优先多 banner 列表（position 升序），为空回退旧的单张
 * banner_url，都没有则不渲染。多张时底部渲染指示点可点击切换（简单干净，
 * 不引入轮播库）。支持图片 / MP4 混排：视频默认静音循环，悬浮解除静音。
 */
export function GuildBannerHero({ guild, className }: { guild: Guild; className?: string }) {
  const urls =
    guild.banners && guild.banners.length > 0
      ? guild.banners.map(banner => banner.url)
      : guild.banner_url
        ? [guild.banner_url]
        : []
  const [index, setIndex] = useState(0)
  const [unmuted, setUnmuted] = useState(false)
  const videoRef = useRef<HTMLVideoElement>(null)

  // banner 增删后索引可能越界，收敛回首张
  useEffect(() => {
    if (index >= urls.length) setIndex(0)
  }, [urls.length, index])

  // 切换幻灯片时恢复静音
  useEffect(() => {
    setUnmuted(false)
  }, [index])

  const safeIndex = urls.length > 0 ? Math.min(index, urls.length - 1) : 0
  const current = urls[safeIndex] ?? ""
  const isVideo = isGuildMediaVideo(current)

  useEffect(() => {
    const el = videoRef.current
    if (!el || !isVideo) return
    void el.play().catch(() => {
      /* 自动播放策略拦截时忽略 */
    })
  }, [isVideo, unmuted, current])

  const onHoverStart = useCallback(() => {
    if (!isVideo) return
    setUnmuted(true)
  }, [isVideo])

  const onHoverEnd = useCallback(() => {
    if (!isVideo) return
    setUnmuted(false)
  }, [isVideo])

  if (urls.length === 0) return null

  return (
    <div
      className={cn("relative overflow-hidden rounded-2xl border", className)}
      onMouseEnter={onHoverStart}
      onMouseLeave={onHoverEnd}
      onFocus={onHoverStart}
      onBlur={onHoverEnd}
    >
      {isVideo ? (
        <video
          key={current}
          ref={videoRef}
          src={current}
          autoPlay
          loop
          muted={!unmuted}
          playsInline
          preload="metadata"
          aria-label="服务器 banner"
          className="h-36 w-full object-cover md:h-44"
        />
      ) : (
        <img src={current} alt="服务器 banner" className="h-36 w-full object-cover md:h-44" />
      )}
      {urls.length > 1 && (
        <div className="absolute inset-x-0 bottom-2 flex justify-center gap-1.5">
          {urls.map((url, dot) => (
            <button
              key={url}
              type="button"
              aria-label={`第 ${dot + 1} 张 banner`}
              onClick={() => setIndex(dot)}
              className={cn(
                "size-2 rounded-full bg-white/50 shadow-sm transition-[background-color,transform]",
                dot === index && "scale-110 bg-white"
              )}
            />
          ))}
        </div>
      )}
    </div>
  )
}
