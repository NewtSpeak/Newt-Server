import { cn } from "~/lib/utils"

export function PageHeader({
  title,
  titleExtra,
  description,
  actions,
  className,
}: {
  title: string
  /** 紧挨标题右侧的附加控件（如 info 按钮） */
  titleExtra?: React.ReactNode
  description?: string
  actions?: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex flex-wrap items-start justify-between gap-3 px-4 lg:px-6", className)}>
      <div>
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          {titleExtra}
        </div>
        {description && <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
