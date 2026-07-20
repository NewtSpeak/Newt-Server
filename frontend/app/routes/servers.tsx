import { useState, type FormEvent } from "react"
import { Link, useOutletContext } from "react-router"
import { ArrowRightIcon, PlusIcon, Users2Icon } from "lucide-react"
import { toast } from "sonner"

import { GuildAvatar } from "~/components/guild-avatar"
import { PageHeader } from "~/components/page-header"
import { EmptyState } from "~/components/states"
import { Button } from "~/components/ui/button"
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
import { createGuild } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"

export default function ServersPage() {
  const { user, guilds, addGuild } = useOutletContext<ConsoleContext>()
  const [open, setOpen] = useState(false)
  const [creating, setCreating] = useState(false)

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = String(new FormData(event.currentTarget).get("guild-name") ?? "").trim()
    if (!name) return
    setCreating(true)
    try {
      const guild = await createGuild(name)
      addGuild(guild)
      setOpen(false)
      toast.success(`服务器「${guild.name}」已创建`)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "服务器创建失败")
    } finally {
      setCreating(false)
    }
  }

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="服务器"
        description="每个服务器是独立的 RBAC 权限域；所有者自动拥有全部权限，具体裁决由后端完成。"
        actions={
          <Dialog open={open} onOpenChange={setOpen}>
            <DialogTrigger render={<Button />}>
              <PlusIcon data-icon="inline-start" />
              创建服务器
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>创建服务器</DialogTitle>
                <DialogDescription>创建后你将成为所有者，可继续配置频道、角色与节点池。</DialogDescription>
              </DialogHeader>
              <form onSubmit={onCreate} className="grid gap-4">
                <div className="grid gap-2">
                  <Label htmlFor="guild-name">服务器名称</Label>
                  <Input id="guild-name" name="guild-name" placeholder="如：产品讨论区" autoFocus required maxLength={64} />
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
        }
      />

      <section className="px-4 lg:px-6">
        {guilds.length === 0 ? (
          <EmptyState
            icon={Users2Icon}
            title="还没有服务器"
            description="创建第一个服务器，开始配置频道、成员与权限体系。"
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {guilds.map((guild, index) => (
              <Link
                key={guild.id}
                to={`/servers/${guild.id}`}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item group flex flex-col gap-3 rounded-2xl border bg-card p-5 shadow-xs transition-[border-color,box-shadow] hover:border-primary/40 hover:shadow-sm focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none active:scale-[0.99]"
              >
                <div className="flex items-center justify-between">
                  <GuildAvatar guild={guild} className="size-10" />
                  <ArrowRightIcon className="size-4 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
                </div>
                <div className="min-w-0">
                  <h2 className="truncate font-medium">{guild.name}</h2>
                  <p className="mt-1 truncate font-mono text-xs text-muted-foreground">{guild.id}</p>
                </div>
                {guild.owner_user_id === user.id && (
                  <span className="w-fit rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">所有者</span>
                )}
              </Link>
            ))}
          </div>
        )}
      </section>
    </main>
  )
}
