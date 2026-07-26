import { Link, useOutletContext, useParams } from "react-router"
import { ArrowLeftIcon } from "lucide-react"

import { GuildAvatar } from "~/components/guild-avatar"
import { GuildBannerHero } from "~/components/guild-banner"
import { BadgesTab } from "~/components/server/badges-tab"
import { BotsTab } from "~/components/server/bots-tab"
import { ChannelsTab } from "~/components/server/channels-tab"
import { InviteLandingTab } from "~/components/server/invite-landing-tab"
import { MembersTab } from "~/components/server/members-tab"
import { OverwritesTab } from "~/components/server/overwrites-tab"
import { RolesTab } from "~/components/server/roles-tab"
import { SettingsTab } from "~/components/server/settings-tab"
import { EmptyState } from "~/components/states"
import { Button } from "~/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { listChannels, listMembersDisplay, listRoles, type Channel, type MemberDisplay, type Role } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"

/** 事件载荷若携带 guild_id，仅在与当前页面服务器一致时刷新（避免跨服误刷）。 */
function payloadGuildID(payload: unknown): string | undefined {
  const data = payload as { guild_id?: string; guild?: { id?: string } } | undefined
  return data?.guild_id ?? data?.guild?.id
}

export default function ServerDetailPage() {
  const { guilds, user, refreshGuilds } = useOutletContext<ConsoleContext>()
  const { guildId = "" } = useParams()
  const guild = guilds.find(item => item.id === guildId)

  const channels = useAsyncData<Channel[]>(guildId ? () => listChannels(guildId) : null, [guildId])
  // 成员采用展示聚合接口：头像/横幅/名字样式/徽章一并返回（customization 专项）。
  const members = useAsyncData<MemberDisplay[]>(guildId ? () => listMembersDisplay(guildId) : null, [guildId])
  const roles = useAsyncData<Role[]>(guildId ? () => listRoles(guildId) : null, [guildId])

  // 服务器结构实时同步（docs 14 §3.2）：结构事件到达即静默重拉对应数据集，
  // 保证多端/后台并发改动时本页频道、成员、角色即时刷新。
  useGatewayEvent(["CHANNEL_CREATE", "CHANNEL_UPDATE", "CHANNEL_DELETE", "PERMISSIONS_UPDATE"], payload => {
    const gid = payloadGuildID(payload)
    if (!gid || gid === guildId) channels.reload(true)
  })
  useGatewayEvent(
    ["GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE", "GUILD_MEMBER_REMOVE", "BADGE_GRANT", "BADGE_REVOKE"],
    payload => {
      const gid = payloadGuildID(payload)
      if (!gid || gid === guildId) members.reload(true)
    }
  )
  useGatewayEvent(["GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE", "GUILD_ROLE_DELETE"], payload => {
    const gid = payloadGuildID(payload)
    if (!gid || gid === guildId) {
      roles.reload(true)
      members.reload(true)
    }
  })

  if (!guild && guilds.length > 0) {
    return (
      <main className="flex flex-1 flex-col gap-6 px-4 py-6 lg:px-6">
        <EmptyState
          title="找不到该服务器"
          description="它可能已被删除，或你没有访问权限。"
          action={
            <Button variant="outline" render={<Link to="/servers" />}>
              <ArrowLeftIcon data-icon="inline-start" />
              返回服务器列表
            </Button>
          }
        />
      </main>
    )
  }

  return (
    <main className="flex flex-1 flex-col gap-5 py-4 md:py-6">
      {guild && <GuildBannerHero guild={guild} className="mx-4 lg:mx-6" />}

      <div className="flex flex-wrap items-center gap-3 px-4 lg:px-6">
        <Button variant="ghost" size="icon-sm" aria-label="返回服务器列表" render={<Link to="/servers" />}>
          <ArrowLeftIcon />
        </Button>
        {guild && <GuildAvatar guild={guild} className="size-11" />}
        <div className="min-w-0">
          <h1 className="truncate text-2xl font-semibold tracking-tight">{guild?.name ?? "服务器详情"}</h1>
          <p className="truncate font-mono text-xs text-muted-foreground">{guildId}</p>
        </div>
      </div>

      <Tabs defaultValue="channels" className="px-4 lg:px-6">
        <TabsList>
          <TabsTrigger value="channels">频道</TabsTrigger>
          <TabsTrigger value="members">成员</TabsTrigger>
          <TabsTrigger value="roles">角色权限</TabsTrigger>
          <TabsTrigger value="overwrites">权限覆盖</TabsTrigger>
          <TabsTrigger value="badges">徽章</TabsTrigger>
          <TabsTrigger value="bots">机器人</TabsTrigger>
          <TabsTrigger value="invite">邀请页</TabsTrigger>
          <TabsTrigger value="settings">设置</TabsTrigger>
        </TabsList>
        <TabsContent value="channels" className="pt-3">
          <ChannelsTab
            guildID={guildId}
            channels={channels.data ?? []}
            roles={roles.data ?? []}
            status={channels.status}
            error={channels.error}
            reload={() => channels.reload()}
          />
        </TabsContent>
        <TabsContent value="members" className="pt-3">
          <MembersTab
            guildID={guildId}
            members={members.data ?? []}
            roles={roles.data ?? []}
            status={members.status}
            error={members.error}
            reload={() => members.reload(true)}
          />
        </TabsContent>
        <TabsContent value="roles" className="pt-3">
          <RolesTab
            guildID={guildId}
            roles={roles.data ?? []}
            status={roles.status}
            error={roles.error}
            reload={() => {
              roles.reload(true)
              members.reload(true)
            }}
          />
        </TabsContent>
        <TabsContent value="overwrites" className="pt-3">
          <OverwritesTab guildID={guildId} channels={channels.data ?? []} roles={roles.data ?? []} members={members.data ?? []} />
        </TabsContent>
        <TabsContent value="badges" className="pt-3">
          <BadgesTab guildID={guildId} members={members.data ?? []} />
        </TabsContent>
        <TabsContent value="bots" className="pt-3">
          <BotsTab guildID={guildId} />
        </TabsContent>
        <TabsContent value="invite" className="pt-3">
          <InviteLandingTab guildID={guildId} isSystemAdmin={Boolean(user?.system_admin)} />
        </TabsContent>
        <TabsContent value="settings" className="pt-3">
          {guild && (
            <SettingsTab
              guild={guild}
              members={members.data ?? []}
              isSystemAdmin={Boolean(user?.system_admin)}
              onChanged={() => {
                void refreshGuilds()
              }}
            />
          )}
        </TabsContent>
      </Tabs>
    </main>
  )
}
