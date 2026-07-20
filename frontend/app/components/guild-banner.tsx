import { useEffect, useState } from "react"

import { cn } from "~/lib/utils"
import type { Guild } from "~/lib/api"

/**
 * 服务器 banner 展示：优先多 banner 列表（position 升序），为空回退旧的单张
 * banner_url，都没有则不渲染。多张时底部渲染指示点可点击切换（简单干净，
 * 不引入轮播库）。
 */
export function GuildBannerHero({ guild, className }: { guild: Guild; className?: string }) {
  const urls =
    guild.banners && guild.banners.length > 0
      ? guild.banners.map(banner => banner.url)
      : guild.banner_url
        ? [guild.banner_url]
        : []
  const [index, setIndex] = useState(0)
  // banner 增删后索引可能越界，收敛回首张
  useEffect(() => {
    if (index >= urls.length) setIndex(0)
  }, [urls.length, index])

  if (urls.length === 0) return null
  const current = urls[Math.min(index, urls.length - 1)]

  return (
    <div className={cn("relative overflow-hidden rounded-2xl border", className)}>
      <img src={current} alt="服务器 banner" className="h-36 w-full object-cover md:h-44" />
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
