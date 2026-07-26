import { useEffect, useState } from "react"
import { useTheme } from "next-themes"
import { MoonIcon, SunIcon } from "lucide-react"

import { Button } from "~/components/ui/button"

/** 亮暗主题快捷切换：icon swap 交叉淡入淡出，与设置页「外观」共用 next-themes 状态 */
export function ThemeToggle({ className }: { className?: string }) {
  const { resolvedTheme, setTheme } = useTheme()
  const [mounted, setMounted] = useState(false)

  useEffect(() => setMounted(true), [])

  // 挂载前 resolvedTheme 不可用，按全局默认暗色渲染，避免水合闪烁
  const isDark = mounted ? resolvedTheme === "dark" : true
  const label = isDark ? "切换到亮色主题" : "切换到暗色主题"

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      title={label}
      className={className}
      onClick={() => setTheme(isDark ? "light" : "dark")}
    >
      <span className="t-icon-swap size-4">
        <MoonIcon className="size-4" data-state={isDark ? "visible" : "hidden"} />
        <SunIcon className="size-4" data-state={isDark ? "hidden" : "visible"} />
      </span>
    </Button>
  )
}
