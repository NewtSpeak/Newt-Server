import { Checkbox } from "~/components/ui/checkbox"
import { Label } from "~/components/ui/label"
import { PERMISSION_GROUPS, hasBit, setBit } from "~/lib/permissions"
import { cn } from "~/lib/utils"

/**
 * 角色权限编辑器：64 位权限位按「服务器 / 文本 / 语音 / 舞台 / 共享」分组勾选。
 * （docs 02 §3.2；位 46–63 保留不展示）
 */
export function PermissionMatrix({
  value,
  onChange,
  disabled,
}: {
  value: number
  onChange: (next: number) => void
  disabled?: boolean
}) {
  return (
    <div className="flex flex-col gap-5">
      {PERMISSION_GROUPS.map(group => {
        const allChecked = group.bits.every(item => hasBit(value, item.bit))
        return (
          <fieldset key={group.id} className="rounded-2xl border p-4">
            <legend className="flex items-center gap-3 px-2">
              <span className="text-sm font-semibold">{group.label}</span>
              <button
                type="button"
                disabled={disabled}
                onClick={() => {
                  let next = value
                  for (const item of group.bits) next = setBit(next, item.bit, !allChecked)
                  onChange(next)
                }}
                className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground transition-[background-color,color] hover:bg-muted hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none disabled:opacity-50"
              >
                {allChecked ? "取消全选" : "全选"}
              </button>
            </legend>
            <div className="grid gap-1.5 sm:grid-cols-2 xl:grid-cols-3">
              {group.bits.map(item => {
                const checked = hasBit(value, item.bit)
                return (
                  <Label
                    key={item.name}
                    className={cn(
                      "flex min-h-10 cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2 transition-[background-color,border-color]",
                      checked ? "border-primary/40 bg-primary/5" : "hover:bg-muted/60",
                      disabled && "cursor-not-allowed opacity-60"
                    )}
                  >
                    <Checkbox
                      checked={checked}
                      disabled={disabled}
                      onCheckedChange={next => onChange(setBit(value, item.bit, Boolean(next)))}
                      className="mt-0.5"
                    />
                    <span className="min-w-0">
                      <span className="flex flex-wrap items-baseline gap-x-2">
                        <span className="text-sm leading-5">{item.label}</span>
                        <span className="font-mono text-[10px] text-muted-foreground">{item.name}</span>
                      </span>
                      {item.description && <span className="block text-xs leading-4 text-muted-foreground">{item.description}</span>}
                    </span>
                  </Label>
                )
              })}
            </div>
          </fieldset>
        )
      })}
    </div>
  )
}

export type OverwriteState = "inherit" | "allow" | "deny"

/**
 * 频道覆盖三态编辑：继承 / 允许 / 拒绝（同一位不可同时 allow+deny）。
 */
export function OverwriteMatrix({
  allow,
  deny,
  onChange,
  disabled,
}: {
  allow: number
  deny: number
  onChange: (allow: number, deny: number) => void
  disabled?: boolean
}) {
  function stateOf(bit: number): OverwriteState {
    if (hasBit(allow, bit)) return "allow"
    if (hasBit(deny, bit)) return "deny"
    return "inherit"
  }

  function setState(bit: number, state: OverwriteState) {
    onChange(setBit(allow, bit, state === "allow"), setBit(deny, bit, state === "deny"))
  }

  const options: { key: OverwriteState; label: string; active: string }[] = [
    { key: "deny", label: "拒绝", active: "bg-destructive/15 text-destructive" },
    { key: "inherit", label: "继承", active: "bg-muted text-foreground" },
    { key: "allow", label: "允许", active: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400" },
  ]

  return (
    <div className="flex flex-col gap-5">
      {PERMISSION_GROUPS.map(group => (
        <fieldset key={group.id} className="rounded-2xl border p-4">
          <legend className="px-2 text-sm font-semibold">{group.label}</legend>
          <div className="grid gap-1.5 lg:grid-cols-2">
            {group.bits.map(item => {
              const state = stateOf(item.bit)
              return (
                <div
                  key={item.name}
                  className={cn(
                    "flex min-h-10 items-center justify-between gap-3 rounded-lg border px-3 py-1.5 transition-[background-color,border-color]",
                    state === "allow" && "border-emerald-500/30 bg-emerald-500/5",
                    state === "deny" && "border-destructive/30 bg-destructive/5"
                  )}
                >
                  <span className="min-w-0">
                    <span className="block truncate text-sm">{item.label}</span>
                    <span className="block truncate font-mono text-[10px] text-muted-foreground">{item.name}</span>
                  </span>
                  <div role="radiogroup" aria-label={`${item.label} 覆盖`} className="flex shrink-0 rounded-full border p-0.5">
                    {options.map(option => (
                      <button
                        key={option.key}
                        type="button"
                        role="radio"
                        aria-checked={state === option.key}
                        disabled={disabled}
                        onClick={() => setState(item.bit, option.key)}
                        className={cn(
                          "min-w-11 rounded-full px-2 py-1 text-xs transition-[background-color,color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none disabled:opacity-50",
                          state === option.key ? option.active : "text-muted-foreground hover:text-foreground"
                        )}
                      >
                        {option.label}
                      </button>
                    ))}
                  </div>
                </div>
              )
            })}
          </div>
        </fieldset>
      ))}
    </div>
  )
}
