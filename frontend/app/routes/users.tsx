import { useEffect, useState } from "react"
import { useOutletContext } from "react-router"
import {
  BanIcon,
  BotIcon,
  CheckCircle2Icon,
  KeyRoundIcon,
  SearchIcon,
  ShieldCheckIcon,
  ShieldMinusIcon,
  ShieldPlusIcon,
  Users2Icon,
} from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
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
} from "~/components/ui/dialog"
import { Input } from "~/components/ui/input"
import { Switch } from "~/components/ui/switch"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  disablePlatformUser,
  enablePlatformUser,
  getRegistrationSetting,
  listPlatformUsers,
  patchPlatformUserSystemAdmin,
  putRegistrationSetting,
  resetPlatformUserPassword,
  type PlatformUser,
  type PlatformUserFilter,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"

const PAGE_SIZE = 50

const FILTER_OPTIONS = [
  { value: "all", label: "全部用户" },
  { value: "admin", label: "系统管理员" },
  { value: "disabled", label: "已禁用" },
  { value: "bot", label: "机器人" },
]

/** 平台用户管理（系统管理员）：目录/禁用/重置密码/系统管理员授予 + 注册开关 */
export default function PlatformUsersPage() {
  const { user: me } = useOutletContext<ConsoleContext>()
  const [query, setQuery] = useState("")
  const [search, setSearch] = useState("")
  const [filter, setFilter] = useState("all")
  const [offset, setOffset] = useState(0)
  const [resetTarget, setResetTarget] = useState<PlatformUser | null>(null)

  const page = useAsyncData(
    () =>
      listPlatformUsers({
        q: search || undefined,
        filter: filter === "all" ? undefined : (filter as PlatformUserFilter),
        limit: PAGE_SIZE,
        offset,
      }),
    [search, filter, offset]
  )

  useEffect(() => {
    const timer = setTimeout(() => {
      setSearch(query.trim())
      setOffset(0)
    }, 300)
    return () => clearTimeout(timer)
  }, [query])

  const users = page.data?.users ?? []
  const total = page.data?.total ?? 0

  async function run(action: () => Promise<unknown>, message: string) {
    try {
      await action()
      toast.success(message)
      page.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "操作失败")
    }
  }

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader title="平台用户" description="全平台账号目录：禁用/解禁、重置密码、系统管理员授予与注册开关。" />

      <section className="px-4 lg:px-6">
        <RegistrationCard />
      </section>

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              aria-label="搜索用户"
              placeholder="搜索用户名 / 邮箱"
              value={query}
              onChange={event => setQuery(event.target.value)}
              className="w-64 pl-8"
            />
          </div>
          <SimpleSelect ariaLabel="筛选" value={filter} onChange={setFilter} options={FILTER_OPTIONS} className="w-36" />
          <p className="ml-auto text-sm text-muted-foreground">
            共 <span className="tabular-nums">{total}</span> 个账号
          </p>
        </div>

        {page.status === "loading" && <LoadingState rows={6} />}
        {page.status === "error" && <ErrorState message={page.error} onRetry={() => page.reload()} />}
        {page.status === "success" && users.length === 0 && (
          <EmptyState icon={Users2Icon} title="没有匹配的用户" description="调整搜索或筛选条件。" />
        )}
        {page.status === "success" &&
          users.map((user, index) => {
            const isSelf = user.id === me.id
            const disabled = Boolean(user.disabled_at)
            return (
              <div
                key={user.id}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3"
              >
                <Avatar className="size-9">
                  {user.avatar_url && <AvatarImage src={user.avatar_url} alt="" />}
                  <AvatarFallback>{user.username.slice(0, 2).toUpperCase()}</AvatarFallback>
                </Avatar>
                <div className="min-w-0 flex-1">
                  <p className="flex items-center gap-1.5 truncate text-sm font-medium">
                    {user.username}
                    {isSelf && <Badge variant="outline">我</Badge>}
                    {user.system_admin && (
                      <Badge variant="default" className="gap-1">
                        <ShieldCheckIcon className="size-3" />
                        系统管理员
                      </Badge>
                    )}
                    {user.is_bot && (
                      <Badge variant="secondary" className="gap-1">
                        <BotIcon className="size-3" />
                        机器人
                      </Badge>
                    )}
                    {disabled && (
                      <Badge variant="destructive" className="gap-1">
                        <BanIcon className="size-3" />
                        已禁用
                      </Badge>
                    )}
                  </p>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {user.email} · {user.id}
                  </p>
                </div>
                <div className="flex items-center gap-1.5">
                  {!user.is_bot && (
                    <Button variant="outline" size="xs" onClick={() => setResetTarget(user)}>
                      <KeyRoundIcon data-icon="inline-start" />
                      重置密码
                    </Button>
                  )}
                  {!user.is_bot && !isSelf && (
                    <Button
                      variant="outline"
                      size="xs"
                      onClick={() =>
                        run(
                          () => patchPlatformUserSystemAdmin(user.id, !user.system_admin),
                          user.system_admin ? "已回收系统管理员" : "已授予系统管理员"
                        )
                      }
                    >
                      {user.system_admin ? (
                        <ShieldMinusIcon data-icon="inline-start" />
                      ) : (
                        <ShieldPlusIcon data-icon="inline-start" />
                      )}
                      {user.system_admin ? "回收管理员" : "授予管理员"}
                    </Button>
                  )}
                  {!isSelf &&
                    (disabled ? (
                      <Button
                        variant="outline"
                        size="xs"
                        onClick={() => run(() => enablePlatformUser(user.id), "账号已解禁")}
                      >
                        <CheckCircle2Icon data-icon="inline-start" />
                        解禁
                      </Button>
                    ) : (
                      <Button
                        variant="destructive"
                        size="xs"
                        onClick={() => {
                          if (window.confirm(`确定禁用「${user.username}」？其全部登录会话将被吊销。`))
                            void run(() => disablePlatformUser(user.id), "账号已禁用")
                        }}
                      >
                        <BanIcon data-icon="inline-start" />
                        禁用
                      </Button>
                    ))}
                </div>
              </div>
            )
          })}

        {total > PAGE_SIZE && (
          <div className="flex items-center justify-center gap-3">
            <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>
              上一页
            </Button>
            <span className="text-xs tabular-nums text-muted-foreground">
              {offset + 1}–{Math.min(offset + PAGE_SIZE, total)} / {total}
            </span>
            <Button
              variant="outline"
              size="sm"
              disabled={offset + PAGE_SIZE >= total}
              onClick={() => setOffset(offset + PAGE_SIZE)}
            >
              下一页
            </Button>
          </div>
        )}
      </section>

      <ResetPasswordDialog target={resetTarget} onClose={() => setResetTarget(null)} />
    </main>
  )
}

function RegistrationCard() {
  const setting = useAsyncData(() => getRegistrationSetting(), [])
  const [saving, setSaving] = useState(false)

  async function onToggle(next: boolean) {
    setSaving(true)
    try {
      await putRegistrationSetting(next)
      toast.success(next ? "用户端注册已开放" : "用户端注册已关闭")
      setting.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">用户端注册开关</CardTitle>
        <CardDescription>
          控制 /gapi 用户端是否开放注册（即时生效、落库持久）；关闭后已有账号仍可凭邀请免注册加入服务器。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex items-center gap-3">
        <Switch
          checked={Boolean(setting.data?.signup_enabled)}
          disabled={saving || setting.status !== "success"}
          onCheckedChange={next => onToggle(Boolean(next))}
          aria-label="用户端注册开关"
        />
        <span className="text-sm">{setting.data?.signup_enabled ? "开放注册" : "关闭注册"}</span>
        {setting.data && (
          <Badge variant="outline">{setting.data.source === "db" ? "控制台配置" : "环境变量默认"}</Badge>
        )}
      </CardContent>
    </Card>
  )
}

function ResetPasswordDialog({ target, onClose }: { target: PlatformUser | null; onClose: () => void }) {
  const [password, setPassword] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (target) setPassword("")
  }, [target?.id])

  async function onReset() {
    if (!target) return
    setBusy(true)
    try {
      await resetPlatformUserPassword(target.id, password)
      toast.success(`「${target.username}」密码已重置，全部会话已吊销`)
      onClose()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "重置失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={target !== null} onOpenChange={open => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>重置密码 · {target?.username}</DialogTitle>
          <DialogDescription>设置新密码（至少 8 位）；该用户全部登录会话将被吊销，需用新密码重新登录。</DialogDescription>
        </DialogHeader>
        <Input
          type="password"
          aria-label="新密码"
          value={password}
          onChange={event => setPassword(event.target.value)}
          placeholder="新密码（≥8 位）"
          minLength={8}
          maxLength={128}
          autoFocus
        />
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onReset} disabled={password.length < 8 || busy}>
            {busy ? "重置中…" : "确认重置"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
