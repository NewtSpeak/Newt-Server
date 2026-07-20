import { Slider as SliderPrimitive } from "@base-ui/react/slider"

import { cn } from "~/lib/utils"

function Slider({ className, ...props }: SliderPrimitive.Root.Props) {
  return (
    <SliderPrimitive.Root data-slot="slider" className={cn("relative w-full select-none", className)} {...props}>
      <SliderPrimitive.Control className="flex w-full touch-none items-center py-2.5">
        <SliderPrimitive.Track className="relative h-1.5 w-full grow rounded-full bg-muted">
          <SliderPrimitive.Indicator className="absolute h-full rounded-full bg-primary" />
          <SliderPrimitive.Thumb
            data-slot="slider-thumb"
            className="block size-4 rounded-full border border-primary/40 bg-background shadow-sm transition-[box-shadow,scale] outline-none hover:scale-110 focus-visible:ring-3 focus-visible:ring-ring/40 active:scale-[0.96]"
          />
        </SliderPrimitive.Track>
      </SliderPrimitive.Control>
    </SliderPrimitive.Root>
  )
}

export { Slider }
