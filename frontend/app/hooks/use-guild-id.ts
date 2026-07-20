import { useEffect, useState } from "react"

import type { Guild } from "~/lib/api"

/** 通用「当前服务器」选择：默认取第一个服务器 */
export function useGuildID(guilds: Guild[]) {
  const [guildID, setGuildID] = useState<string | null>(guilds[0]?.id ?? null)
  useEffect(() => {
    if (!guildID && guilds[0]) setGuildID(guilds[0].id)
  }, [guildID, guilds])
  return [guildID, setGuildID] as const
}
