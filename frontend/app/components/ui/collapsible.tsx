"use client"

import { Collapsible as CollapsiblePrimitive } from "@base-ui/react/collapsible"

import { cn } from "~/lib/utils"

function Collapsible({ ...props }: CollapsiblePrimitive.Root.Props) {
  return <CollapsiblePrimitive.Root data-slot="collapsible" {...props} />
}

function CollapsibleTrigger({ ...props }: CollapsiblePrimitive.Trigger.Props) {
  return (
    <CollapsiblePrimitive.Trigger data-slot="collapsible-trigger" {...props} />
  )
}

/**
 * 展开/收起面板。高度过渡由 base-ui 暴露的 --collapsible-panel-height 驱动，
 * 无需 JS 测高；时长走 transitions-dev 的 --panel-* token。
 * 收起默认 260ms（= 展开 400ms 的 65%），符合「退出快于入场」。
 */
function CollapsibleContent({ className, style, ...props }: CollapsiblePrimitive.Panel.Props) {
  return (
    <CollapsiblePrimitive.Panel
      data-slot="collapsible-content"
      className={cn(
        "h-(--collapsible-panel-height) overflow-hidden",
        "transition-[height,opacity,filter] ease-(--panel-ease)",
        "data-[open]:duration-(--panel-open-dur) data-[closed]:duration-(--panel-close-dur)",
        "data-starting-style:h-0 data-starting-style:opacity-0 data-starting-style:blur-(--panel-blur)",
        "data-ending-style:h-0 data-ending-style:opacity-0 data-ending-style:blur-(--panel-blur)",
        className
      )}
      style={{ "--panel-close-dur": "260ms", ...(style as object) } as React.CSSProperties}
      {...props}
    />
  )
}

export { Collapsible, CollapsibleTrigger, CollapsibleContent }
