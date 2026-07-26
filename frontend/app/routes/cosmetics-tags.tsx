import { useEffect, useState } from "react"
import { PencilIcon, PlusIcon, TagsIcon, Trash2Icon } from "lucide-react"
import { toast } from "sonner"

import { CosmeticTagBadge } from "~/components/cosmetic-thumb"
import { PageHeader } from "~/components/page-header"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Button } from "~/components/ui/button"
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
import { useAsyncData } from "~/hooks/use-async-data"
import {
  createCosmeticTag,
  deleteCosmeticTag,
  listCosmeticTags,
  patchCosmeticTag,
  type CosmeticTag,
} from "~/lib/api"

/** 标签 key 规则（与后端 tagKeyPattern 对齐）：小写字母数字，连字符分隔 */
const TAG_KEY_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/
const HEX_COLOR = /^#[0-9a-fA-F]{6}$/

/** 装扮标签：最简 CRUD，供单品/捆绑包打标与列表筛选 */
export default function CosmeticTagsPage() {
  const page = useAsyncData(() => listCosmeticTags().then(raw => raw.tags), [])
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<CosmeticTag | null>(null)

  async function onDelete(tag: CosmeticTag) {
    if (!window.confirm(`确定删除标签「${tag.name}」？将级联移除所有单品与捆绑包上的该标签。`)) return
    try {
      await deleteCosmeticTag(tag.id)
      toast.success("标签已删除")
      page.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "删除失败")
    }
  }

  const tags = page.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="装扮标签"
        description="用于单品与捆绑包的运营打标（如限时、新品）；颜色用于商店与列表中的标签底色。"
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon data-icon="inline-start" />
            新建标签
          </Button>
        }
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        {page.status === "loading" && <LoadingState rows={4} />}
        {page.status === "error" && <ErrorState message={page.error} onRetry={() => page.reload()} />}
        {page.status === "success" && tags.length === 0 && (
          <EmptyState icon={TagsIcon} title="暂无标签" description="新建标签后即可在单品与捆绑包上使用。" />
        )}
        {page.status === "success" &&
          tags.map((tag, index) => (
            <div
              key={tag.id}
              style={{ "--stagger-index": index } as React.CSSProperties}
              className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-2.5"
            >
              <CosmeticTagBadge tag={tag} />
              <code className="font-mono text-xs text-muted-foreground">{tag.key}</code>
              {tag.color && <code className="font-mono text-xs text-muted-foreground">{tag.color}</code>}
              <div className="ml-auto flex items-center gap-1.5">
                <Button variant="outline" size="xs" onClick={() => setEditing(tag)}>
                  <PencilIcon data-icon="inline-start" />
                  编辑
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`删除标签 ${tag.name}`} onClick={() => onDelete(tag)}>
                  <Trash2Icon />
                </Button>
              </div>
            </div>
          ))}
      </section>

      <TagDialog
        open={createOpen || editing !== null}
        editing={editing}
        onClose={() => {
          setCreateOpen(false)
          setEditing(null)
        }}
        onSaved={() => page.reload(true)}
      />
    </main>
  )
}

function TagDialog({
  open,
  editing,
  onClose,
  onSaved,
}: {
  open: boolean
  editing: CosmeticTag | null
  onClose: () => void
  onSaved: () => void
}) {
  const [key, setKey] = useState("")
  const [name, setName] = useState("")
  const [color, setColor] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setKey(editing?.key ?? "")
    setName(editing?.name ?? "")
    setColor(editing?.color ?? "")
  }, [open, editing?.id])

  const keyValid = editing !== null || TAG_KEY_PATTERN.test(key)
  const colorValid = color === "" || HEX_COLOR.test(color)

  async function onSave() {
    setBusy(true)
    try {
      if (editing) {
        await patchCosmeticTag(editing.id, { name: name.trim(), color: color || undefined })
        toast.success("标签已更新")
      } else {
        await createCosmeticTag({ key: key.trim(), name: name.trim(), color: color || undefined })
        toast.success("标签已创建")
      }
      onClose()
      onSaved()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={next => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? `编辑标签 · ${editing.name}` : "新建标签"}</DialogTitle>
          <DialogDescription>key 须为小写字母数字与连字符（如 limited-time），创建后不可修改。</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="tag-key">key</Label>
            <Input
              id="tag-key"
              value={key}
              onChange={event => setKey(event.target.value.toLowerCase())}
              placeholder="如 limited-time"
              readOnly={editing !== null}
              aria-invalid={key !== "" && !keyValid ? true : undefined}
              className={editing ? "font-mono opacity-70" : "font-mono"}
              maxLength={64}
            />
            {key !== "" && !keyValid && <p className="text-xs text-destructive">须匹配 ^[a-z0-9]+(-[a-z0-9]+)*$</p>}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tag-name">名称</Label>
            <Input id="tag-name" value={name} onChange={event => setName(event.target.value)} placeholder="如 限时" maxLength={100} />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tag-color">颜色（可选）</Label>
            <div className="flex items-center gap-2">
              <input
                type="color"
                aria-label="标签颜色取色器"
                value={HEX_COLOR.test(color) ? color : "#888888"}
                onChange={event => setColor(event.target.value)}
                className="size-9 cursor-pointer rounded-lg border bg-transparent p-1"
              />
              <Input
                id="tag-color"
                value={color}
                onChange={event => setColor(event.target.value)}
                placeholder="#RRGGBB"
                aria-invalid={!colorValid ? true : undefined}
                className="w-32 font-mono"
                maxLength={7}
              />
              {color && (
                <Button variant="ghost" size="xs" onClick={() => setColor("")}>
                  清除
                </Button>
              )}
            </div>
            {!colorValid && <p className="text-xs text-destructive">颜色须为 #RRGGBB 格式</p>}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onSave} disabled={busy || !name.trim() || !keyValid || !colorValid}>
            {busy ? "保存中…" : editing ? "保存修改" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
