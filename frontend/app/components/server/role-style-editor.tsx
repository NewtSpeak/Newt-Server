import { useRef, useState } from "react"
import { ImagePlusIcon, PlusIcon, Trash2Icon, XIcon } from "lucide-react"
import { toast } from "sonner"

import { RoleBadgePill, RoleStyleIcon, StyledName } from "~/components/server/styled-name"
import { Button } from "~/components/ui/button"
import { Label } from "~/components/ui/label"
import { Slider } from "~/components/ui/slider"
import { Switch } from "~/components/ui/switch"
import {
  deleteRoleBadgeBackground,
  deleteRoleBadgeIcon,
  uploadRoleBadgeBackground,
  uploadRoleBadgeIcon,
  type RoleBadgeStyle,
  type RoleStyle,
  type RoleStyleType,
  type RoleSurfaceStyle,
} from "~/lib/api"
import { cn } from "~/lib/utils"

const TYPE_OPTIONS: { value: RoleStyleType; label: string; note: string }[] = [
  { value: "", label: "默认", note: "跟随主题" },
  { value: "solid", label: "纯色", note: "单一颜色" },
  { value: "linear", label: "线性渐变", note: "多色可调角度" },
  { value: "radial", label: "径向渐变", note: "圆形/椭圆扩散" },
]

const DEFAULT_COLORS = ["#7dd3fc", "#a78bfa"]
const DEFAULT_SPEED = 4

function TextDecorToggles({
  value,
  onChange,
  disabled,
  label = "文字样式",
}: {
  value: {
    bold?: boolean
    italic?: boolean
    underline?: boolean
    strikethrough?: boolean
  }
  onChange: (next: {
    bold?: boolean
    italic?: boolean
    underline?: boolean
    strikethrough?: boolean
  }) => void
  disabled?: boolean
  label?: string
}) {
  const items = [
    { key: "bold" as const, label: "加粗", sample: "B", className: "font-bold" },
    { key: "italic" as const, label: "斜体", sample: "I", className: "italic" },
    { key: "underline" as const, label: "下划线", sample: "U", className: "underline" },
    { key: "strikethrough" as const, label: "中划线", sample: "S", className: "line-through" },
  ]
  return (
    <div className="flex flex-col gap-1.5">
      <Label className="text-foreground/70">{label}</Label>
      <div className="flex flex-wrap gap-1.5">
        {items.map(item => {
          const on = Boolean(value[item.key])
          return (
            <button
              key={item.key}
              type="button"
              disabled={disabled}
              title={item.label}
              aria-pressed={on}
              onClick={() =>
                onChange({
                  ...value,
                  [item.key]: on ? undefined : true,
                })
              }
              className={cn(
                "flex size-8 items-center justify-center rounded-lg border text-sm transition-colors",
                "focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:outline-none",
                "disabled:pointer-events-none disabled:opacity-50",
                on
                  ? "border-primary/60 bg-primary/10 text-foreground"
                  : "text-muted-foreground hover:bg-muted/60",
                item.className,
              )}
            >
              {item.sample}
            </button>
          )
        })}
      </div>
    </div>
  )
}

function ensureColors(type: RoleStyleType, colors: string[]): string[] {
  let next = colors.length > 0 ? colors : [...DEFAULT_COLORS]
  if (type === "solid") next = [next[0] ?? "#7dd3fc"]
  else if (type !== "" && next.length < 2) next = [...next, "#a78bfa"]
  return next
}

function patchSurface(
  base: RoleSurfaceStyle,
  type: RoleStyleType,
): RoleSurfaceStyle {
  if (type === "") return { type: "" }
  const colors = ensureColors(type, base.colors ?? [])
  return {
    type,
    colors,
    angle: type === "linear" ? (base.angle ?? 90) : undefined,
    shape: type === "radial" ? (base.shape ?? "circle") : undefined,
    animated: type === "solid" ? undefined : base.animated,
    speed: type === "solid" ? undefined : base.animated ? (base.speed ?? DEFAULT_SPEED) : undefined,
  }
}

/** 表面样式子编辑器（文字 / 独立 icon 复用） */
function SurfaceStyleFields({
  value,
  onChange,
  disabled,
  labelPrefix = "",
}: {
  value: RoleSurfaceStyle
  onChange: (next: RoleSurfaceStyle) => void
  disabled?: boolean
  labelPrefix?: string
}) {
  const colors = value.colors ?? []

  function switchType(type: RoleStyleType) {
    if (disabled) return
    onChange(patchSurface(value, type))
  }

  function setColor(index: number, color: string) {
    if (disabled) return
    const next = [...colors]
    next[index] = color
    onChange({ ...value, colors: next })
  }

  return (
    <div className={cn("flex flex-col gap-3", disabled && "opacity-70")}>
      <div role="radiogroup" aria-label={`${labelPrefix}样式类型`} className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {TYPE_OPTIONS.map(option => (
          <button
            key={option.value || "none"}
            type="button"
            role="radio"
            disabled={disabled}
            aria-checked={value.type === option.value}
            onClick={() => switchType(option.value)}
            className={cn(
              "flex flex-col items-center gap-0.5 rounded-xl border px-3 py-2.5 transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none disabled:pointer-events-none",
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
                  value={/^#[0-9a-fA-F]{6}$/.test(color) ? color : "#7dd3fc"}
                  disabled={disabled}
                  onChange={event => setColor(index, event.target.value)}
                  className="size-7 cursor-pointer rounded-md border-0 bg-transparent p-0 disabled:cursor-not-allowed"
                />
                <code className="font-mono text-[10px] text-muted-foreground">{color}</code>
                {value.type !== "solid" && colors.length > 2 && (
                  <button
                    type="button"
                    aria-label={`删除颜色 ${index + 1}`}
                    disabled={disabled}
                    onClick={() => onChange({ ...value, colors: colors.filter((_, i) => i !== index) })}
                    className="grid size-4 place-items-center rounded-full transition-[background-color] hover:bg-foreground/10 disabled:pointer-events-none"
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
                disabled={disabled}
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
            disabled={disabled}
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
                disabled={disabled}
                aria-checked={(value.shape ?? "circle") === shape}
                onClick={() => onChange({ ...value, shape })}
                className={cn(
                  "min-w-14 rounded-full px-3 py-1 text-xs transition-[background-color,color] disabled:pointer-events-none",
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
        <div className="flex flex-col gap-3">
          <label className="flex items-center gap-2.5 text-sm">
            <Switch
              checked={Boolean(value.animated)}
              disabled={disabled}
              onCheckedChange={next =>
                onChange({
                  ...value,
                  animated: Boolean(next),
                  speed: next ? (value.speed ?? DEFAULT_SPEED) : undefined,
                })
              }
              aria-label="渐变流动动画"
            />
            渐变流动动画
          </label>
          {value.animated ? (
            <div className="grid gap-2">
              <Label>
                流动速度{" "}
                <span className="font-mono text-xs text-muted-foreground">
                  {(value.speed ?? DEFAULT_SPEED).toFixed(1)} 秒/周期
                </span>
                <span className="ml-1 text-[10px] text-muted-foreground">（数值越小越快）</span>
              </Label>
              <Slider
                min={0.5}
                max={20}
                step={0.5}
                disabled={disabled}
                value={[value.speed ?? DEFAULT_SPEED]}
                onValueChange={next => {
                  const speed = Array.isArray(next) ? next[0] : next
                  onChange({ ...value, speed })
                }}
                aria-label="流动速度"
              />
            </div>
          ) : null}
        </div>
      )}
    </div>
  )
}

/** 角色名样式编辑器：文字 + 色点 + 徽章背景/icon */
export function RoleStyleEditor({
  value,
  onChange,
  previewText,
  disabled = false,
  guildID,
  roleID,
}: {
  value: RoleStyle
  onChange: (next: RoleStyle) => void
  previewText: string
  disabled?: boolean
  /** 上传徽章 icon 时需要 */
  guildID?: string
  roleID?: string
}) {
  const textSurface: RoleSurfaceStyle = {
    type: value.type,
    colors: value.colors,
    angle: value.angle,
    shape: value.shape,
    animated: value.animated,
    speed: value.speed,
  }
  const fileRef = useRef<HTMLInputElement>(null)
  const bgFileRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const [uploadingBg, setUploadingBg] = useState(false)

  function setTextSurface(next: RoleSurfaceStyle) {
    onChange({
      ...value,
      type: next.type,
      colors: next.colors,
      angle: next.angle,
      shape: next.shape,
      animated: next.animated,
      speed: next.speed,
      ...(next.type === "" ? { icon_sync: undefined, icon: undefined } : {}),
    })
  }

  function setBadge(patch: Partial<RoleBadgeStyle> | null) {
    if (patch === null) {
      onChange({ ...value, badge: undefined })
      return
    }
    const next: RoleBadgeStyle = { ...value.badge, ...patch, enabled: true }
    onChange({ ...value, badge: next })
  }

  const iconIndependent = Boolean(value.icon?.type) && !value.icon_sync
  const iconDraft: RoleSurfaceStyle = value.icon?.type
    ? value.icon
    : { type: "solid", colors: [value.colors?.[0] ?? "#7dd3fc"] }
  const badgeBg: RoleSurfaceStyle = value.badge?.background?.type
    ? value.badge.background
    : { type: "" }
  const badgeEnabled = Boolean(
    value.badge?.enabled ||
      value.badge?.background ||
      value.badge?.background_image_url ||
      value.badge?.icon_url,
  )

  function validateImageFile(file: File, maxMb: number) {
    const okType =
      /^(image\/(png|jpeg|webp|gif|svg\+xml))$/.test(file.type) ||
      file.name.endsWith(".svg")
    if (!okType) {
      toast.error("仅支持 PNG/JPEG/WebP/GIF/SVG")
      return false
    }
    if (file.size > maxMb * 1024 * 1024) {
      toast.error(`文件需 ≤${maxMb}MB`)
      return false
    }
    return true
  }

  async function onPickIcon(file: File | undefined) {
    if (!file || !guildID || !roleID) return
    if (!validateImageFile(file, 2)) return
    setUploading(true)
    try {
      const res = await uploadRoleBadgeIcon(guildID, roleID, file)
      setBadge({ icon_url: res.icon_url, enabled: true })
      toast.success("徽章图标已上传")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "上传失败")
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ""
    }
  }

  async function onClearIcon() {
    if (!guildID || !roleID) {
      setBadge({ icon_url: "" })
      return
    }
    setUploading(true)
    try {
      await deleteRoleBadgeIcon(guildID, roleID)
      setBadge({ icon_url: "" })
      toast.success("已移除徽章图标")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "移除失败")
    } finally {
      setUploading(false)
    }
  }

  async function onPickBg(file: File | undefined) {
    if (!file || !guildID || !roleID) return
    if (!validateImageFile(file, 4)) return
    setUploadingBg(true)
    try {
      const res = await uploadRoleBadgeBackground(guildID, roleID, file)
      setBadge({
        background_image_url: res.background_image_url,
        enabled: true,
      })
      toast.success("徽章背景图已上传")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "上传失败")
    } finally {
      setUploadingBg(false)
      if (bgFileRef.current) bgFileRef.current.value = ""
    }
  }

  async function onClearBg() {
    if (!guildID || !roleID) {
      setBadge({ background_image_url: "" })
      return
    }
    setUploadingBg(true)
    try {
      await deleteRoleBadgeBackground(guildID, roleID)
      setBadge({ background_image_url: "" })
      toast.success("已移除徽章背景图")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "移除失败")
    } finally {
      setUploadingBg(false)
    }
  }

  return (
    <div className={cn("flex flex-col gap-4", disabled && "pointer-events-none opacity-70")}>
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-muted/30 px-4 py-3">
        <span className="text-xs text-muted-foreground">实时预览</span>
        <span className="flex flex-wrap items-center gap-2">
          <RoleStyleIcon style={value} className="size-4" fallbackColor={value.colors?.[0]} />
          <StyledName nameStyle={value} className="text-lg font-semibold">
            {previewText || "角色名预览"}
          </StyledName>
          <RoleBadgePill
            name={previewText || "徽章"}
            badge={value.badge}
            fallbackColor={value.colors?.[0]}
          />
        </span>
      </div>

      <div className="flex flex-col gap-2">
        <Label className="text-foreground/70">用户名样式</Label>
        <SurfaceStyleFields value={textSurface} onChange={setTextSurface} disabled={disabled} labelPrefix="用户名" />
        <TextDecorToggles
          disabled={disabled}
          label="用户名文字样式"
          value={{
            bold: value.bold,
            italic: value.italic,
            underline: value.underline,
            strikethrough: value.strikethrough,
          }}
          onChange={next =>
            onChange({
              ...value,
              bold: next.bold,
              italic: next.italic,
              underline: next.underline,
              strikethrough: next.strikethrough,
            })
          }
        />
      </div>

      {value.type !== "" && (
        <div className="flex flex-col gap-3 rounded-xl border p-3">
          <div className="flex items-center justify-between gap-2">
            <div>
              <p className="text-sm font-medium">角色色点 Icon</p>
              <p className="text-[11px] text-muted-foreground">列表圆点；可与文字同步或独立渐变</p>
            </div>
            <RoleStyleIcon style={value} className="size-5" fallbackColor={value.colors?.[0]} />
          </div>
          <label className="flex items-center gap-2.5 text-sm">
            <Switch
              checked={Boolean(value.icon_sync)}
              disabled={disabled}
              onCheckedChange={next =>
                onChange({
                  ...value,
                  icon_sync: Boolean(next) || undefined,
                  icon: next ? undefined : value.icon,
                })
              }
            />
            色点与用户名样式同步
          </label>
          {!value.icon_sync && (
            <>
              <label className="flex items-center gap-2.5 text-sm">
                <Switch
                  checked={iconIndependent}
                  disabled={disabled}
                  onCheckedChange={next => {
                    if (next) {
                      onChange({
                        ...value,
                        icon: patchSurface(
                          { type: "linear", colors: DEFAULT_COLORS, animated: true, speed: DEFAULT_SPEED },
                          "linear",
                        ),
                      })
                    } else onChange({ ...value, icon: undefined })
                  }}
                />
                独立配置色点样式
              </label>
              {iconIndependent ? (
                <SurfaceStyleFields
                  value={iconDraft}
                  disabled={disabled}
                  labelPrefix="色点"
                  onChange={next => onChange({ ...value, icon: next.type ? next : undefined })}
                />
              ) : null}
            </>
          )}
        </div>
      )}

      {/* 角色徽章 */}
      <div className="flex flex-col gap-3 rounded-xl border p-3">
        <div className="flex items-center justify-between gap-2">
          <div>
            <p className="text-sm font-medium">角色徽章</p>
            <p className="text-[11px] text-muted-foreground">
              消息流 / 成员列表旁标签：背景纯色或渐变、自定义 SVG/图片 icon
            </p>
          </div>
          <RoleBadgePill name={previewText || "徽章"} badge={value.badge} fallbackColor={value.colors?.[0]} />
        </div>
        <label className="flex items-center gap-2.5 text-sm">
          <Switch
            checked={badgeEnabled}
            disabled={disabled}
            onCheckedChange={next => {
              if (next) {
                setBadge({
                  enabled: true,
                  background: value.badge?.background ?? {
                    type: "solid",
                    colors: [value.colors?.[0] ?? "#7dd3fc"],
                  },
                  show_name: value.badge?.show_name ?? true,
                })
              } else {
                setBadge(null)
              }
            }}
          />
          启用自定义徽章
        </label>
        {badgeEnabled ? (
          <>
            <Label className="text-foreground/70">徽章背景色 / 渐变</Label>
            <SurfaceStyleFields
              value={badgeBg}
              disabled={disabled}
              labelPrefix="徽章背景"
              onChange={next =>
                setBadge({
                  background: next.type ? next : undefined,
                  enabled: true,
                })
              }
            />
            <div className="flex flex-col gap-2">
              <Label className="text-foreground/70">徽章背景图</Label>
              <p className="text-[11px] text-muted-foreground">
                可与上方渐变叠加（渐变盖在图上）；支持 PNG / JPEG / WebP / GIF / SVG，≤4MB
              </p>
              <div className="flex flex-wrap items-center gap-2">
                <input
                  ref={bgFileRef}
                  type="file"
                  accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml,.svg"
                  className="hidden"
                  onChange={e => void onPickBg(e.target.files?.[0])}
                />
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  disabled={disabled || uploadingBg || !guildID || !roleID}
                  onClick={() => bgFileRef.current?.click()}
                >
                  <ImagePlusIcon className="size-3.5" />
                  {uploadingBg
                    ? "上传中…"
                    : value.badge?.background_image_url
                      ? "更换背景图"
                      : "选择背景图"}
                </Button>
                {value.badge?.background_image_url ? (
                  <>
                    <img
                      src={value.badge.background_image_url}
                      alt=""
                      className="h-10 w-20 rounded-md border object-cover"
                    />
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      disabled={disabled || uploadingBg}
                      onClick={() => void onClearBg()}
                    >
                      <Trash2Icon className="size-3.5" />
                      移除背景图
                    </Button>
                  </>
                ) : null}
              </div>
            </div>
            <label className="flex items-center gap-2.5 text-sm">
              <Switch
                checked={value.badge?.show_name !== false}
                disabled={disabled}
                onCheckedChange={next => setBadge({ show_name: next })}
              />
              显示角色名称
            </label>
            <TextDecorToggles
              disabled={disabled}
              label="徽章文字样式"
              value={{
                bold: value.badge?.bold,
                italic: value.badge?.italic,
                underline: value.badge?.underline,
                strikethrough: value.badge?.strikethrough,
              }}
              onChange={next =>
                setBadge({
                  bold: next.bold,
                  italic: next.italic,
                  underline: next.underline,
                  strikethrough: next.strikethrough,
                })
              }
            />
            <div className="flex flex-wrap items-center gap-2">
              <Label className="text-xs text-muted-foreground">文字色</Label>
              <input
                type="color"
                disabled={disabled}
                value={
                  value.badge?.text_color && /^#[0-9a-fA-F]{6}$/.test(value.badge.text_color)
                    ? value.badge.text_color
                    : "#ffffff"
                }
                onChange={e => setBadge({ text_color: e.target.value })}
                className="size-7 cursor-pointer rounded-md border-0 bg-transparent p-0"
              />
              <code className="font-mono text-[10px] text-muted-foreground">
                {value.badge?.text_color || "#ffffff"}
              </code>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <input
                ref={fileRef}
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml,.svg"
                className="hidden"
                onChange={e => void onPickIcon(e.target.files?.[0])}
              />
              <Button
                type="button"
                size="sm"
                variant="secondary"
                disabled={disabled || uploading || !guildID || !roleID}
                onClick={() => fileRef.current?.click()}
              >
                <ImagePlusIcon className="size-3.5" />
                {uploading ? "上传中…" : value.badge?.icon_url ? "更换 Icon" : "上传 Icon"}
              </Button>
              {value.badge?.icon_url ? (
                <>
                  <img
                    src={value.badge.icon_url}
                    alt=""
                    className="size-8 rounded-md border object-contain"
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    disabled={disabled || uploading}
                    onClick={() => void onClearIcon()}
                  >
                    <Trash2Icon className="size-3.5" />
                    移除 Icon
                  </Button>
                </>
              ) : (
                <span className="text-[11px] text-muted-foreground">
                  支持 PNG / JPEG / WebP / GIF / SVG，≤2MB
                </span>
              )}
            </div>
          </>
        ) : null}
      </div>
    </div>
  )
}
