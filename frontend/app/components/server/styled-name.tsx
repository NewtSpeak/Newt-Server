import type { CSSProperties, ReactNode } from "react"

import { Tooltip, TooltipContent, TooltipTrigger } from "~/components/ui/tooltip"
import type { MemberBadgeView, RoleStyle } from "~/lib/api"
import { cn } from "~/lib/utils"

/** RoleStyle → 文本渲染 CSS（纯色 / 线性 / 多色 / 径向渐变；animated 走 CSS 动画类） */
export function roleStyleCss(style: RoleStyle | Record<string, never> | undefined | null): {
  style: CSSProperties
  className: string
} {
  const parsed = style as RoleStyle | undefined
  if (!parsed?.type || !parsed.colors?.length) return { style: {}, className: "" }
  if (parsed.type === "solid") {
    return { style: { color: parsed.colors[0] }, className: "" }
  }
  // 渐变文本：background-clip: text；动画流动时首尾同色避免跳变。
  const colors = parsed.animated ? [...parsed.colors, parsed.colors[0]] : parsed.colors
  const stops = colors.join(", ")
  const image =
    parsed.type === "radial"
      ? `radial-gradient(${parsed.shape ?? "circle"}, ${stops})`
      : `linear-gradient(${parsed.angle ?? 90}deg, ${stops})`
  return {
    style: {
      backgroundImage: image,
      WebkitBackgroundClip: "text",
      backgroundClip: "text",
      color: "transparent",
    },
    className: parsed.animated ? "name-gradient-animated" : "",
  }
}

/** 按角色样式渲染的用户名/角色名 */
export function StyledName({
  nameStyle,
  className,
  children,
}: {
  nameStyle: RoleStyle | Record<string, never> | undefined | null
  className?: string
  children: ReactNode
}) {
  const css = roleStyleCss(nameStyle)
  return (
    <span className={cn(css.className, className)} style={css.style}>
      {children}
    </span>
  )
}

function formatExpiry(expiresAt: string | null | undefined) {
  if (!expiresAt) return "永久有效"
  const date = new Date(expiresAt)
  if (Number.isNaN(date.getTime())) return "永久有效"
  return `${date.toLocaleString()} 到期`
}

/** 成员徽章小徽标（悬浮显示描述与有效期） */
export function MemberBadgeChip({ badge }: { badge: MemberBadgeView }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            className="inline-flex h-5 shrink-0 cursor-default items-center gap-1 rounded-full border px-1.5 text-[11px] font-medium"
            style={badge.color ? { borderColor: `${badge.color}66`, color: badge.color, backgroundColor: `${badge.color}14` } : undefined}
          />
        }
      >
        {badge.icon_url ? (
          <img src={badge.icon_url} alt="" className="size-3 rounded-full object-cover" />
        ) : (
          badge.emoji && <span aria-hidden>{badge.emoji}</span>
        )}
        {badge.name}
      </TooltipTrigger>
      <TooltipContent>
        <p className="font-medium">{badge.name}</p>
        {badge.description && <p className="text-muted-foreground">{badge.description}</p>}
        <p className="text-muted-foreground">{formatExpiry(badge.expires_at)}</p>
      </TooltipContent>
    </Tooltip>
  )
}
