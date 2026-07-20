import type { Guild, User } from "~/lib/api"

export type ConsoleContext = {
  user: User
  guilds: Guild[]
  addGuild: (guild: Guild) => void
  /** 重新拉取服务器列表（改名/删除/转让后刷新侧边数据） */
  refreshGuilds: () => Promise<void>
}
