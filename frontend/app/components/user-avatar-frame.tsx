import type { EquippedSlotView } from "~/lib/api"
import { cn } from "~/lib/utils"

/**
 * 头像框包装组件：relative 容器包住现有 Avatar，
 * 有框时叠加 absolute 外扩 18% 的框层（不改变布局尺寸），无框时原样渲染。
 * 框层 pointer-events-none 且 z 高于头像；mime 为 video/* 时用静音循环视频。
 * 注意：父容器不能 overflow-hidden，否则外扩部分会被裁切。
 */
export function FramedAvatar({
  frame,
  className,
  children,
}: {
  /** 用户装备的头像框；null/undefined = 无框，原样渲染 children */
  frame?: EquippedSlotView | null
  className?: string
  children: React.ReactNode
}) {
  const asset = frame?.assets?.primary
  if (!asset?.url) return <>{children}</>

  const overlayClass =
    "pointer-events-none absolute inset-[-18%] z-10 size-auto max-w-none select-none"

  return (
    <span className={cn("relative inline-flex shrink-0", className)}>
      {children}
      {asset.mime?.startsWith("video/") ? (
        <video
          src={asset.url}
          muted
          loop
          autoPlay
          playsInline
          aria-hidden
          className={cn(overlayClass, "h-[136%] w-[136%] object-contain")}
        />
      ) : (
        <img
          src={asset.url}
          alt=""
          aria-hidden
          draggable={false}
          className={cn(overlayClass, "h-[136%] w-[136%] object-contain")}
        />
      )}
    </span>
  )
}
