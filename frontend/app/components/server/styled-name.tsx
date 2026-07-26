import type { CSSProperties, ReactNode } from "react"
import { useTheme } from "next-themes"

import { Tooltip, TooltipContent, TooltipTrigger } from "~/components/ui/tooltip"
import type { MemberBadgeView, RoleBadgeStyle, RoleStyle, RoleSurfaceStyle } from "~/lib/api"
import { resolveRoleIconStyle } from "~/lib/api"
import { cn } from "~/lib/utils"

const DEFAULT_SPEED = 4

function animDurationSec(style: RoleSurfaceStyle | RoleStyle | undefined | null): number {
  const s = style?.speed
  if (typeof s === "number" && s >= 0.5 && s <= 20) return s
  return DEFAULT_SPEED
}

/** 当前是否暗色主题（跟随 next-themes 切换） */
function useIsDark(): boolean {
  const { resolvedTheme } = useTheme()
  return resolvedTheme === "dark"
}

/** 按亮暗主题选色：暗色主题优先 colors_dark，缺省共用 colors */
function surfaceColors(style: RoleSurfaceStyle, dark: boolean): string[] {
  if (dark && style.colors_dark?.length) return style.colors_dark
  return style.colors ?? []
}

function gradientImage(style: RoleSurfaceStyle, dark: boolean): string | undefined {
  const base = surfaceColors(style, dark)
  if (!style.type || !base.length) return undefined
  if (style.type === "solid") return undefined
  const colors = style.animated ? [...base, base[0]] : base
  const stops = colors.join(", ")
  return style.type === "radial"
    ? `radial-gradient(${style.shape ?? "circle"}, ${stops})`
    : `linear-gradient(${style.angle ?? 90}deg, ${stops})`
}

/** RoleStyle / 表面样式 → 文本渲染 CSS；dark 为当前是否暗色主题 */
export function roleStyleCss(
  style: RoleStyle | RoleSurfaceStyle | Record<string, never> | undefined | null,
  dark = false,
): {
  style: CSSProperties
  className: string
} {
  const parsed = style as RoleSurfaceStyle | undefined
  if (!parsed?.type || !parsed.colors?.length) return { style: {}, className: "" }
  if (parsed.type === "solid") {
    return { style: { color: surfaceColors(parsed, dark)[0] }, className: "" }
  }
  const image = gradientImage(parsed, dark)
  const duration = animDurationSec(parsed)
  return {
    style: {
      backgroundImage: image,
      WebkitBackgroundClip: "text",
      backgroundClip: "text",
      color: "transparent",
      ...(parsed.animated
        ? {
            backgroundSize: "200% 200%",
            animationDuration: `${duration}s`,
          }
        : {}),
    },
    className: parsed.animated ? "name-gradient-animated" : "",
  }
}

/** 角色色点 / icon 填充 CSS（背景渐变，非文字 clip）；dark 为当前是否暗色主题 */
export function roleIconFillCss(
  style: RoleSurfaceStyle | null | undefined,
  dark = false,
): {
  style: CSSProperties
  className: string
} {
  if (!style?.type || !style.colors?.length) {
    return { style: {}, className: "" }
  }
  if (style.type === "solid") {
    return {
      style: { backgroundColor: surfaceColors(style, dark)[0] },
      className: "",
    }
  }
  const image = gradientImage(style, dark)
  const duration = animDurationSec(style)
  return {
    style: {
      backgroundImage: image,
      backgroundColor: "transparent",
      ...(style.animated
        ? {
            backgroundSize: "200% 200%",
            animationDuration: `${duration}s`,
          }
        : {}),
    },
    className: style.animated ? "name-gradient-animated" : "",
  }
}

function textDecorClass(style: {
  bold?: boolean
  italic?: boolean
  underline?: boolean
  strikethrough?: boolean
} | null | undefined) {
  return cn(
    style?.bold && "font-bold",
    style?.italic && "italic",
    style?.underline && "underline",
    style?.strikethrough && "line-through",
  )
}

/** 按角色样式渲染的用户名/角色名 */
export function StyledName({
  nameStyle,
  className,
  children,
}: {
  nameStyle: RoleStyle | RoleSurfaceStyle | Record<string, never> | undefined | null
  className?: string
  children: ReactNode
}) {
  const dark = useIsDark()
  const css = roleStyleCss(nameStyle, dark)
  const decor = nameStyle as RoleStyle | undefined
  return (
    <span
      className={cn(css.className, textDecorClass(decor), className)}
      style={css.style}
    >
      {children}
    </span>
  )
}

/**
 * 角色色点 / icon：支持纯色、线性/径向渐变与流动动画。
 * 传入完整 RoleStyle 时按 icon_sync / icon 独立配置解析。
 */
export function RoleStyleIcon({
  style,
  className,
  title,
  fallbackColor,
}: {
  style?: RoleStyle | RoleSurfaceStyle | null
  className?: string
  title?: string
  /** 无高级样式时的回退纯色 */
  fallbackColor?: string
}) {
  // 完整 RoleStyle（含 icon_sync）与表面样式分支
  const isFull =
    style &&
    typeof style === "object" &&
    ("icon_sync" in style || "icon" in style || (style as RoleStyle).type !== undefined)

  let surface: RoleSurfaceStyle | null = null
  if (style && isFull && ("icon_sync" in style || "icon" in style)) {
    surface = resolveRoleIconStyle(style as RoleStyle)
  } else if (style && (style as RoleSurfaceStyle).type) {
    surface = style as RoleSurfaceStyle
  } else if (style && (style as RoleStyle).type) {
    surface = resolveRoleIconStyle(style as RoleStyle)
  }

  const dark = useIsDark()
  const fill = roleIconFillCss(surface, dark)
  const bg =
    fill.style.backgroundImage || fill.style.backgroundColor
      ? fill.style
      : fallbackColor
        ? { backgroundColor: fallbackColor }
        : { backgroundColor: "transparent" }

  return (
    <span
      title={title}
      aria-hidden={!title}
      className={cn(
        "inline-block size-3 shrink-0 rounded-full border border-black/10 dark:border-white/15",
        fill.className,
        className,
      )}
      style={bg}
    />
  )
}

/** 角色徽章 pill（背景图/渐变 + 可选自定义 icon + 角色名） */
export function RoleBadgePill({
  name,
  badge,
  fallbackColor,
  className,
}: {
  name: string
  badge?: RoleBadgeStyle | null
  fallbackColor?: string
  className?: string
}) {
  const dark = useIsDark()
  const bg = badge?.background
  const fill = roleIconFillCss(bg ?? undefined, dark)
  const bgImageUrl = badge?.background_image_url?.trim()
  const hasGradient = Boolean(fill.style.backgroundImage || fill.style.backgroundColor)
  const hasBgImage = Boolean(bgImageUrl)
  const showName = badge?.show_name !== false
  const iconUrl = badge?.icon_url?.trim()

  if (!badge?.enabled && !iconUrl && !hasGradient && !hasBgImage) {
    return (
      <span
        className={cn(
          "inline-flex h-4 max-w-[7rem] items-center gap-0.5 truncate rounded-full px-1.5 text-[10px] font-medium",
          className,
        )}
        style={{
          backgroundColor: fallbackColor ? `${fallbackColor}22` : undefined,
          color: fallbackColor || undefined,
          border: fallbackColor ? `1px solid ${fallbackColor}55` : undefined,
        }}
        title={name}
      >
        <span className={cn("truncate", textDecorClass(badge))}>{name}</span>
      </span>
    )
  }

  // 背景图 + 可选渐变叠加：linear-gradient(...), url(...)
  const layers: string[] = []
  if (fill.style.backgroundImage) {
    layers.push(String(fill.style.backgroundImage))
  }
  if (bgImageUrl) {
    layers.push(`url(${JSON.stringify(bgImageUrl)})`)
  }

  const style: CSSProperties = {
    color: badge?.text_color || "#fff",
    ...(layers.length
      ? {
          backgroundImage: layers.join(", "),
          backgroundSize: layers.map(() => "cover").join(", "),
          backgroundPosition: layers.map(() => "center").join(", "),
          backgroundRepeat: "no-repeat",
          ...(fill.style.animationDuration
            ? { animationDuration: fill.style.animationDuration }
            : {}),
        }
      : fill.style.backgroundColor
        ? { backgroundColor: fill.style.backgroundColor }
        : {
            backgroundColor: fallbackColor || "var(--muted)",
            color: badge?.text_color || (fallbackColor ? "#fff" : undefined),
          }),
  }

  const nameClass = cn("truncate", textDecorClass(badge))

  return (
    <span
      className={cn(
        "inline-flex h-4 max-w-[8rem] items-center gap-0.5 truncate rounded-full px-1.5 text-[10px] font-medium",
        fill.className,
        className,
      )}
      style={style}
      title={name}
    >
      {iconUrl ? (
        <img src={iconUrl} alt="" className="size-3 shrink-0 rounded-sm object-contain" draggable={false} />
      ) : null}
      {showName ? <span className={nameClass}>{name}</span> : null}
      {!showName && !iconUrl ? <span className={nameClass}>{name}</span> : null}
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
