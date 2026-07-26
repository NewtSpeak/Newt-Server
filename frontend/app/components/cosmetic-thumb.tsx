import { ImageIcon } from "lucide-react"

import type { CosmeticItem, CosmeticTag } from "~/lib/api"
import { previewMedia } from "~/lib/cosmetics"
import { cn } from "~/lib/utils"

/**
 * 单品预览缩略图：视频 mime 用静音循环 video 标签，其余用 img；无预览资产给占位图标。
 */
export function CosmeticThumb({ item, className }: { item: CosmeticItem; className?: string }) {
  const media = previewMedia(item)
  const frame = cn("size-12 shrink-0 overflow-hidden rounded-lg border bg-muted", className)
  if (!media) {
    return (
      <div className={cn(frame, "grid place-items-center text-muted-foreground")}>
        <ImageIcon className="size-4" />
      </div>
    )
  }
  if (media.isVideo) {
    return <video src={media.url} muted loop autoPlay playsInline className={cn(frame, "object-cover")} />
  }
  return <img src={media.url} alt="" className={cn(frame, "object-cover")} />
}

/** 标签 Badge：背景取 tag.color（无色时回退 secondary 风格） */
export function CosmeticTagBadge({ tag }: { tag: CosmeticTag }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center rounded-3xl px-2 text-xs font-medium whitespace-nowrap",
        tag.color ? "text-white" : "bg-secondary text-secondary-foreground"
      )}
      style={tag.color ? { backgroundColor: tag.color } : undefined}
    >
      {tag.name}
    </span>
  )
}
