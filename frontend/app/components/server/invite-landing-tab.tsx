import { useEffect, useState, type FormEvent } from "react"
import { GlobeIcon, LinkIcon, MegaphoneIcon, PlusIcon, Trash2Icon } from "lucide-react"
import { toast } from "sonner"

import { CopyButton } from "~/components/copy-button"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
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
import { Label } from "~/components/ui/label"
import { Switch } from "~/components/ui/switch"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  createInviteNotice,
  createInviteWithTTL,
  deleteInviteNotice,
  getInviteLanding,
  getInvitePortal,
  listInvites,
  putInviteLanding,
  putInvitePortal,
  revokeInvite,
  updateInviteNotice,
  type InviteLandingConfig,
  type InviteNotice,
  type InviteNoticeKind,
  type InvitePortalConfig,
  type SharedInvite,
} from "~/lib/api"

const KIND_LABELS: Record<InviteNoticeKind, string> = {
  ANNOUNCEMENT: "公告",
  NOTICE: "注意事项",
  AGREEMENT: "协议",
}

const KIND_OPTIONS = (Object.entries(KIND_LABELS) as [InviteNoticeKind, string][]).map(([value, label]) => ({
  value,
  label,
}))

/**
 * 邀请页标签：落地页配置（简介/开关/自动深链）、公告/注意事项/协议内容块管理、
 * 分享链接列表；系统管理员可编辑全局下载渠道与深链协议。
 */
export function InviteLandingTab({ guildID, isSystemAdmin }: { guildID: string; isSystemAdmin: boolean }) {
  const landing = useAsyncData(guildID ? () => getInviteLanding(guildID) : null, [guildID])
  const invites = useAsyncData<SharedInvite[]>(guildID ? () => listInvites(guildID) : null, [guildID])

  if (landing.status === "loading") return <LoadingState rows={5} />
  if (landing.status === "error") return <ErrorState message={landing.error} onRetry={() => landing.reload()} />

  return (
    <div className="flex flex-col gap-5">
      {landing.data && (
        <LandingConfigCard guildID={guildID} config={landing.data.config} onSaved={() => landing.reload(true)} />
      )}
      {landing.data && (
        <NoticesCard guildID={guildID} notices={landing.data.notices} reload={() => landing.reload(true)} />
      )}
      <InvitesCard
        guildID={guildID}
        invites={invites.data ?? []}
        status={invites.status}
        error={invites.error}
        reload={() => invites.reload(true)}
      />
      {isSystemAdmin && <PortalCard />}
    </div>
  )
}

function LandingConfigCard({
  guildID,
  config,
  onSaved,
}: {
  guildID: string
  config: InviteLandingConfig
  onSaved: () => void
}) {
  const [description, setDescription] = useState(config.description)
  const [enabled, setEnabled] = useState(config.enabled)
  const [autoDeepLink, setAutoDeepLink] = useState(config.auto_deep_link)
  const [saving, setSaving] = useState(false)
  const dirty = description !== config.description || enabled !== config.enabled || autoDeepLink !== config.auto_deep_link

  async function onSave() {
    setSaving(true)
    try {
      await putInviteLanding(guildID, { description, enabled, auto_deep_link: autoDeepLink })
      toast.success("落地页配置已保存")
      onSaved()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "配置保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <GlobeIcon className="size-4" />
          公开落地页
        </CardTitle>
        <CardDescription>
          未注册用户打开分享链接时看到的页面：服务器简介、公告/协议与客户端下载引导；已装客户端可深链直达并自动加入。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-2">
          <Label htmlFor="landing-description">服务器公开简介</Label>
          <textarea
            id="landing-description"
            value={description}
            onChange={event => setDescription(event.target.value)}
            rows={3}
            maxLength={4000}
            placeholder="向访客介绍这个服务器…（对未登录用户可见，注意内容脱敏）"
            className="w-full rounded-lg border bg-transparent px-3 py-2 text-sm transition-[border-color,box-shadow] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none"
          />
        </div>
        <div className="flex flex-wrap items-center gap-6">
          <label className="flex items-center gap-2.5 text-sm">
            <Switch checked={enabled} onCheckedChange={next => setEnabled(Boolean(next))} aria-label="启用落地页" />
            启用落地页
          </label>
          <label className="flex items-center gap-2.5 text-sm">
            <Switch
              checked={autoDeepLink}
              onCheckedChange={next => setAutoDeepLink(Boolean(next))}
              aria-label="自动唤起客户端"
            />
            打开页面时自动唤起客户端
          </label>
          <Button size="sm" onClick={onSave} disabled={!dirty || saving} className="ml-auto">
            {saving ? "保存中…" : "保存配置"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function NoticesCard({
  guildID,
  notices,
  reload,
}: {
  guildID: string
  notices: InviteNotice[]
  reload: () => void
}) {
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<InviteNotice | null>(null)
  const [kind, setKind] = useState<InviteNoticeKind>("ANNOUNCEMENT")

  useEffect(() => {
    if (editorOpen) setKind(editing?.kind ?? "ANNOUNCEMENT")
  }, [editorOpen, editing])

  async function onSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const body = {
      kind,
      title: String(form.get("notice-title") ?? "").trim(),
      body: String(form.get("notice-body") ?? ""),
      position: Number(form.get("notice-position") ?? 0),
      enabled: form.get("notice-enabled") === "on",
    }
    if (!body.title) return
    try {
      if (editing) {
        await updateInviteNotice(guildID, editing.id, body)
        toast.success("内容块已更新")
      } else {
        await createInviteNotice(guildID, body)
        toast.success("内容块已添加")
      }
      setEditorOpen(false)
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "内容块保存失败")
    }
  }

  async function onDelete(notice: InviteNotice) {
    if (!window.confirm(`确定删除「${notice.title}」？`)) return
    try {
      await deleteInviteNotice(guildID, notice.id)
      toast.success("内容块已删除")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "内容块删除失败")
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <MegaphoneIcon className="size-4" />
          公告 / 注意事项 / 协议
        </CardTitle>
        <CardDescription>支持同类多条；按「排序值 → 创建时间」在落地页分组展示，停用的内容块不对外显示。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex justify-end">
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setEditing(null)
              setEditorOpen(true)
            }}
          >
            <PlusIcon data-icon="inline-start" />
            添加内容块
          </Button>
        </div>
        {notices.length === 0 && (
          <EmptyState title="暂无内容块" description="添加公告、注意事项或协议，访客在落地页即可看到。" className="py-8" />
        )}
        {notices.map((notice, index) => (
          <div
            key={notice.id}
            style={{ "--stagger-index": index } as React.CSSProperties}
            className="anim-item flex items-start gap-3 rounded-xl border px-4 py-3"
          >
            <Badge variant={notice.kind === "AGREEMENT" ? "default" : "secondary"} className="mt-0.5 shrink-0">
              {KIND_LABELS[notice.kind]}
            </Badge>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">
                {notice.title}
                {!notice.enabled && <span className="ml-2 text-xs text-muted-foreground">（已停用）</span>}
              </p>
              {notice.body && <p className="mt-0.5 line-clamp-2 text-xs whitespace-pre-wrap text-muted-foreground">{notice.body}</p>}
            </div>
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">#{notice.position}</span>
            <Button
              variant="outline"
              size="xs"
              onClick={() => {
                setEditing(notice)
                setEditorOpen(true)
              }}
            >
              编辑
            </Button>
            <Button variant="ghost" size="icon-sm" aria-label="删除内容块" onClick={() => onDelete(notice)}>
              <Trash2Icon />
            </Button>
          </div>
        ))}
      </CardContent>

      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing ? "编辑内容块" : "添加内容块"}</DialogTitle>
            <DialogDescription>协议类内容建议放在最后，访客加入前需要了解的规则写在注意事项。</DialogDescription>
          </DialogHeader>
          <form key={editing?.id ?? "new"} onSubmit={onSave} className="grid gap-4">
            <div className="grid grid-cols-[8rem_1fr] gap-3">
              <div className="grid gap-2">
                <Label>类型</Label>
                <SimpleSelect
                  ariaLabel="内容块类型"
                  value={kind}
                  onChange={next => setKind(next as InviteNoticeKind)}
                  options={KIND_OPTIONS}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="notice-title">标题</Label>
                <Input id="notice-title" name="notice-title" defaultValue={editing?.title ?? ""} required maxLength={200} />
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="notice-body">正文</Label>
              <textarea
                id="notice-body"
                name="notice-body"
                defaultValue={editing?.body ?? ""}
                rows={6}
                maxLength={20000}
                className="w-full rounded-lg border bg-transparent px-3 py-2 text-sm transition-[border-color,box-shadow] focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none"
              />
            </div>
            <div className="flex items-center gap-6">
              <div className="grid gap-2">
                <Label htmlFor="notice-position">排序值（小的在前）</Label>
                <Input
                  id="notice-position"
                  name="notice-position"
                  type="number"
                  defaultValue={editing?.position ?? 0}
                  className="w-28 tabular-nums"
                />
              </div>
              <label className="mt-6 flex items-center gap-2.5 text-sm">
                <input type="checkbox" name="notice-enabled" defaultChecked={editing?.enabled ?? true} className="size-4" />
                对外展示
              </label>
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditorOpen(false)}>
                取消
              </Button>
              <Button type="submit">{editing ? "保存" : "添加"}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

const TTL_OPTIONS = [
  { value: "0", label: "永不过期" },
  { value: "3600", label: "1 小时" },
  { value: "86400", label: "1 天" },
  { value: "604800", label: "7 天" },
  { value: "2592000", label: "30 天" },
]

function InvitesCard({
  guildID,
  invites,
  status,
  error,
  reload,
}: {
  guildID: string
  invites: SharedInvite[]
  status: "idle" | "loading" | "success" | "error"
  error: string
  reload: () => void
}) {
  const [ttl, setTtl] = useState("0")
  const [maxUses, setMaxUses] = useState("0")
  const [creating, setCreating] = useState(false)

  async function onCreate() {
    setCreating(true)
    try {
      await createInviteWithTTL(guildID, Number(ttl) || undefined, Number(maxUses) || undefined)
      toast.success("邀请已创建")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "邀请创建失败")
    } finally {
      setCreating(false)
    }
  }

  async function onRevoke(invite: SharedInvite) {
    if (!window.confirm(`确定撤销邀请 ${invite.code}？分享链接将立即失效。`)) return
    try {
      await revokeInvite(guildID, invite.code)
      toast.success("邀请已撤销")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "邀请撤销失败")
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <LinkIcon className="size-4" />
          分享链接
        </CardTitle>
        <CardDescription>
          分享链接对未注册用户展示落地页；已装客户端用户点击即深链唤起并自动加入（同后端已有账号免注册）。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <SimpleSelect ariaLabel="有效期" value={ttl} onChange={setTtl} options={TTL_OPTIONS} className="w-36" />
          <SimpleSelect
            ariaLabel="最大使用次数"
            value={maxUses}
            onChange={setMaxUses}
            options={[
              { value: "0", label: "不限次数" },
              { value: "1", label: "1 次" },
              { value: "5", label: "5 次" },
              { value: "10", label: "10 次" },
              { value: "50", label: "50 次" },
              { value: "100", label: "100 次" },
            ]}
            className="w-32"
          />
          <Button size="sm" onClick={onCreate} disabled={creating}>
            <PlusIcon data-icon="inline-start" />
            {creating ? "创建中…" : "新建邀请"}
          </Button>
        </div>
        {status === "loading" && <LoadingState rows={3} />}
        {status === "error" && <ErrorState message={error} onRetry={reload} />}
        {status === "success" && invites.length === 0 && (
          <EmptyState title="暂无有效邀请" description="创建邀请后即可复制分享链接。" className="py-8" />
        )}
        {status === "success" &&
          invites.map((invite, index) => (
            <div
              key={invite.code}
              style={{ "--stagger-index": index } as React.CSSProperties}
              className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-2.5"
            >
              <code className="font-mono text-sm font-medium">{invite.code}</code>
              <span className="text-xs text-muted-foreground">
                {invite.expires_at ? `${new Date(invite.expires_at).toLocaleString()} 过期` : "永不过期"}
              </span>
              <span className="text-xs tabular-nums text-muted-foreground">
                已用 {invite.uses ?? 0}
                {invite.max_uses ? ` / ${invite.max_uses}` : " 次 · 不限"}
              </span>
              <div className="ml-auto flex items-center gap-1.5">
                <CopyButton text={invite.share_url} />
                <span className="max-w-64 truncate font-mono text-xs text-muted-foreground">{invite.share_url}</span>
                <Button variant="ghost" size="icon-sm" aria-label="撤销邀请" onClick={() => onRevoke(invite)}>
                  <Trash2Icon />
                </Button>
              </div>
            </div>
          ))}
      </CardContent>
    </Card>
  )
}

const PORTAL_FIELDS: { key: keyof InvitePortalConfig; label: string; placeholder?: string }[] = [
  { key: "app_name", label: "产品名", placeholder: "NewtSpeak" },
  { key: "deep_link_scheme", label: "深链协议（scheme）", placeholder: "newtspeak" },
  { key: "windows_url", label: "Windows 下载地址" },
  { key: "macos_url", label: "macOS 下载地址" },
  { key: "linux_url", label: "Linux 下载地址" },
  { key: "android_url", label: "Android 下载地址" },
  { key: "ios_url", label: "iOS 下载地址" },
  { key: "website_url", label: "官网地址" },
]

/** 全局门户配置（系统管理员）：下载渠道、深链协议，作用于全部服务器的落地页 */
function PortalCard() {
  const portal = useAsyncData<InvitePortalConfig>(() => getInvitePortal(), [])
  const [draft, setDraft] = useState<InvitePortalConfig | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (portal.data) setDraft(portal.data)
  }, [portal.data])

  async function onSave() {
    if (!draft) return
    setSaving(true)
    try {
      await putInvitePortal(draft)
      toast.success("门户配置已保存")
      portal.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "门户配置保存失败")
    } finally {
      setSaving(false)
    }
  }

  if (portal.status === "error") return <ErrorState message={portal.error} onRetry={() => portal.reload()} />

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">全局下载渠道（系统管理员）</CardTitle>
        <CardDescription>作用于全部服务器的落地页：各平台安装包地址与客户端深链协议。留空的平台不显示下载按钮。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-3 sm:grid-cols-2">
          {PORTAL_FIELDS.map(field => (
            <div key={field.key} className="grid gap-1.5">
              <Label htmlFor={`portal-${field.key}`}>{field.label}</Label>
              <Input
                id={`portal-${field.key}`}
                value={draft?.[field.key] ?? ""}
                placeholder={field.placeholder}
                onChange={event => setDraft(current => (current ? { ...current, [field.key]: event.target.value } : current))}
              />
            </div>
          ))}
        </div>
        <div className="flex justify-end">
          <Button size="sm" onClick={onSave} disabled={!draft || saving}>
            {saving ? "保存中…" : "保存门户配置"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
