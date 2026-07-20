import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import { cn } from "~/lib/utils"
import type { Guild } from "~/lib/api"

/**
 * 服务器头像（Discord 风格）：有 icon_url 显示图片，否则回退服务器名首字
 * （中文取第一个字符，英文取首字母大写）。圆角方形与列表卡片视觉一致。
 */
export function GuildAvatar({ guild, className }: { guild: Pick<Guild, "name" | "icon_url">; className?: string }) {
  const initial = (guild.name ?? "?").trim().charAt(0).toUpperCase() || "?"
  return (
    <Avatar className={cn("rounded-xl after:rounded-xl", className)}>
      {guild.icon_url && <AvatarImage src={guild.icon_url} alt={`${guild.name} 图标`} className="rounded-xl" />}
      <AvatarFallback className="rounded-xl bg-primary/10 font-medium text-primary">{initial}</AvatarFallback>
    </Avatar>
  )
}
