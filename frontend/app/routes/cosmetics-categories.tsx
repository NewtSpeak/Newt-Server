import { useEffect, useState } from "react"
import { PencilIcon, PlusIcon, ShapesIcon } from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
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
import { Switch } from "~/components/ui/switch"
import { Textarea } from "~/components/ui/textarea"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  createCosmeticCategory,
  listCosmeticCategories,
  patchCosmeticCategory,
  type CosmeticCategory,
  type CosmeticCategorySchema,
} from "~/lib/api"
import { CATEGORY_SCHEMA_TEMPLATE, schemaAssetSlots } from "~/lib/cosmetics"

/** 装扮品类管理：schema 决定单品的资产槽与 payload 表单；无删除，用 enabled=false 停用 */
export default function CosmeticCategoriesPage() {
  const page = useAsyncData(() => listCosmeticCategories().then(raw => raw.categories), [])
  const [editing, setEditing] = useState<CosmeticCategory | null>(null)
  const [createOpen, setCreateOpen] = useState(false)

  async function onToggleEnabled(category: CosmeticCategory, enabled: boolean) {
    try {
      await patchCosmeticCategory(category.key, { enabled })
      toast.success(enabled ? `「${category.name}」已启用` : `「${category.name}」已停用`)
      page.reload(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "更新失败")
    }
  }

  const categories = page.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="装扮品类"
        description="品类 schema 定义单品的资产槽与渲染配置表单；品类不可删除，停用后用户端不再展示。"
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon data-icon="inline-start" />
            新建品类
          </Button>
        }
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        {page.status === "loading" && <LoadingState rows={4} />}
        {page.status === "error" && <ErrorState message={page.error} onRetry={() => page.reload()} />}
        {page.status === "success" && categories.length === 0 && (
          <EmptyState icon={ShapesIcon} title="暂无品类" description="新建品类后即可在单品页选用。" />
        )}
        {page.status === "success" &&
          categories.map((category, index) => {
            const slots = schemaAssetSlots(category.schema)
            return (
              <div
                key={category.key}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3"
              >
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-1.5 text-sm font-medium">
                    {category.name}
                    <code className="font-mono text-xs text-muted-foreground">{category.key}</code>
                    <Badge variant="outline">槽位 {category.slot}</Badge>
                    {!category.enabled && <Badge variant="secondary">已停用</Badge>}
                  </p>
                  <p className="truncate text-xs text-muted-foreground">
                    {slots.length > 0
                      ? `资产槽：${slots.map(slot => `${slot.label || slot.key}${slot.required ? "*" : ""}`).join("、")}`
                      : "无资产槽（纯配置型）"}
                    {category.description ? ` · ${category.description}` : ""}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Switch
                    checked={category.enabled}
                    onCheckedChange={next => onToggleEnabled(category, Boolean(next))}
                    aria-label={`启用 ${category.name}`}
                  />
                  <Button variant="outline" size="xs" onClick={() => setEditing(category)}>
                    <PencilIcon data-icon="inline-start" />
                    编辑
                  </Button>
                </div>
              </div>
            )
          })}
      </section>

      <CategoryDialog
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

function CategoryDialog({
  open,
  editing,
  onClose,
  onSaved,
}: {
  open: boolean
  editing: CosmeticCategory | null
  onClose: () => void
  onSaved: () => void
}) {
  const [key, setKey] = useState("")
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [slot, setSlot] = useState("")
  const [sortOrder, setSortOrder] = useState("100")
  const [schemaText, setSchemaText] = useState(CATEGORY_SCHEMA_TEMPLATE)
  const [schemaError, setSchemaError] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    if (editing) {
      setKey(editing.key)
      setName(editing.name)
      setDescription(editing.description)
      setSlot(editing.slot)
      setSortOrder(String(editing.sort_order))
      setSchemaText(JSON.stringify(editing.schema ?? {}, null, 2))
    } else {
      setKey("")
      setName("")
      setDescription("")
      setSlot("")
      setSortOrder("100")
      setSchemaText(CATEGORY_SCHEMA_TEMPLATE)
    }
    setSchemaError("")
  }, [open, editing?.key])

  /** 失焦 parse 校验 schema JSON */
  function validateSchema(): CosmeticCategorySchema | null {
    try {
      const parsed = JSON.parse(schemaText || "{}") as CosmeticCategorySchema
      setSchemaError("")
      return parsed
    } catch {
      setSchemaError("schema JSON 语法非法")
      return null
    }
  }

  async function onSave() {
    const schema = validateSchema()
    if (!schema) return
    setBusy(true)
    try {
      const body = {
        name: name.trim(),
        description: description.trim(),
        slot: slot.trim() || undefined,
        schema,
        sort_order: Number.isFinite(Number(sortOrder)) ? Math.floor(Number(sortOrder)) : undefined,
      }
      if (editing) {
        await patchCosmeticCategory(editing.key, body)
        toast.success("品类已更新")
      } else {
        await createCosmeticCategory({ ...body, key: key.trim() })
        toast.success("品类已创建")
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
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? `编辑品类 · ${editing.name}` : "新建品类"}</DialogTitle>
          <DialogDescription>
            schema 定义资产槽（asset_slots）与 payload 表单（payload_fields）；mime_groups 可选
            image / animated_image / video / audio。
          </DialogDescription>
        </DialogHeader>
        <div className="flex max-h-[65vh] flex-col gap-4 overflow-y-auto pr-1">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="flex flex-col gap-2">
              <Label htmlFor="cat-key">key（创建后不可修改）</Label>
              <Input
                id="cat-key"
                value={key}
                onChange={event => setKey(event.target.value)}
                placeholder="如 avatar_frame"
                readOnly={editing !== null}
                className={editing ? "font-mono opacity-70" : "font-mono"}
                maxLength={64}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="cat-name">名称</Label>
              <Input id="cat-name" value={name} onChange={event => setName(event.target.value)} maxLength={100} />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="cat-description">描述</Label>
            <Textarea
              id="cat-description"
              value={description}
              onChange={event => setDescription(event.target.value)}
              maxLength={2000}
            />
          </div>
          <div className="grid gap-4 md:grid-cols-2">
            <div className="flex flex-col gap-2">
              <Label htmlFor="cat-slot">装备槽位（缺省 = key）</Label>
              <Input
                id="cat-slot"
                value={slot}
                onChange={event => setSlot(event.target.value)}
                placeholder="同一槽位同时只能装备一件"
                className="font-mono"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="cat-sort">排序权重</Label>
              <Input id="cat-sort" type="number" step={1} value={sortOrder} onChange={event => setSortOrder(event.target.value)} />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="cat-schema">schema（JSON）</Label>
            <Textarea
              id="cat-schema"
              value={schemaText}
              onChange={event => setSchemaText(event.target.value)}
              onBlur={validateSchema}
              aria-invalid={schemaError ? true : undefined}
              className="min-h-52 font-mono text-xs"
              spellCheck={false}
            />
            {schemaError && <p className="text-xs text-destructive">{schemaError}</p>}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onSave} disabled={busy || !name.trim() || (!editing && !key.trim())}>
            {busy ? "保存中…" : editing ? "保存修改" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
