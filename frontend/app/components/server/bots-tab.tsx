// 服务器详情 · 机器人：本服创建独属 bot、签发 token、角色绑定、删除/卸载。

import { useState, type FormEvent } from "react"
import {
  BotIcon,
  KeyRoundIcon,
  PlusIcon,
  Trash2Icon,
} from "lucide-react"
import { toast } from "sonner"

import { CopyButton } from "~/components/copy-button"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "~/components/ui/dialog"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  addMemberRole,
  createGuildBot,
  createGuildBotToken,
  listGuildBotTokens,
  listGuildBots,
  listRoles,
  removeMemberRole,
  revokeGuildBotToken,
  uninstallBot,
  type BotToken,
  type GuildBot,
  type Role,
} from "~/lib/api"

export function BotsTab({ guildID }: { guildID: string }) {
  const bots = useAsyncData<GuildBot[]>(() => listGuildBots(guildID), [guildID])
  const roles = useAsyncData<Role[]>(() => listRoles(guildID), [guildID])

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">机器人</h2>
          <p className="text-sm text-muted-foreground">
            在本服创建独属机器人（自动加入成员列表），签发 bot token 后即可用 SDK
            收发消息与流式回复。需要「管理机器人」权限。
          </p>
        </div>
        <CreateGuildBotDialog
          guildID={guildID}
          onCreated={() => bots.reload(true)}
        />
      </div>

      {bots.status === "loading" && <LoadingState rows={3} />}
      {bots.status === "error" && (
        <ErrorState message={bots.error} onRetry={() => bots.reload()} />
      )}
      {bots.status === "success" && (bots.data?.length ?? 0) === 0 && (
        <EmptyState
          icon={BotIcon}
          title="本服还没有机器人"
          description="点击右上角创建，机器人将自动安装到本服务器。"
        />
      )}
      {bots.status === "success" && (bots.data?.length ?? 0) > 0 && (
        <div className="grid gap-3 lg:grid-cols-2">
          {bots.data!.map(bot => (
            <GuildBotCard
              key={bot.id}
              guildID={guildID}
              bot={bot}
              roles={(roles.data ?? []).filter(r => !r.is_everyone)}
              onChanged={() => bots.reload(true)}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function CreateGuildBotDialog({
  guildID,
  onCreated,
}: {
  guildID: string
  onCreated: () => void
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const name = String(form.get("name") ?? "").trim()
    const username = String(form.get("username") ?? "").trim()
    const description = String(form.get("description") ?? "").trim()
    if (!name || !username) return
    setBusy(true)
    try {
      await createGuildBot(guildID, { name, username, description })
      toast.success(`机器人「${name}」已创建并加入本服`)
      setOpen(false)
      onCreated()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "创建失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button />}>
        <PlusIcon data-icon="inline-start" />
        创建机器人
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>创建本服机器人</DialogTitle>
          <DialogDescription>
            将创建独立 bot 账号并自动安装到当前服务器。用户名全局唯一，不可密码登录。
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="gb-name">显示名称</Label>
            <Input id="gb-name" name="name" required minLength={2} maxLength={64} placeholder="AI 助手" />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="gb-user">用户名</Label>
            <Input id="gb-user" name="username" required minLength={2} maxLength={32} placeholder="ai-helper" />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="gb-desc">描述</Label>
            <Input id="gb-desc" name="description" maxLength={512} placeholder="可选" />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? "创建中…" : "创建并加入"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function GuildBotCard({
  guildID,
  bot,
  roles,
  onChanged,
}: {
  guildID: string
  bot: GuildBot
  roles: Role[]
  onChanged: () => void
}) {
  const guildOwned = bot.home_guild_id === guildID
  const tokens = useAsyncData<BotToken[]>(
    () => listGuildBotTokens(guildID, bot.id),
    [guildID, bot.id],
  )
  const [plain, setPlain] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function issueToken() {
    setBusy(true)
    try {
      const res = await createGuildBotToken(guildID, bot.id, { name: "default" })
      setPlain(res.plain)
      tokens.reload(true)
      toast.success("令牌已签发，请立即复制")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "签发失败")
    } finally {
      setBusy(false)
    }
  }

  async function revoke(tokenID: string) {
    if (!confirm("确定吊销该令牌？")) return
    setBusy(true)
    try {
      await revokeGuildBotToken(guildID, bot.id, tokenID)
      tokens.reload(true)
      toast.success("已吊销")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "吊销失败")
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    const msg = guildOwned
      ? `删除「${bot.name}」？将吊销全部令牌并移出本服。`
      : `卸载「${bot.name}」？`
    if (!confirm(msg)) return
    setBusy(true)
    try {
      await uninstallBot(guildID, bot.id)
      toast.success(guildOwned ? "已删除" : "已卸载")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "操作失败")
    } finally {
      setBusy(false)
    }
  }

  async function toggleRole(roleID: string, on: boolean) {
    setBusy(true)
    try {
      if (on) await addMemberRole(guildID, bot.member_id, roleID)
      else await removeMemberRole(guildID, bot.member_id, roleID)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色更新失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-2 space-y-0">
        <div className="min-w-0">
          <CardTitle className="flex flex-wrap items-center gap-2 text-base">
            <BotIcon className="size-4 text-primary" />
            {bot.name}
            <Badge variant="secondary">BOT</Badge>
            {guildOwned ? (
              <Badge variant="outline">本服独属</Badge>
            ) : (
              <Badge variant="outline">平台安装</Badge>
            )}
          </CardTitle>
          <CardDescription className="truncate">
            @{bot.username}
            {bot.description ? ` · ${bot.description}` : ""}
          </CardDescription>
        </div>
        <Button variant="ghost" size="icon-sm" disabled={busy} onClick={() => void remove()} aria-label="删除或卸载">
          <Trash2Icon className="text-destructive" />
        </Button>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="grid gap-2">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium">访问令牌</span>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => void issueToken()}>
              <KeyRoundIcon data-icon="inline-start" />
              签发
            </Button>
          </div>
          {plain && (
            <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs">
              <p className="mb-1 font-medium">明文仅显示一次：</p>
              <div className="flex items-start gap-2">
                <code className="min-w-0 flex-1 break-all font-mono">{plain}</code>
                <CopyButton text={plain} label="复制" />
              </div>
            </div>
          )}
          {tokens.status === "success" && (
            <ul className="space-y-1 text-xs">
              {(tokens.data ?? []).map(t => (
                <li key={t.id} className="flex items-center justify-between gap-2 rounded border px-2 py-1">
                  <span className="font-mono">
                    {t.prefix}… {t.name || ""}
                    {t.revoked_at ? " · 已吊销" : ""}
                  </span>
                  {!t.revoked_at && (
                    <Button size="sm" variant="ghost" className="h-7 text-destructive" disabled={busy} onClick={() => void revoke(t.id)}>
                      吊销
                    </Button>
                  )}
                </li>
              ))}
              {(tokens.data ?? []).length === 0 && (
                <li className="text-muted-foreground">暂无令牌</li>
              )}
            </ul>
          )}
        </div>

        <div className="grid gap-2">
          <span className="text-sm font-medium">角色</span>
          <div className="flex flex-wrap gap-2">
            {roles.map(role => {
              const on = bot.role_ids?.includes(role.id)
              return (
                <label
                  key={role.id}
                  className="flex cursor-pointer items-center gap-1.5 rounded border px-2 py-1 text-xs"
                >
                  <input
                    type="checkbox"
                    checked={Boolean(on)}
                    disabled={busy}
                    onChange={e => void toggleRole(role.id, e.target.checked)}
                  />
                  {role.name}
                </label>
              )
            })}
            {roles.length === 0 && (
              <span className="text-xs text-muted-foreground">暂无角色</span>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
