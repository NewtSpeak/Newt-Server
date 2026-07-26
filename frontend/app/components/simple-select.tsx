import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "~/components/ui/select"

export type SelectOption = { value: string; label: React.ReactNode }

/**
 * 控制台通用下拉：value 为字段值，展示始终为 option.label。
 * Root 同时传 items，保证关闭弹层后仍显示标签而非 raw value。
 */
export function SimpleSelect({
  value,
  onChange,
  options,
  placeholder = "请选择",
  className,
  disabled,
  ariaLabel,
}: {
  value: string | null
  onChange: (value: string) => void
  options: SelectOption[]
  placeholder?: string
  className?: string
  disabled?: boolean
  ariaLabel?: string
}) {
  const selected = options.find((o) => o.value === value)

  return (
    <Select
      value={value}
      onValueChange={(next) => {
        if (typeof next === "string") onChange(next)
      }}
      items={options}
    >
      <SelectTrigger
        className={className}
        disabled={disabled}
        aria-label={ariaLabel ?? placeholder}
      >
        <SelectValue placeholder={placeholder}>
          {selected ? selected.label : null}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem
            key={option.value}
            value={option.value}
            label={
              typeof option.label === "string" || typeof option.label === "number"
                ? String(option.label)
                : undefined
            }
          >
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
