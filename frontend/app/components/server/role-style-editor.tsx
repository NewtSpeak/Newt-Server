import { PlusIcon, XIcon } from "lucide-react"

import { StyledName } from "~/components/server/styled-name"
import { Button } from "~/components/ui/button"
import { Label } from "~/components/ui/label"
import { Slider } from "~/components/ui/slider"
import { Switch } from "~/components/ui/switch"
import type { RoleStyle, RoleStyleType } from "~/lib/api"
import { cn } from "~/lib/utils"

const TYPE_OPTIONS: { value: RoleStyleType; label: string; note: string }[] = [
  { value: "", label: "默认", note: "跟随主题" },
  { value: "solid", label: "纯色", note: "单一颜色" },
  { value: "linear", label: "线性渐变", note: "多色可调角度" },
  { value: "radial", label: "径向渐变", note: "圆形/椭圆扩散" },
]

const DEFAULT_COLORS = ["#7dd3fc", "#a78bfa"]

/** 角色名样式编辑器：纯色 / 线性 / 多色 / 径向渐变 + 动画，带实时预览 */
export function RoleStyleEditor({
  value,
  onChange,
  previewText,
}: {
  value: RoleStyle
  onChange: (next: RoleStyle) => void
  previewText: string
}) {
  const colors = value.colors ?? []

  function switchType(type: RoleStyleType) {
    if (type === "") {
      onChange({ type: "" })
      return
    }
    let nextColors = colors.length > 0 ? colors : DEFAULT_COLORS
    if (type === "solid") nextColors = [nextColors[0] ?? "#7dd3fc"]
    else if (nextColors.length < 2) nextColors = [...nextColors, "#a78bfa"]
    onChange({
      type,
      colors: nextColors,
      angle: type === "linear" ? (value.angle ?? 90) : undefined,
      shape: type === "radial" ? (value.shape ?? "circle") : undefined,
      animated: type === "solid" ? undefined : value.animated,
    })
  }

  function setColor(index: number, color: string) {
    const next = [...colors]
    next[index] = color
    onChange({ ...value, colors: next })
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3 rounded-xl border bg-muted/30 px-4 py-3">
        <span className="text-xs text-muted-foreground">实时预览</span>
        <StyledName nameStyle={value} className="text-lg font-semibold">
          {previewText || "角色名预览"}
        </StyledName>
      </div>

      <div role="radiogroup" aria-label="样式类型" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {TYPE_OPTIONS.map(option => (
          <button
            key={option.value || "none"}
            type="button"
            role="radio"
            aria-checked={value.type === option.value}
            onClick={() => switchType(option.value)}
            className={cn(
              "flex flex-col items-center gap-0.5 rounded-xl border px-3 py-2.5 transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none",
              value.type === option.value ? "border-primary/60 bg-primary/5" : "hover:bg-muted/50"
            )}
          >
            <span className="text-sm font-medium">{option.label}</span>
            <span className="text-[10px] text-muted-foreground">{option.note}</span>
          </button>
        ))}
      </div>

      {value.type !== "" && (
        <div className="grid gap-2">
          <Label>颜色（{value.type === "solid" ? "1 个" : "2–8 个，多色渐变"}）</Label>
          <div className="flex flex-wrap items-center gap-2">
            {colors.map((color, index) => (
              <span key={index} className="relative inline-flex items-center gap-1 rounded-lg border p-1">
                <input
                  type="color"
                  aria-label={`颜色 ${index + 1}`}
                  value={color}
                  onChange={event => setColor(index, event.target.value)}
                  className="size-7 cursor-pointer rounded-md border-0 bg-transparent p-0"
                />
                <code className="font-mono text-[10px] text-muted-foreground">{color}</code>
                {value.type !== "solid" && colors.length > 2 && (
                  <button
                    type="button"
                    aria-label={`删除颜色 ${index + 1}`}
                    onClick={() => onChange({ ...value, colors: colors.filter((_, i) => i !== index) })}
                    className="grid size-4 place-items-center rounded-full transition-[background-color] hover:bg-foreground/10"
                  >
                    <XIcon className="size-3" />
                  </button>
                )}
              </span>
            ))}
            {value.type !== "solid" && colors.length < 8 && (
              <Button
                type="button"
                variant="outline"
                size="xs"
                onClick={() => onChange({ ...value, colors: [...colors, "#f0abfc"] })}
              >
                <PlusIcon data-icon="inline-start" />
                加一色
              </Button>
            )}
          </div>
        </div>
      )}

      {value.type === "linear" && (
        <div className="grid gap-2">
          <Label>
            渐变角度 <span className="font-mono text-xs text-muted-foreground">{value.angle ?? 90}°</span>
          </Label>
          <Slider
            min={0}
            max={360}
            step={5}
            value={[value.angle ?? 90]}
            onValueChange={next => onChange({ ...value, angle: Array.isArray(next) ? next[0] : next })}
            aria-label="渐变角度"
          />
        </div>
      )}

      {value.type === "radial" && (
        <div className="flex items-center gap-3">
          <Label>径向形状</Label>
          <div role="radiogroup" aria-label="径向形状" className="flex rounded-full border p-0.5">
            {(["circle", "ellipse"] as const).map(shape => (
              <button
                key={shape}
                type="button"
                role="radio"
                aria-checked={(value.shape ?? "circle") === shape}
                onClick={() => onChange({ ...value, shape })}
                className={cn(
                  "min-w-14 rounded-full px-3 py-1 text-xs transition-[background-color,color]",
                  (value.shape ?? "circle") === shape ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground"
                )}
              >
                {shape === "circle" ? "圆形" : "椭圆"}
              </button>
            ))}
          </div>
        </div>
      )}

      {(value.type === "linear" || value.type === "radial") && (
        <label className="flex items-center gap-2.5 text-sm">
          <Switch
            checked={Boolean(value.animated)}
            onCheckedChange={next => onChange({ ...value, animated: Boolean(next) })}
            aria-label="渐变流动动画"
          />
          渐变流动动画
        </label>
      )}
    </div>
  )
}
