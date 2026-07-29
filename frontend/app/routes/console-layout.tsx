import { useEffect, useRef, useState } from "react"
import { Outlet, useLocation, useNavigate } from "react-router"

import { AppSidebar } from "~/components/app-sidebar"
import { SiteHeader } from "~/components/site-header"
import { SidebarInset, SidebarProvider } from "~/components/ui/sidebar"
import { useGatewayEvent } from "~/hooks/use-gateway"
import { api, getSession, logout, saveSession, type Guild, type User } from "~/lib/api"
import type { ConsoleContext } from "~/lib/console-context"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"
import { pageTitle } from "~/lib/nav"

export default function ConsoleLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const [user, setUser] = useState<User | null>(() => getSession()?.user ?? null)
  const [guilds, setGuilds] = useState<Guild[]>([])
  const [error, setError] = useState("")
  const contentRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!getSession()) {
      navigate("/login", { replace: true })
      return
    }
    Promise.all([api<User>("/auth/me"), api<Guild[]>("/guilds")])
      .then(([currentUser, currentGuilds]) => {
        setUser(currentUser)
        setGuilds(currentGuilds ?? [])
      })
      .catch(reason => {
        if (!getSession()) navigate("/login", { replace: true })
        else setError(reason instanceof Error ? reason.message : "后台加载失败")
      })
  }, [navigate])

  // 标签页标题：页面名 · NewtSpeak
  useEffect(() => {
    const page = pageTitle(location.pathname)
    document.title = page ? `${page} · NewtSpeak` : "NewtSpeak 管理控制台"
  }, [location.pathname])

  // 服务器结构实时同步：GUILD_UPDATE 事件载荷内嵌完整 guild 实体，本地就地合并；
  // GUILD_CREATE / GUILD_DELETE 重拉列表兜底（成员关系变化本地无法推导）。
  // guilds 经 ConsoleContext 下发全部页面，名称/图标/治理开关等基本信息随事件即时更新。
  useGatewayEvent("GUILD_UPDATE", payload => {
    const data = payload as { guild?: Guild; banners?: Guild["banners"] } | undefined
    const guild = data?.guild
    if (!guild?.id) return
    setGuilds(current =>
      current.map(item =>
        item.id === guild.id
          ? // banners 仅在 banner 增删/排序事件中携带（最新全量），有则整体替换
            { ...item, ...guild, banners: data?.banners ?? item.banners }
          : item
      )
    )
  })
  useGatewayEvent(["GUILD_CREATE", "GUILD_DELETE"], () => {
    void api<Guild[]>("/guilds")
      .then(next => setGuilds(next ?? []))
      .catch(() => undefined)
  })

  // 侧边栏切换页面时的内容区过渡（GSAP + 尊重 prefers-reduced-motion）
  useGSAP(
    () => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.fromTo(
          contentRef.current,
          { autoAlpha: 0, y: 10 },
          { autoAlpha: 1, y: 0, duration: MOTION.enter, ease: MOTION.ease, clearProps: "all" }
        )
      })
    },
    { dependencies: [location.pathname], scope: contentRef }
  )

  if (!user)
    return <div className="grid min-h-dvh place-items-center text-sm text-muted-foreground">正在加载后台…</div>

  async function signOut() {
    await logout()
    navigate("/login", { replace: true })
  }

  const context: ConsoleContext = {
    user,
    guilds,
    addGuild: guild => setGuilds(current => [guild, ...current]),
    refreshGuilds: async () => {
      const next = await api<Guild[]>("/guilds").catch(() => null)
      if (next) setGuilds(next)
    },
    updateUser: next => {
      setUser(next)
      const session = getSession()
      if (session) saveSession({ ...session, user: next })
    },
  }

  return (
    <SidebarProvider
      style={{ "--sidebar-width": "calc(var(--spacing) * 72)", "--header-height": "calc(var(--spacing) * 12)" } as React.CSSProperties}
    >
      <AppSidebar user={user} onLogout={signOut} variant="inset" />
      <SidebarInset>
        <SiteHeader title={pageTitle(location.pathname)} />
        {error && (
          <p role="alert" className="m-4 rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive lg:m-6">
            {error}
          </p>
        )}
        <div ref={contentRef} className="flex flex-1 flex-col">
          <Outlet context={context} />
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}
