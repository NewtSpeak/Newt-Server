import { useState, type FormEvent } from "react"
import { Link, useNavigate } from "react-router"

import { cn } from "~/lib/utils"
import { api, saveSession, type TokenResponse } from "~/lib/api"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "~/components/ui/field"
import { Input } from "~/components/ui/input"

export function SignupForm({ className, ...props }: React.ComponentProps<"div">) {
  const navigate = useNavigate()
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const password = String(form.get("password") ?? "")
    if (password !== form.get("confirm-password")) { setError("两次输入的密码不一致"); return }
    setError(""); setLoading(true)
    try {
      const session = await api<TokenResponse>("/auth/register", { method: "POST", body: JSON.stringify({ username: form.get("username"), email: form.get("email"), password }) })
      saveSession(session)
      navigate("/dashboard", { replace: true })
    } catch (reason) { setError(reason instanceof Error ? reason.message : "注册失败，请重试") }
    finally { setLoading(false) }
  }

  return <div className={cn("flex flex-col gap-6", className)} {...props}>
    <Card><CardHeader className="text-center"><CardTitle className="text-xl">创建账号</CardTitle><CardDescription>注册后将自动登录并进入控制台</CardDescription></CardHeader>
      <CardContent><form onSubmit={submit}><FieldGroup>
        <Field><FieldLabel htmlFor="username">用户名</FieldLabel><Input id="username" name="username" autoComplete="username" placeholder="night-owl" minLength={2} maxLength={32} required /></Field>
        <Field><FieldLabel htmlFor="email">邮箱</FieldLabel><Input id="email" name="email" type="email" autoComplete="email" placeholder="owl@example.com" required /></Field>
        <Field className="grid sm:grid-cols-2 sm:gap-4"><Field><FieldLabel htmlFor="password">密码</FieldLabel><Input id="password" name="password" type="password" autoComplete="new-password" minLength={8} required /></Field><Field><FieldLabel htmlFor="confirm-password">确认密码</FieldLabel><Input id="confirm-password" name="confirm-password" type="password" autoComplete="new-password" minLength={8} required /></Field></Field>
        <FieldDescription>密码至少 8 位，最多 128 位。</FieldDescription>
        {error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>}
        <Field><Button type="submit" disabled={loading}>{loading ? "正在创建…" : "创建账号"}</Button><FieldDescription className="text-center">已有账号？ <Link className="underline underline-offset-4" to="/login">返回登录</Link></FieldDescription></Field>
      </FieldGroup></form></CardContent>
    </Card><FieldDescription className="px-6 text-center">密码将使用 Argon2id 加密保存。</FieldDescription>
  </div>
}
