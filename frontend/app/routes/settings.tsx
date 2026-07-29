import { useRef, useState } from "react"
import { useTheme } from "next-themes"
import { useOutletContext } from "react-router"
import {
  ImageIcon,
  MonitorIcon,
  MonitorSmartphoneIcon,
  MoonIcon,
  SunIcon,
  Trash2Icon,
  UserRoundIcon,
  XIcon,
} from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { PasswordCard } from "~/components/password-card"
import { ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { useAsyncData } from "~/hooks/use-async-data"
import { Input } from "~/components/ui/input"
import {
  api,
  listMySessions,
  patchMyProfile,
  revokeMySession,
  revokeOtherSessions,
  uploadMyProfileImage,
  type RegistrationStatus,
  type User,
} from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { cn } from "~/lib/utils"

const THEME_OPTIONS = [
  { value: "dark", label: "暗色", icon: MoonIcon, note: "默认推荐" },
  { value: "light", label: "亮色", icon: SunIcon, note: "明亮环境" },
  { value: "system", label: "跟随系统", icon: MonitorIcon, note: "自动切换" },
] as const

export default function SettingsPage() {
  const { user } = useOutletContext<ConsoleContext>()
  const { theme, setTheme } = useTheme()
  const registration = useAsyncData<RegistrationStatus>(() => api<RegistrationStatus>("/auth/registration-status"), [])

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader title="系统设置" description="账号信息、外观偏好与平台运行状态。" />

      <section className="grid gap-4 px-4 lg:px-6">
        <ProfileCard initialUser={user} />
      </section>

      <section className="grid gap-4 px-4 lg:grid-cols-2 lg:px-6">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">外观</CardTitle>
            <CardDescription>管理台以暗色为默认主题，可按环境切换。</CardDescription>
          </CardHeader>
          <CardContent>
            <div role="radiogroup" aria-label="主题" className="grid grid-cols-3 gap-2">
              {THEME_OPTIONS.map(option => (
                <button
                  key={option.value}
                  type="button"
                  role="radio"
                  aria-checked={theme === option.value}
                  onClick={() => setTheme(option.value)}
                  className={cn(
                    "flex flex-col items-center gap-1.5 rounded-xl border p-4 transition-[background-color,border-color] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none active:scale-[0.98]",
                    theme === option.value ? "border-primary/60 bg-primary/5" : "hover:bg-muted/50"
                  )}
                >
                  <option.icon className="size-5" />
                  <span className="text-sm font-medium">{option.label}</span>
                  <span className="text-[10px] text-muted-foreground">{option.note}</span>
                </button>
              ))}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">账号</CardTitle>
            <CardDescription>令牌自动刷新；权限由 Newt-Server 权威裁决。</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
              <span className="text-muted-foreground">用户名</span>
              <span className="font-medium">{user.username}</span>
            </div>
            <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
              <span className="text-muted-foreground">邮箱</span>
              <span className="font-medium">{user.email}</span>
            </div>
            <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
              <span className="text-muted-foreground">平台身份</span>
              <Badge variant={user.system_admin ? "default" : "secondary"}>
                {user.system_admin ? "系统管理员" : "普通用户"}
              </Badge>
            </div>
            <div className="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5">
              <span className="text-muted-foreground">开放注册</span>
              <Badge variant="outline">
                {registration.status === "success" ? (registration.data?.registration_open ? "开启" : "关闭") : "—"}
              </Badge>
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 px-4 lg:grid-cols-2 lg:px-6">
        <PasswordCard />
        <SessionsCard />
      </section>
    </main>
  )
}

const PLATFORM_LABELS: Record<string, string> = {
  windows: "Windows",
  macos: "macOS",
  linux: "Linux",
  android: "Android",
  ios: "iOS",
}

/** 登录会话管理：列出全部活跃会话（含设备/平台/IP 元数据），可单个吊销或一键登出其他设备 */
function SessionsCard() {
  const sessions = useAsyncData(() => listMySessions(), [])
  const [revokingOthers, setRevokingOthers] = useState(false)

  async function onRevoke(sessionID: string, current: boolean) {
    if (current && !window.confirm("这是当前会话，吊销后你将被登出。确定继续？")) return
    try {
      await revokeMySession(sessionID)
      toast.success("会话已吊销")
      sessions.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "吊销失败")
    }
  }

  async function onRevokeOthers() {
    if (!window.confirm("确定登出所有其他设备？其他端需重新登录。")) return
    setRevokingOthers(true)
    try {
      const result = await revokeOtherSessions()
      toast.success(`已登出 ${result.revoked} 个其他会话`)
      sessions.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "操作失败")
    } finally {
      setRevokingOthers(false)
    }
  }

  const list = sessions.data ?? []
  const others = list.filter(session => !session.current).length

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <MonitorSmartphoneIcon className="size-4" />
          登录会话
        </CardTitle>
        <CardDescription>全部活跃登录（后台管理 + 用户端），含设备与 IP 信息；吊销后对应端需重新登录。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {sessions.status === "loading" && <LoadingState rows={3} />}
        {sessions.status === "error" && <ErrorState message={sessions.error} onRetry={() => sessions.reload()} />}
        {sessions.status === "success" &&
          list.map(session => {
            const device = [
              session.device_name,
              session.platform && session.platform !== "unknown" ? PLATFORM_LABELS[session.platform] ?? session.platform : "",
              session.ip_address,
            ]
              .filter(Boolean)
              .join(" · ")
            return (
              <div key={session.id} className="flex items-center gap-3 rounded-xl border px-3 py-2.5 text-sm">
                <Badge variant={session.audience === "admin" ? "default" : "secondary"}>
                  {session.audience === "admin" ? "后台" : "用户端"}
                </Badge>
                <div className="min-w-0 flex-1">
                  {device && <p className="truncate text-xs font-medium">{device}</p>}
                  <p className="truncate text-xs text-muted-foreground">
                    登录于 {new Date(session.created_at).toLocaleString()} · 最近使用{" "}
                    {new Date(session.last_used_at).toLocaleString()}
                  </p>
                </div>
                {session.current && <Badge variant="outline">当前会话</Badge>}
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="吊销会话"
                  onClick={() => onRevoke(session.id, session.current)}
                >
                  <XIcon />
                </Button>
              </div>
            )
          })}
        {sessions.status === "success" && list.length === 0 && (
          <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">暂无活跃会话</p>
        )}
        {sessions.status === "success" && others > 0 && (
          <div className="flex justify-end pt-1">
            <Button variant="outline" size="sm" onClick={onRevokeOthers} disabled={revokingOthers}>
              {revokingOthers ? "处理中…" : `登出所有其他设备（${others}）`}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/** 个人资料卡：上传动态/静态头像、个人横幅（banner），设置强调色 */
function ProfileCard({ initialUser }: { initialUser: User }) {
  const [profile, setProfile] = useState<User>(initialUser)
  const [busy, setBusy] = useState(false)
  const avatarInput = useRef<HTMLInputElement>(null)
  const bannerInput = useRef<HTMLInputElement>(null)

  async function onPickFile(kind: "avatar" | "banner", file: File | undefined) {
    if (!file) return
    setBusy(true)
    try {
      const next = await uploadMyProfileImage(kind, file)
      setProfile(next)
      toast.success(kind === "avatar" ? "头像已更新" : "横幅已更新")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "上传失败")
    } finally {
      setBusy(false)
    }
  }

  async function onPatch(body: Parameters<typeof patchMyProfile>[0], message: string) {
    setBusy(true)
    try {
      const next = await patchMyProfile(body)
      setProfile(next)
      toast.success(message)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card className="overflow-hidden pt-0">
      <div
        className="relative h-28 w-full bg-muted"
        style={
          profile.banner_url
            ? { backgroundImage: `url(${profile.banner_url})`, backgroundSize: "cover", backgroundPosition: "center" }
            : profile.accent_color
              ? { backgroundColor: profile.accent_color }
              : { background: "linear-gradient(90deg, #6366f1, #8b5cf6)" }
        }
      >
        <div className="absolute right-3 bottom-3 flex gap-1.5">
          <Button variant="secondary" size="xs" disabled={busy} onClick={() => bannerInput.current?.click()}>
            <ImageIcon data-icon="inline-start" />
            更换横幅
          </Button>
          {profile.banner_url && (
            <Button
              variant="secondary"
              size="icon-xs"
              aria-label="移除横幅"
              disabled={busy}
              onClick={() => onPatch({ clear_banner: true }, "横幅已移除")}
            >
              <Trash2Icon />
            </Button>
          )}
        </div>
      </div>
      <CardHeader className="-mt-12">
        <div className="flex items-end gap-4">
          <div className="relative">
            <Avatar className="size-20 ring-4 ring-background">
              {profile.avatar_url && <AvatarImage src={profile.avatar_url} alt="" />}
              <AvatarFallback className="text-xl">
                {(profile.username || "?").slice(0, 2).toUpperCase()}
              </AvatarFallback>
            </Avatar>
            {profile.avatar_animated && (
              <span className="absolute -right-1 -bottom-1 rounded-md border bg-background px-1 font-mono text-[9px]">GIF</span>
            )}
          </div>
          <div className="flex flex-1 flex-wrap items-center gap-2 pb-1">
            <div className="min-w-0 flex-1">
              <CardTitle className="text-base">{profile.username}</CardTitle>
              <CardDescription>头像支持 PNG/JPEG/WebP，GIF 自动识别为动态头像。</CardDescription>
            </div>
            <Button variant="outline" size="sm" disabled={busy} onClick={() => avatarInput.current?.click()}>
              <UserRoundIcon data-icon="inline-start" />
              更换头像
            </Button>
            {profile.avatar_url && (
              <Button variant="ghost" size="sm" disabled={busy} onClick={() => onPatch({ clear_avatar: true }, "头像已移除")}>
                移除头像
              </Button>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-3 text-sm">
        <span className="text-muted-foreground">个人强调色</span>
        <input
          type="color"
          aria-label="个人强调色"
          value={profile.accent_color || "#6366f1"}
          onChange={event => setProfile(current => ({ ...current, accent_color: event.target.value }))}
          className="size-8 cursor-pointer rounded-md border bg-transparent p-0.5"
        />
        <code className="font-mono text-xs text-muted-foreground">{profile.accent_color || "未设置"}</code>
        <Button
          variant="outline"
          size="xs"
          disabled={busy || (profile.accent_color ?? "") === (initialUser.accent_color ?? "")}
          onClick={() => onPatch({ accent_color: profile.accent_color ?? "" }, "强调色已保存")}
        >
          保存强调色
        </Button>
        {profile.accent_color && (
          <Button variant="ghost" size="xs" disabled={busy} onClick={() => onPatch({ accent_color: "" }, "强调色已清除")}>
            清除
          </Button>
        )}
      </CardContent>
      <input
        ref={avatarInput}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif"
        className="hidden"
        onChange={event => {
          onPickFile("avatar", event.target.files?.[0])
          event.target.value = ""
        }}
      />
      <input
        ref={bannerInput}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif"
        className="hidden"
        onChange={event => {
          onPickFile("banner", event.target.files?.[0])
          event.target.value = ""
        }}
      />
    </Card>
  )
}
