import { useRef, useState } from "react"
import { useOutletContext } from "react-router"
import { InfoIcon, UserRoundPenIcon } from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { PasswordCard } from "~/components/password-card"
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Field, FieldDescription, FieldLabel } from "~/components/ui/field"
import { Input } from "~/components/ui/input"
import { FramedAvatar } from "~/components/user-avatar-frame"
import { useAvatarFrames } from "~/hooks/use-avatar-frames"
import { changeMyUsername, type User } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"

export default function AccountPage() {
  const { user, updateUser } = useOutletContext<ConsoleContext>()

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="账号安全"
        description="修改登录用户名与密码，摆脱部署时的默认凭据。邮箱登录始终可用。"
      />

      <section className="grid gap-4 px-4 lg:px-6">
        <div className="anim-item" style={{ "--stagger-index": 0 } as React.CSSProperties}>
          <OverviewCard user={user} />
        </div>
      </section>

      <section className="grid gap-4 px-4 lg:grid-cols-2 lg:px-6">
        <div className="anim-item" style={{ "--stagger-index": 1 } as React.CSSProperties}>
          <UsernameCard user={user} onUpdated={updateUser} />
        </div>
        <div className="anim-item" style={{ "--stagger-index": 2 } as React.CSSProperties}>
          <PasswordCard />
        </div>
      </section>
    </main>
  )
}

/** 账号概览：当前登录身份一览；用户名变更时值做 text-swap 入场 + 高亮衰减 */
function OverviewCard({ user }: { user: User }) {
  const rowRef = useRef<HTMLDivElement>(null)
  const highlightRef = useRef<HTMLSpanElement>(null)
  const previousUsername = useRef(user.username)
  // 当前管理员本人的头像框
  const avatarFrames = useAvatarFrames([user.id])

  // 改名成功后对用户名行做一次高亮衰减（纯 opacity，尊重 prefers-reduced-motion）
  useGSAP(
    () => {
      if (previousUsername.current === user.username) return
      previousUsername.current = user.username
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.fromTo(
          highlightRef.current,
          { autoAlpha: 1 },
          { autoAlpha: 0, duration: 0.9, ease: MOTION.ease }
        )
      })
    },
    { dependencies: [user.username], scope: rowRef }
  )

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-4">
          <FramedAvatar frame={avatarFrames[user.id]}>
            <Avatar className="size-14">
              {user.avatar_url && <AvatarImage src={user.avatar_url} alt="" />}
              <AvatarFallback className="text-lg">
                {(user.username || "?").slice(0, 2).toUpperCase()}
              </AvatarFallback>
            </Avatar>
          </FramedAvatar>
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2 text-base">
              <span key={user.username} className="t-text-swap truncate">
                {user.username}
              </span>
              <Badge variant={user.system_admin ? "default" : "secondary"}>
                {user.system_admin ? "系统管理员" : "普通用户"}
              </Badge>
            </CardTitle>
            <CardDescription className="truncate">{user.email}</CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm sm:grid-cols-2">
        <div ref={rowRef} className="relative flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
          <span
            ref={highlightRef}
            aria-hidden
            className="pointer-events-none absolute inset-0 rounded-xl bg-primary/10 opacity-0"
          />
          <span className="text-muted-foreground">登录用户名</span>
          <span key={user.username} className="t-text-swap font-medium">
            {user.username}
          </span>
        </div>
        <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
          <span className="text-muted-foreground">登录邮箱</span>
          <span className="truncate font-medium">{user.email}</span>
        </div>
      </CardContent>
    </Card>
  )
}

/** 修改用户名：密码二次确认；成功后同步布局态与 localStorage，现有会话不受影响 */
function UsernameCard({ user, onUpdated }: { user: User; onUpdated: (user: User) => void }) {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [fieldError, setFieldError] = useState("")
  const [busy, setBusy] = useState(false)

  function validate(value: string): string {
    const trimmed = value.trim()
    const length = [...trimmed].length
    if (length < 2 || length > 32) return "用户名长度需为 2-32 个字符"
    if (/[\s@]/.test(trimmed)) return "用户名不能包含空白字符或 @"
    return ""
  }

  const trimmed = username.trim()
  const valid = trimmed && !validate(username)

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const message = validate(username)
    if (message) {
      setFieldError(message)
      return
    }
    setBusy(true)
    try {
      const fresh = await changeMyUsername(trimmed, password)
      onUpdated(fresh)
      toast.success(`用户名已更新为 ${fresh.username}`)
      setUsername("")
      setPassword("")
      setFieldError("")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "用户名修改失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <UserRoundPenIcon className="size-4" />
          修改用户名
        </CardTitle>
        <CardDescription>
          改名后现有登录不受影响；下次登录请使用新用户名或邮箱。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={onSubmit} className="grid gap-4">
          <Field data-invalid={!!fieldError || undefined}>
            <FieldLabel htmlFor="account-username">新用户名</FieldLabel>
            <Input
              id="account-username"
              autoComplete="username"
              placeholder={user.username}
              value={username}
              minLength={2}
              maxLength={32}
              aria-invalid={!!fieldError || undefined}
              onChange={event => {
                setUsername(event.target.value)
                if (fieldError) setFieldError("")
              }}
              onBlur={() => setFieldError(username.trim() ? validate(username) : "")}
              required
            />
            {fieldError ? (
              <p role="alert" className="text-sm text-destructive">
                {fieldError}
              </p>
            ) : (
              <FieldDescription>2-32 个字符，不含空白与 @。</FieldDescription>
            )}
          </Field>
          <Field>
            <FieldLabel htmlFor="account-username-password">当前密码</FieldLabel>
            <Input
              id="account-username-password"
              type="password"
              autoComplete="current-password"
              placeholder="验证身份后才能修改登录标识"
              value={password}
              onChange={event => setPassword(event.target.value)}
              required
            />
          </Field>
          <div className="flex items-center justify-between gap-3">
            <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
              <InfoIcon className="mt-0.5 size-3.5 shrink-0" />
              自动化脚本若写死了旧用户名，改名后需同步更新。
            </p>
            <Button type="submit" size="sm" disabled={busy || !valid || !password}>
              {busy ? "提交中…" : "修改用户名"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}
