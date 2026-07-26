import { useRef, useState } from "react"
import { KeyRoundIcon, PlusIcon, RocketIcon, ServerIcon, Trash2Icon } from "lucide-react"
import { toast } from "sonner"

import { EmptyState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "~/components/ui/field"
import { Input } from "~/components/ui/input"
import { Textarea } from "~/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "~/components/ui/toggle-group"
import { Tooltip, TooltipContent, TooltipTrigger } from "~/components/ui/tooltip"
import { createSfuDeployServer, deleteSfuDeployServer, type SfuDeployServer } from "~/lib/api"
import { gsap, MOTION, MOTION_OK } from "~/lib/gsap"
import { formatRelative } from "~/lib/format"
import { cn } from "~/lib/utils"

type Props = {
  servers: SfuDeployServer[]
  status: "idle" | "loading" | "success" | "error"
  onChanged: () => void
  onDeployTo: (server: SfuDeployServer) => void
}

export function DeployServers({ servers, status, onChanged, onDeployTo }: Props) {
  const [addOpen, setAddOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<SfuDeployServer | null>(null)
  const [busy, setBusy] = useState(false)
  const listRef = useRef<HTMLDivElement>(null)

  // 新增表单
  const [name, setName] = useState("")
  const [host, setHost] = useState("")
  const [port, setPort] = useState("22")
  const [username, setUsername] = useState("root")
  const [authMethod, setAuthMethod] = useState<"password" | "private_key">("password")
  const [password, setPassword] = useState("")
  const [privateKey, setPrivateKey] = useState("")
  const [passphrase, setPassphrase] = useState("")
  const [sudoPassword, setSudoPassword] = useState("")

  function resetForm() {
    setName("")
    setHost("")
    setPort("22")
    setUsername("root")
    setAuthMethod("password")
    setPassword("")
    setPrivateKey("")
    setPassphrase("")
    setSudoPassword("")
  }

  async function onCreate() {
    if (!host.trim()) {
      toast.error("请填写服务器 IP 或域名")
      return
    }
    if (authMethod === "password" && !password) {
      toast.error("请填写 SSH 密码")
      return
    }
    if (authMethod === "private_key" && !privateKey.trim()) {
      toast.error("请粘贴 SSH 私钥")
      return
    }
    setBusy(true)
    try {
      await createSfuDeployServer({
        host: host.trim(),
        port: Number(port) || 22,
        username: username.trim() || "root",
        auth_method: authMethod,
        password: authMethod === "password" ? password : undefined,
        private_key: authMethod === "private_key" ? privateKey : undefined,
        passphrase: passphrase || undefined,
        sudo_password: sudoPassword || undefined,
        save_as: name.trim() || host.trim(),
      })
      toast.success("服务器已保存")
      resetForm()
      setAddOpen(false)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setBusy(false)
    }
  }

  async function onDelete(server: SfuDeployServer) {
    setBusy(true)
    try {
      // 先播退出动画再刷新列表，避免卡片瞬间消失
      const card = listRef.current?.querySelector<HTMLElement>(`[data-server="${server.id}"]`)
      if (card) {
        const media = gsap.matchMedia()
        media.add(MOTION_OK, () => {
          gsap.to(card, { autoAlpha: 0, y: -8, height: 0, duration: MOTION.exit, ease: MOTION.easeIn })
        })
      }
      await deleteSfuDeployServer(server.id)
      toast.success(`已删除「${server.name}」及其加密凭据`)
      setPendingDelete(null)
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "删除失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-2xl border bg-card p-4 shadow-xs">
      <div className="mb-3 flex items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">已保存的服务器</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">SSH 凭据加密存储，可直接复用发起新部署。</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setAddOpen(true)}>
          <PlusIcon data-icon="inline-start" />
          添加
        </Button>
      </div>

      {status === "loading" && servers.length === 0 ? (
        <LoadingState rows={2} />
      ) : servers.length === 0 ? (
        <EmptyState
          icon={ServerIcon}
          title="还没有保存的服务器"
          description="发起部署时勾选「保存这台服务器」，或在此手动添加。"
        />
      ) : (
        <div ref={listRef} className="grid gap-2">
          {servers.map((server, index) => (
            <article
              key={server.id}
              data-server={server.id}
              style={{ "--stagger-index": index } as React.CSSProperties}
              className="anim-item flex items-center gap-3 rounded-xl border p-3 transition-[border-color] hover:border-primary/30"
            >
              <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
                {server.auth_method === "private_key" ? (
                  <KeyRoundIcon className="size-4" />
                ) : (
                  <ServerIcon className="size-4" />
                )}
              </span>

              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{server.name}</p>
                <p className="truncate font-mono text-xs text-muted-foreground">
                  {server.username}@{server.host}:{server.port}
                </p>
              </div>

              {server.host_key_fingerprint && (
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <span className="hidden shrink-0 font-mono text-[11px] text-muted-foreground/70 lg:block">
                        {server.host_key_fingerprint.slice(0, 18)}…
                      </span>
                    }
                  />
                  <TooltipContent>
                    <p className="font-mono text-xs">{server.host_key_fingerprint}</p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      已记录的主机指纹 · 添加于 {formatRelative(server.created_at)}
                    </p>
                  </TooltipContent>
                </Tooltip>
              )}

              <div className="flex shrink-0 items-center gap-1">
                <Button variant="ghost" size="sm" onClick={() => onDeployTo(server)}>
                  <RocketIcon data-icon="inline-start" />
                  部署
                </Button>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`删除 ${server.name}`}
                  onClick={() => setPendingDelete(server)}
                  className="text-muted-foreground hover:text-destructive"
                >
                  <Trash2Icon />
                </Button>
              </div>
            </article>
          ))}
        </div>
      )}

      {/* 新增服务器 */}
      <Dialog
        open={addOpen}
        onOpenChange={next => {
          setAddOpen(next)
          if (!next) resetForm()
        }}
      >
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>添加服务器</DialogTitle>
            <DialogDescription>凭据会加密保存在本 Server，用于后续一键部署。</DialogDescription>
          </DialogHeader>

          <div className="grid max-h-[55vh] gap-4 overflow-auto pr-1">
            <Field>
              <FieldLabel htmlFor="srv-name">备注名</FieldLabel>
              <Input
                id="srv-name"
                value={name}
                onChange={e => setName(e.target.value)}
                placeholder="留空则用 IP"
                maxLength={100}
              />
            </Field>
            <div className="grid gap-4 sm:grid-cols-[1fr_110px]">
              <Field>
                <FieldLabel htmlFor="srv-host">服务器 IP 或域名</FieldLabel>
                <Input id="srv-host" value={host} onChange={e => setHost(e.target.value)} placeholder="203.0.113.10" />
              </Field>
              <Field>
                <FieldLabel htmlFor="srv-port">SSH 端口</FieldLabel>
                <Input id="srv-port" value={port} onChange={e => setPort(e.target.value)} inputMode="numeric" />
              </Field>
            </div>
            <Field>
              <FieldLabel htmlFor="srv-user">登录用户名</FieldLabel>
              <Input id="srv-user" value={username} onChange={e => setUsername(e.target.value)} autoComplete="off" />
            </Field>
            <Field>
              <FieldLabel>认证方式</FieldLabel>
              <ToggleGroup
                value={[authMethod]}
                onValueChange={v => setAuthMethod((v[0] as "password" | "private_key") ?? "password")}
                spacing={0}
                variant="outline"
                className="w-fit"
              >
                <ToggleGroupItem value="password" variant="outline">
                  密码
                </ToggleGroupItem>
                <ToggleGroupItem value="private_key" variant="outline">
                  SSH 私钥
                </ToggleGroupItem>
              </ToggleGroup>
            </Field>
            {authMethod === "password" ? (
              <Field>
                <FieldLabel htmlFor="srv-password">SSH 密码</FieldLabel>
                <Input
                  id="srv-password"
                  type="password"
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                  autoComplete="off"
                />
              </Field>
            ) : (
              <>
                <Field>
                  <FieldLabel htmlFor="srv-key">SSH 私钥（PEM）</FieldLabel>
                  <Textarea
                    id="srv-key"
                    value={privateKey}
                    onChange={e => setPrivateKey(e.target.value)}
                    rows={5}
                    spellCheck={false}
                    className="font-mono text-xs"
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="srv-passphrase">私钥口令（可选）</FieldLabel>
                  <Input
                    id="srv-passphrase"
                    type="password"
                    value={passphrase}
                    onChange={e => setPassphrase(e.target.value)}
                    autoComplete="off"
                  />
                </Field>
              </>
            )}
            {username.trim() !== "root" && (
              <Field>
                <FieldLabel htmlFor="srv-sudo">sudo 密码（可选）</FieldLabel>
                <Input
                  id="srv-sudo"
                  type="password"
                  value={sudoPassword}
                  onChange={e => setSudoPassword(e.target.value)}
                  placeholder={authMethod === "password" ? "留空则复用登录密码" : "免密 sudo 可留空"}
                  autoComplete="off"
                />
                <FieldDescription>非 root 用户安装 systemd 服务需要 sudo 权限。</FieldDescription>
              </Field>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)} disabled={busy}>
              取消
            </Button>
            <Button onClick={() => void onCreate()} disabled={busy}>
              {busy ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认 */}
      <Dialog open={Boolean(pendingDelete)} onOpenChange={next => !next && setPendingDelete(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>删除「{pendingDelete?.name}」？</DialogTitle>
            <DialogDescription>
              该服务器的加密凭据会一并销毁，之后需要重新录入才能部署。已经部署好的节点不受影响。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPendingDelete(null)} disabled={busy}>
              取消
            </Button>
            <Button
              variant="destructive"
              onClick={() => pendingDelete && void onDelete(pendingDelete)}
              disabled={busy}
            >
              删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
