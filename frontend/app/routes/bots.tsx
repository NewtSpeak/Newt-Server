import { useState, type FormEvent } from "react"
import { useOutletContext } from "react-router"
import {
  BotIcon,
  KeyRoundIcon,
  PlusIcon,
  PuzzleIcon,
  ShieldCheckIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react"
import { toast } from "sonner"

import { CopyButton } from "~/components/copy-button"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback } from "~/components/ui/avatar"
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { useAsyncData } from "~/hooks/use-async-data"
import { useGuildID } from "~/hooks/use-guild-id"
import {
  addMemberRole,
  createBot,
  createBotToken,
  deleteBot,
  installBot,
  listBots,
  listBotTokens,
  listGuildBots,
  listRoles,
  removeMemberRole,
  revokeBotToken,
  uninstallBot,
  type Bot,
  type BotToken,
  type GuildBot,
  type Role,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { formatTime } from "~/lib/format"

export default function BotsPage() {
  const { guilds } = useOutletContext<ConsoleContext>()
  const bots = useAsyncData<Bot[]>(() => listBots(), [])

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="机器人"
        description="机器人复用「用户 + 成员 + 角色」权限体系：创建后签发独立 bot token，安装到服务器并绑定角色即可独立收发消息、卡片、流式回复与接入语音。"
        actions={<CreateBotDialog onCreated={() => bots.reload(true)} />}
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        {bots.status === "loading" && <LoadingState rows={4} />}
        {bots.status === "error" && <ErrorState message={bots.error} onRetry={() => bots.reload()} />}
        {bots.status === "success" && (bots.data?.length ?? 0) === 0 && (
          <EmptyState icon={BotIcon} title="还没有机器人" description="创建第一个机器人，为它签发 token 并安装到服务器。" />
        )}
        {bots.status === "success" && (bots.data?.length ?? 0) > 0 && (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {bots.data!.map((bot, index) => (
              <BotCard key={bot.id} bot={bot} index={index} onChanged={() => bots.reload(true)} />
            ))}
          </div>
        )}
      </section>

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <GuildInstallSection guilds={guilds} bots={bots.data ?? []} onChanged={() => bots.reload(true)} />
      </section>
    </main>
  )
}

// ---------------------------------------------------------------------------
// 创建机器人
// ---------------------------------------------------------------------------

function CreateBotDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false)
  const [creating, setCreating] = useState(false)

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const name = String(form.get("bot-name") ?? "").trim()
    const username = String(form.get("bot-username") ?? "").trim()
    const description = String(form.get("bot-description") ?? "").trim()
    if (!name || !username) return
    setCreating(true)
    try {
      const bot = await createBot({ name, username, description })
      toast.success(`机器人「${bot.name}」已创建，请为它签发 token`)
      setOpen(false)
      onCreated()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "机器人创建失败")
    } finally {
      setCreating(false)
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
          <DialogTitle>创建机器人</DialogTitle>
          <DialogDescription>将同时创建一个独立的机器人账号（不可密码登录，仅 bot token 鉴权）。</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="bot-name">显示名称</Label>
            <Input id="bot-name" name="bot-name" placeholder="如：AI 助手" autoFocus required minLength={2} maxLength={64} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="bot-username">用户名（全局唯一）</Label>
            <Input id="bot-username" name="bot-username" placeholder="如：ai-assistant" required minLength={2} maxLength={32} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="bot-description">描述（可选）</Label>
            <Input id="bot-description" name="bot-description" placeholder="它能做什么？" maxLength={512} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button type="submit" disabled={creating}>
              {creating ? "创建中…" : "创建"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// 机器人卡片（含令牌管理与删除）
// ---------------------------------------------------------------------------

function BotCard({ bot, index, onChanged }: { bot: Bot; index: number; onChanged: () => void }) {
  async function onDelete() {
    if (!window.confirm(`确定删除机器人「${bot.name}」？其全部 token 将被吊销并退出所有服务器。`)) return
    try {
      await deleteBot(bot.id)
      toast.success("机器人已删除")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "删除失败")
    }
  }

  return (
    <div
      style={{ "--stagger-index": index } as React.CSSProperties}
      className="anim-item flex flex-col gap-3 rounded-2xl border bg-card p-5 shadow-xs"
    >
      <div className="flex items-center gap-3">
        <span className="grid size-10 place-items-center rounded-xl bg-primary/10 text-primary">
          <BotIcon className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <h2 className="truncate font-medium">{bot.name}</h2>
            <Badge variant="secondary" className="h-5 px-1.5 text-[10px]">
              BOT
            </Badge>
          </div>
          <p className="truncate font-mono text-xs text-muted-foreground">@{bot.username}</p>
        </div>
      </div>
      {bot.description && <p className="line-clamp-2 text-sm text-muted-foreground">{bot.description}</p>}
      <div className="flex items-center gap-3 text-xs text-muted-foreground tabular-nums">
        <span>{bot.token_count} 个有效 token</span>
        <span>已安装 {bot.guild_count} 个服务器</span>
      </div>
      <div className="mt-auto flex items-center gap-2">
        <TokenDialog bot={bot} onChanged={onChanged} />
        <Button variant="destructive" size="icon-sm" aria-label="删除机器人" onClick={onDelete}>
          <Trash2Icon />
        </Button>
      </div>
    </div>
  )
}

function TokenDialog({ bot, onChanged }: { bot: Bot; onChanged: () => void }) {
  const [open, setOpen] = useState(false)
  const tokens = useAsyncData<BotToken[]>(open ? () => listBotTokens(bot.id) : null, [open, bot.id])
  const [plainToken, setPlainToken] = useState<string | null>(null)
  const [issuing, setIssuing] = useState(false)

  async function onIssue() {
    setIssuing(true)
    try {
      const created = await createBotToken(bot.id, { name: `token-${new Date().toISOString().slice(0, 10)}` })
      setPlainToken(created.plain)
      tokens.reload(true)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "签发失败")
    } finally {
      setIssuing(false)
    }
  }

  async function onRevoke(token: BotToken) {
    if (!window.confirm(`确定吊销 token「${token.prefix}…」？使用该 token 的机器人将立即掉线。`)) return
    try {
      await revokeBotToken(bot.id, token.id)
      toast.success("token 已吊销")
      tokens.reload(true)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "吊销失败")
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={next => {
        setOpen(next)
        if (!next) setPlainToken(null)
      }}
    >
      <DialogTrigger render={<Button variant="outline" size="sm" className="flex-1" />}>
        <KeyRoundIcon data-icon="inline-start" />
        token 管理
      </DialogTrigger>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>「{bot.name}」的访问令牌</DialogTitle>
          <DialogDescription>token 用于 SDK / API 鉴权（Authorization: Bot &lt;token&gt;），明文仅签发时显示一次。</DialogDescription>
        </DialogHeader>

        {plainToken && (
          <div className="grid gap-2 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-3">
            <p className="text-xs font-medium text-emerald-600 dark:text-emerald-400">新 token 已签发，请立即保存（关闭后无法再次查看）：</p>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate font-mono text-xs">{plainToken}</code>
              <CopyButton text={plainToken} />
            </div>
          </div>
        )}

        {tokens.status === "loading" && <LoadingState rows={2} />}
        {tokens.status === "error" && <ErrorState message={tokens.error} onRetry={() => tokens.reload()} />}
        {tokens.status === "success" && (
          <div className="flex flex-col gap-2">
            {(tokens.data ?? []).length === 0 && <p className="text-sm text-muted-foreground">还没有 token。</p>}
            {(tokens.data ?? []).map(token => (
              <div key={token.id} className="flex items-center gap-3 rounded-xl border px-3 py-2">
                <code className="font-mono text-xs">{token.prefix}…</code>
                <div className="min-w-0 flex-1 text-xs text-muted-foreground">
                  <span className="truncate">{token.name || "未命名"}</span>
                  <span className="ml-2">
                    {token.revoked_at
                      ? "已吊销"
                      : token.last_used_at
                        ? `最近使用 ${formatTime(token.last_used_at)}`
                        : "从未使用"}
                  </span>
                </div>
                {!token.revoked_at && (
                  <Button variant="ghost" size="icon-sm" aria-label="吊销" onClick={() => onRevoke(token)}>
                    <XIcon />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}

        <DialogFooter>
          <Button onClick={onIssue} disabled={issuing}>
            <PlusIcon data-icon="inline-start" />
            {issuing ? "签发中…" : "签发新 token"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// 安装到服务器 + 角色（权限）赋予
// ---------------------------------------------------------------------------

function GuildInstallSection({
  guilds,
  bots,
  onChanged,
}: {
  guilds: ConsoleContext["guilds"]
  bots: Bot[]
  onChanged: () => void
}) {
  const [guildID, setGuildID] = useGuildID(guilds)
  const installed = useAsyncData<GuildBot[]>(guildID ? () => listGuildBots(guildID) : null, [guildID])
  const roles = useAsyncData<Role[]>(guildID ? () => listRoles(guildID) : null, [guildID])

  const installedIDs = new Set((installed.data ?? []).map(item => item.id))
  const installable = bots.filter(bot => !installedIDs.has(bot.id))
  const roleByID = new Map((roles.data ?? []).map(role => [role.id, role]))

  async function onInstall(bot: Bot) {
    if (!guildID) return
    try {
      await installBot(guildID, bot.id)
      toast.success(`「${bot.name}」已安装，可继续为它绑定角色赋权`)
      installed.reload(true)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "安装失败")
    }
  }

  async function onUninstall(bot: GuildBot) {
    if (!guildID) return
    if (!window.confirm(`确定将「${bot.name}」从该服务器卸载？其角色绑定将被清除。`)) return
    try {
      await uninstallBot(guildID, bot.id)
      toast.success("机器人已卸载")
      installed.reload(true)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "卸载失败")
    }
  }

  async function bindRole(bot: GuildBot, roleID: string) {
    if (!guildID) return
    try {
      await addMemberRole(guildID, bot.member_id, roleID)
      toast.success("角色已绑定")
      installed.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色绑定失败")
    }
  }

  async function unbindRole(bot: GuildBot, roleID: string) {
    if (!guildID) return
    try {
      await removeMemberRole(guildID, bot.member_id, roleID)
      toast.success("角色已解绑")
      installed.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色解绑失败")
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">安装到服务器与权限赋予</CardTitle>
        <CardDescription>
          安装 = 让机器人成为该服成员；随后手动绑定角色即可精确控制它的文字 / 语音 / 管理权限（与人类成员同一套 RBAC）。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <SimpleSelect
            ariaLabel="选择服务器"
            placeholder="选择服务器"
            value={guildID}
            onChange={setGuildID}
            options={guilds.map(guild => ({ value: guild.id, label: guild.name }))}
            className="w-52"
          />
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button variant="outline" size="sm" disabled={!guildID || installable.length === 0}>
                  <PuzzleIcon data-icon="inline-start" />
                  安装机器人
                </Button>
              }
            />
            <DropdownMenuContent align="start">
              <DropdownMenuLabel>选择机器人</DropdownMenuLabel>
              {installable.map(bot => (
                <DropdownMenuItem key={bot.id} onClick={() => onInstall(bot)}>
                  <BotIcon />
                  {bot.name}
                  <span className="ml-auto font-mono text-xs text-muted-foreground">@{bot.username}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        {installed.status === "loading" && <LoadingState rows={3} />}
        {installed.status === "error" && <ErrorState message={installed.error} onRetry={() => installed.reload()} />}
        {installed.status === "success" && (installed.data?.length ?? 0) === 0 && (
          <EmptyState icon={BotIcon} title="该服务器还没有机器人" description="从上方「安装机器人」选择一个已创建的机器人。" />
        )}
        {installed.status === "success" &&
          (installed.data ?? []).map((bot, index) => {
            const boundIDs = new Set(bot.role_ids)
            const assignable = (roles.data ?? []).filter(role => !role.is_everyone && !boundIDs.has(role.id))
            return (
              <div
                key={bot.id}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 transition-[background-color] hover:bg-muted/40"
              >
                <Avatar className="size-9">
                  <AvatarFallback>
                    <BotIcon className="size-4" />
                  </AvatarFallback>
                </Avatar>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <p className="truncate text-sm font-medium">{bot.name}</p>
                    <Badge variant="secondary" className="h-5 px-1.5 text-[10px]">
                      BOT
                    </Badge>
                  </div>
                  <p className="truncate font-mono text-xs text-muted-foreground">@{bot.username}</p>
                </div>
                <div className="flex flex-wrap items-center gap-1.5">
                  {bot.role_ids
                    .map(roleID => roleByID.get(roleID))
                    .filter((role): role is Role => Boolean(role))
                    .map(role => (
                      <Badge key={role.id} variant="secondary" className="h-6 gap-1 pr-1">
                        <ShieldCheckIcon className="size-3" />
                        {role.name}
                        <button
                          type="button"
                          aria-label={`移除角色 ${role.name}`}
                          onClick={() => unbindRole(bot, role.id)}
                          className="grid size-4 place-items-center rounded-full transition-[background-color] hover:bg-foreground/10"
                        >
                          <XIcon className="size-3" />
                        </button>
                      </Badge>
                    ))}
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      render={
                        <Button variant="outline" size="xs" disabled={assignable.length === 0}>
                          + 绑定角色
                        </Button>
                      }
                    />
                    <DropdownMenuContent align="end">
                      <DropdownMenuLabel>选择角色</DropdownMenuLabel>
                      {assignable.map(role => (
                        <DropdownMenuItem key={role.id} onClick={() => bindRole(bot, role.id)}>
                          <ShieldCheckIcon />
                          {role.name}
                          <span className="ml-auto font-mono text-xs text-muted-foreground">P{role.position}</span>
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <Button variant="destructive" size="icon-sm" aria-label="卸载机器人" onClick={() => onUninstall(bot)}>
                  <Trash2Icon />
                </Button>
              </div>
            )
          })}
      </CardContent>
    </Card>
  )
}
