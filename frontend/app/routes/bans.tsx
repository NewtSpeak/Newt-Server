import { useState, type FormEvent } from "react"
import { useOutletContext } from "react-router"
import { PlusIcon, ShieldBanIcon } from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback } from "~/components/ui/avatar"
import { Button } from "~/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGuildID } from "~/hooks/use-guild-id"
import { banUser, listBans, listMembers, memberName, unbanUser, type Ban, type GuildMember } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatTime } from "~/lib/format"

export default function BansPage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const [guildID, setGuildID] = useGuildID(guilds)

  const bans = useAsyncData<Ban[]>(guildID ? () => listBans(guildID) : null, [guildID])
  const members = useAsyncData<GuildMember[]>(guildID ? () => listMembers(guildID) : null, [guildID])

  const [banOpen, setBanOpen] = useState(false)
  const [banning, setBanning] = useState(false)
  const [targetID, setTargetID] = useState<string | null>(null)

  async function onBan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!guildID || !targetID) return
    const reason = String(new FormData(event.currentTarget).get("ban-reason") ?? "").trim()
    setBanning(true)
    try {
      await banUser(guildID, targetID, reason || undefined)
      toast.success("封禁已生效：成员被移出且无法再加入，其全部限制记录将被清理归档")
      setBanOpen(false)
      setTargetID(null)
      bans.reload(true)
    } catch (reason_) {
      toast.error(reason_ instanceof Error ? reason_.message : "封禁失败")
    } finally {
      setBanning(false)
    }
  }

  async function onUnban(ban: Ban) {
    if (!guildID) return
    const name = ban.username ?? ban.user_id
    if (!window.confirm(`确定解除「${name}」的封禁？解除后对方可通过邀请重新加入。`)) return
    try {
      await unbanUser(guildID, ban.user_id)
      toast.success("封禁已解除")
      bans.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "解封失败")
    }
  }

  const list = bans.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="封禁管理"
        description="Ban Member 独立于多维限制：移出服务器并阻止再次加入，同时清理该用户在本服的全部限制记录。"
        actions={
          <Button variant="destructive" onClick={() => setBanOpen(true)} disabled={!guildID}>
            <PlusIcon data-icon="inline-start" />
            封禁成员
          </Button>
        }
      />

      <Dialog open={banOpen} onOpenChange={setBanOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>封禁成员</DialogTitle>
            <DialogDescription>该操作会立即移出成员并阻止其再加入；层级校验由后端完成（不可封禁更高层级者）。</DialogDescription>
          </DialogHeader>
          <form onSubmit={onBan} className="grid gap-4">
            <div className="grid gap-1.5">
              <Label>目标成员 *</Label>
              <SimpleSelect
                ariaLabel="目标成员"
                placeholder="选择成员"
                value={targetID}
                onChange={setTargetID}
                options={(members.data ?? []).map(member => ({ value: member.user_id, label: memberName(member) }))}
                className="w-full"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ban-reason">封禁原因（建议填写，便于审计）</Label>
              <Input id="ban-reason" name="ban-reason" placeholder="如：多次恶意骚扰" maxLength={512} />
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setBanOpen(false)}>
                取消
              </Button>
              <Button type="submit" variant="destructive" disabled={banning || !targetID}>
                {banning ? "封禁中…" : "确认封禁"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <SimpleSelect
          ariaLabel="选择服务器"
          placeholder="选择服务器"
          value={guildID}
          onChange={setGuildID}
          options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
          className="w-52"
        />

        {bans.status === "loading" && <LoadingState rows={4} />}
        {bans.status === "error" && <ErrorState message={bans.error} onRetry={() => bans.reload()} />}
        {bans.status === "success" && list.length === 0 && (
          <EmptyState icon={ShieldBanIcon} title="封禁列表为空" description="被封禁的用户会显示在这里，可随时解除。" />
        )}

        {bans.status === "success" &&
          list.map((ban, index) => {
            const name = ban.username ?? ban.user_id
            return (
              <div
                key={ban.user_id}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3"
              >
                <Avatar className="size-9">
                  <AvatarFallback>{name.slice(0, 2).toUpperCase()}</AvatarFallback>
                </Avatar>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{name}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {ban.reason ? `原因：${ban.reason}` : "未填写原因"} · 封禁于 {formatTime(ban.created_at)}
                  </p>
                </div>
                <Button variant="outline" size="sm" onClick={() => onUnban(ban)}>
                  解除封禁
                </Button>
              </div>
            )
          })}
      </section>
    </main>
  )
}
