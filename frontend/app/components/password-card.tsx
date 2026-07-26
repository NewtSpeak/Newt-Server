import { useState } from "react"
import { KeyRoundIcon } from "lucide-react"
import { toast } from "sonner"

import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Input } from "~/components/ui/input"
import { changeMyPassword } from "~/lib/api"

/** 修改密码：验证当前密码后更新，其他登录会话自动被吊销（settings 与 account 两页共用） */
export function PasswordCard() {
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  const [busy, setBusy] = useState(false)

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (next !== confirm) {
      toast.error("两次输入的新密码不一致")
      return
    }
    setBusy(true)
    try {
      await changeMyPassword(current, next)
      toast.success("密码已修改，其他登录会话已被吊销")
      setCurrent("")
      setNext("")
      setConfirm("")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "密码修改失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <KeyRoundIcon className="size-4" />
          修改密码
        </CardTitle>
        <CardDescription>修改成功后除当前会话外的所有登录（含用户端）将被强制下线。</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={onSubmit} className="grid gap-3">
          <Input
            type="password"
            aria-label="当前密码"
            placeholder="当前密码"
            autoComplete="current-password"
            value={current}
            onChange={event => setCurrent(event.target.value)}
            required
          />
          <Input
            type="password"
            aria-label="新密码"
            placeholder="新密码（≥8 位）"
            autoComplete="new-password"
            value={next}
            onChange={event => setNext(event.target.value)}
            minLength={8}
            maxLength={128}
            required
          />
          <Input
            type="password"
            aria-label="确认新密码"
            placeholder="确认新密码"
            autoComplete="new-password"
            value={confirm}
            onChange={event => setConfirm(event.target.value)}
            minLength={8}
            maxLength={128}
            required
          />
          <div className="flex justify-end">
            <Button type="submit" size="sm" disabled={busy || !current || next.length < 8}>
              {busy ? "提交中…" : "修改密码"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}
