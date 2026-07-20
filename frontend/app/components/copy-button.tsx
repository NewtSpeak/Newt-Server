import { useRef, useState } from "react"
import { CheckIcon, CopyIcon } from "lucide-react"

import { Button } from "~/components/ui/button"
import { cn } from "~/lib/utils"

/** 复制按钮：icon swap 交叉淡入淡出（scale 0.25→1 + blur 4px→0） */
export function CopyButton({ text, label = "复制", className }: { text: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const area = document.createElement("textarea")
      area.value = text
      document.body.appendChild(area)
      area.select()
      document.execCommand("copy")
      area.remove()
    }
    setCopied(true)
    if (timerRef.current) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => setCopied(false), 2000)
  }

  return (
    <Button type="button" variant="outline" size="sm" onClick={copy} className={cn("shrink-0", className)} aria-label={label}>
      <span className="t-icon-swap size-4" data-icon="inline-start">
        <CopyIcon className="size-4" data-state={copied ? "hidden" : "visible"} />
        <CheckIcon className="size-4 text-emerald-500" data-state={copied ? "visible" : "hidden"} />
      </span>
      {copied ? "已复制" : label}
    </Button>
  )
}
