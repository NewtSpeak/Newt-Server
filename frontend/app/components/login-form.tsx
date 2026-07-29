import { useEffect, useState, type FormEvent } from "react"
import { Link, useNavigate } from "react-router"

import { cn } from "~/lib/utils"
import { api, saveSession, type RegistrationStatus, type TokenResponse } from "~/lib/api"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "~/components/ui/field"
import { Input } from "~/components/ui/input"

export function LoginForm({ className, ...props }: React.ComponentProps<"div">) {
  const navigate = useNavigate()
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(false)
  const [registrationOpen, setRegistrationOpen] = useState(false)

  useEffect(() => {
    api<RegistrationStatus>("/auth/registration-status").then(status => setRegistrationOpen(status.registration_open)).catch(() => undefined)
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    setLoading(true)
    const form = new FormData(event.currentTarget)
    try {
      const session = await api<TokenResponse>("/auth/login", {
        method: "POST",
        body: JSON.stringify({ identifier: form.get("identifier"), password: form.get("password") }),
      })
      saveSession(session)
      navigate("/dashboard", { replace: true })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "登录失败，请重试")
    } finally {
      setLoading(false)
    }
  }

  return <div className={cn("flex flex-col gap-6", className)} {...props}>
    <Card>
      <CardHeader className="text-center"><CardTitle className="text-xl">欢迎回来</CardTitle><CardDescription>使用用户名或邮箱登录 NewtSpeak</CardDescription></CardHeader>
      <CardContent><form onSubmit={submit}><FieldGroup>
        <Field><FieldLabel htmlFor="identifier">用户名或邮箱</FieldLabel><Input id="identifier" name="identifier" autoComplete="username" placeholder="night-owl 或 owl@example.com" required /></Field>
        <Field><FieldLabel htmlFor="password">密码</FieldLabel><Input id="password" name="password" type="password" autoComplete="current-password" required /></Field>
        {error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>}
        <Field><Button type="submit" disabled={loading}>{loading ? "正在登录…" : "登录"}</Button>{registrationOpen && <FieldDescription className="text-center">首次部署？ <Link className="underline underline-offset-4" to="/signup">初始化系统管理员</Link></FieldDescription>}</Field>
      </FieldGroup></form></CardContent>
    </Card>
    <FieldDescription className="px-6 text-center">登录即表示你同意遵守 NewtSpeak 社区规则。</FieldDescription>
  </div>
}
