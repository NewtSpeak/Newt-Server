import {
  Links,
  Meta,
  Outlet,
  Scripts,
  ScrollRestoration,
  isRouteErrorResponse,
} from "react-router"
import { ThemeProvider } from "next-themes"

import type { Route } from "./+types/root"
import { Toaster } from "~/components/ui/sonner"
import "./app.css"

/** 管理后台图标：来自 Newt-assets/logo.png（links + 下方 head 双写，避免 SPA 壳漏掉） */
export const links: Route.LinksFunction = () => [
  { rel: "icon", href: "/favicon.ico?v=newt2", sizes: "any" },
  { rel: "icon", type: "image/png", href: "/favicon.png?v=newt2" },
  { rel: "apple-touch-icon", href: "/apple-touch-icon.png?v=newt2" },
]

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        {/* 显式写入，防止 SPA 预渲染不带 links、浏览器继续用 React Router 默认图标缓存 */}
        <link rel="icon" href="/favicon.ico?v=newt2" sizes="any" />
        <link rel="icon" type="image/png" href="/favicon.png?v=newt2" />
        <link rel="apple-touch-icon" href="/apple-touch-icon.png?v=newt2" />
        <Meta />
        <Links />
      </head>
      <body>
        <ThemeProvider attribute="class" defaultTheme="dark" disableTransitionOnChange>
          {children}
          <Toaster position="bottom-right" />
        </ThemeProvider>
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  )
}

export default function App() {
  return <Outlet />
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  let message = "页面出现问题"
  let details = "发生了意外错误，请稍后重试。"
  let stack: string | undefined

  if (isRouteErrorResponse(error)) {
    message = error.status === 404 ? "404" : "Error"
    details =
      error.status === 404
        ? "找不到请求的页面。"
        : error.statusText || details
  } else if (import.meta.env.DEV && error && error instanceof Error) {
    details = error.message
    stack = error.stack
  }

  return (
    <main className="container mx-auto p-4 pt-16">
      <h1>{message}</h1>
      <p>{details}</p>
      {stack && (
        <pre className="w-full overflow-x-auto p-4">
          <code>{stack}</code>
        </pre>
      )}
    </main>
  )
}
