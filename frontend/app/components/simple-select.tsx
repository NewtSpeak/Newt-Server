import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "~/components/ui/select"

export type SelectOption = { value: string; label: React.ReactNode }

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
  return (
    <Select
      value={value}
      onValueChange={next => {
        if (typeof next === "string") onChange(next)
      }}
      items={options}
    >
      <SelectTrigger className={className} disabled={disabled} aria-label={ariaLabel ?? placeholder}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {options.map(option => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
