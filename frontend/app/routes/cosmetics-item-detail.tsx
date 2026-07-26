import { useEffect, useMemo, useRef, useState } from "react"
import { Link, useParams } from "react-router"
import {
  ArchiveIcon,
  ArrowLeftIcon,
  Loader2Icon,
  MusicIcon,
  RocketIcon,
  UndoIcon,
  UploadIcon,
} from "lucide-react"
import { toast } from "sonner"

import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { Switch } from "~/components/ui/switch"
import { Textarea } from "~/components/ui/textarea"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  getCosmeticItem,
  listCosmeticCategories,
  listCosmeticTags,
  patchCosmeticItem,
  uploadCosmeticItemAsset,
  type CosmeticAssetSlotDef,
  type CosmeticItem,
  type CosmeticPayloadFieldDef,
  type CosmeticTag,
} from "~/lib/api"
import {
  COSMETIC_STATUS_META,
  acceptForSlot,
  formatBytes,
  isoToLocalInput,
  localInputToISO,
  maxBytesForSlot,
  mimesForSlot,
  MIME_GROUP_LABEL,
  schemaAssetSlots,
} from "~/lib/cosmetics"

/** 单品详情（核心页）：基本信息 / 资产槽 / payload 动态表单，均由品类 schema 驱动 */
export default function CosmeticItemDetailPage() {
  const { itemId = "" } = useParams()
  const initial = useAsyncData(itemId ? () => getCosmeticItem(itemId) : null, [itemId])
  const categories = useAsyncData(() => listCosmeticCategories().then(raw => raw.categories), [])
  const tags = useAsyncData(() => listCosmeticTags().then(raw => raw.tags), [])

  // 编辑期以本地 item 为准：PATCH / 上传响应即最新 itemView，直接回填
  const [item, setItem] = useState<CosmeticItem | null>(null)
  useEffect(() => {
    if (initial.data) setItem(initial.data)
  }, [initial.data])

  const category = useMemo(
    () => (categories.data ?? []).find(cat => cat.key === item?.category_key),
    [categories.data, item?.category_key]
  )

  if (initial.status === "loading" || (initial.status === "success" && !item)) {
    return (
      <main className="flex flex-1 flex-col gap-6 px-4 py-6 lg:px-6">
        <LoadingState rows={6} />
      </main>
    )
  }
  if (initial.status === "error" || !item) {
    return (
      <main className="flex flex-1 flex-col gap-6 px-4 py-6 lg:px-6">
        <ErrorState message={initial.error || "单品不存在"} onRetry={() => initial.reload()} />
      </main>
    )
  }

  const meta = COSMETIC_STATUS_META[item.status ?? ""] ?? COSMETIC_STATUS_META.draft

  async function transition(status: string, successMessage: string) {
    try {
      const next = await patchCosmeticItem(item!.id, { status })
      setItem(next)
      toast.success(successMessage)
    } catch (reason) {
      // 发布失败常见于 INCOMPLETE_ASSETS：后端中文 message 直接透出
      toast.error(reason instanceof Error ? reason.message : "状态更新失败")
    }
  }

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title={item.name || "未命名单品"}
        titleExtra={<Badge variant={meta.variant}>{meta.label}</Badge>}
        description={`品类：${category?.name ?? item.category_key} · ID ${item.id}`}
        actions={
          <>
            <Button variant="outline" size="sm" render={<Link to="/cosmetics/items" />}>
              <ArrowLeftIcon data-icon="inline-start" />
              返回列表
            </Button>
            {item.status === "draft" && (
              <Button size="sm" onClick={() => transition("published", "已发布上架")}>
                <RocketIcon data-icon="inline-start" />
                发布
              </Button>
            )}
            {item.status === "published" && (
              <>
                <Button variant="outline" size="sm" onClick={() => transition("draft", "已恢复为草稿")}>
                  <UndoIcon data-icon="inline-start" />
                  恢复草稿
                </Button>
                <Button variant="destructive" size="sm" onClick={() => transition("archived", "已归档下架")}>
                  <ArchiveIcon data-icon="inline-start" />
                  归档
                </Button>
              </>
            )}
            {item.status === "archived" && (
              <Button variant="outline" size="sm" onClick={() => transition("draft", "已恢复为草稿")}>
                <UndoIcon data-icon="inline-start" />
                恢复草稿
              </Button>
            )}
          </>
        }
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <BasicInfoCard item={item} tags={tags.data ?? []} onSaved={setItem} />
        <AssetsCard item={item} slots={schemaAssetSlots(category?.schema)} onUpdated={setItem} />
        <PayloadCard
          key={item.id}
          item={item}
          fields={category?.schema?.payload_fields ?? []}
          onSaved={setItem}
        />
      </section>
    </main>
  )
}

// ---------------------------------------------------------------------------
// ① 基本信息
// ---------------------------------------------------------------------------

function BasicInfoCard({
  item,
  tags,
  onSaved,
}: {
  item: CosmeticItem
  tags: CosmeticTag[]
  onSaved: (item: CosmeticItem) => void
}) {
  const [name, setName] = useState(item.name)
  const [description, setDescription] = useState(item.description)
  const [price, setPrice] = useState(String(item.price_points))
  const [sortOrder, setSortOrder] = useState(String(item.sort_order))
  const [tagIDs, setTagIDs] = useState<string[]>((item.tags ?? []).map(tag => tag.id))
  const [availFrom, setAvailFrom] = useState(isoToLocalInput(item.available_from))
  const [availUntil, setAvailUntil] = useState(isoToLocalInput(item.available_until))
  const [busy, setBusy] = useState(false)

  // item 外部更新（状态流转/上传响应）时同步只读派生字段，避免覆盖正在编辑的表单
  useEffect(() => {
    setName(item.name)
    setDescription(item.description)
    setPrice(String(item.price_points))
    setSortOrder(String(item.sort_order))
    setTagIDs((item.tags ?? []).map(tag => tag.id))
    setAvailFrom(isoToLocalInput(item.available_from))
    setAvailUntil(isoToLocalInput(item.available_until))
  }, [item.id])

  function toggleTag(tagID: string) {
    setTagIDs(current => (current.includes(tagID) ? current.filter(id => id !== tagID) : [...current, tagID]))
  }

  async function onSave() {
    const priceValue = Number(price)
    const sortValue = Number(sortOrder)
    if (!Number.isFinite(priceValue) || priceValue < 0) {
      toast.error("价格须为非负整数")
      return
    }
    setBusy(true)
    try {
      // 后端约束：PATCH 始终整体提交 name+description（空 name 会被忽略、description 依附 name）
      const next = await patchCosmeticItem(item.id, {
        name: name.trim(),
        description: description.trim(),
        price_points: Math.floor(priceValue),
        sort_order: Number.isFinite(sortValue) ? Math.floor(sortValue) : 0,
        tag_ids: tagIDs,
        available_from: localInputToISO(availFrom),
        available_until: localInputToISO(availUntil),
      })
      onSaved(next)
      toast.success("基本信息已保存")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">基本信息</CardTitle>
        <CardDescription>名称、定价、排序与售卖时间窗；时间窗一经设置只能改期、不能清空。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor="item-name">名称</Label>
            <Input id="item-name" value={name} onChange={event => setName(event.target.value)} maxLength={100} />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="item-price">价格（积分，0 = 免费）</Label>
            <Input
              id="item-price"
              type="number"
              min={0}
              step={1}
              value={price}
              onChange={event => setPrice(event.target.value)}
            />
          </div>
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="item-description">描述</Label>
          <Textarea
            id="item-description"
            value={description}
            onChange={event => setDescription(event.target.value)}
            placeholder="商店内展示的描述文案"
            maxLength={2000}
          />
        </div>
        <div className="grid gap-4 md:grid-cols-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="item-sort">排序权重（小者靠前）</Label>
            <Input
              id="item-sort"
              type="number"
              step={1}
              value={sortOrder}
              onChange={event => setSortOrder(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="item-avail-from">开售时间（可选）</Label>
            <Input
              id="item-avail-from"
              type="datetime-local"
              value={availFrom}
              onChange={event => setAvailFrom(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="item-avail-until">停售时间（可选）</Label>
            <Input
              id="item-avail-until"
              type="datetime-local"
              value={availUntil}
              onChange={event => setAvailUntil(event.target.value)}
            />
          </div>
        </div>
        <div className="flex flex-col gap-2">
          <Label>标签</Label>
          {tags.length === 0 ? (
            <p className="text-xs text-muted-foreground">暂无标签，可先到「装扮商店 → 标签」创建。</p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {tags.map(tag => {
                const active = tagIDs.includes(tag.id)
                return (
                  <button
                    key={tag.id}
                    type="button"
                    aria-pressed={active}
                    onClick={() => toggleTag(tag.id)}
                    className="rounded-3xl outline-none focus-visible:ring-3 focus-visible:ring-ring/30"
                  >
                    <Badge
                      variant={active ? "default" : "outline"}
                      style={active && tag.color ? { backgroundColor: tag.color, color: "#fff" } : undefined}
                    >
                      {tag.name}
                    </Badge>
                  </button>
                )
              })}
            </div>
          )}
        </div>
        <div className="flex justify-end">
          <Button onClick={onSave} disabled={busy || !name.trim()}>
            {busy ? "保存中…" : "保存基本信息"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// ② 资产槽（按品类 schema.asset_slots 动态渲染）
// ---------------------------------------------------------------------------

function AssetsCard({
  item,
  slots,
  onUpdated,
}: {
  item: CosmeticItem
  slots: CosmeticAssetSlotDef[]
  onUpdated: (item: CosmeticItem) => void
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">资产槽</CardTitle>
        <CardDescription>按品类 schema 定义的槽位上传媒体资产；带 * 的必填槽未上传时无法发布。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {slots.length === 0 && (
          <EmptyState title="该品类未定义资产槽" description="纯配置型品类无需上传资产。" className="py-8" />
        )}
        {slots.map(slot => (
          <AssetSlotUploader key={slot.key} item={item} slot={slot} onUpdated={onUpdated} />
        ))}
      </CardContent>
    </Card>
  )
}

function AssetSlotUploader({
  item,
  slot,
  onUpdated,
}: {
  item: CosmeticItem
  slot: CosmeticAssetSlotDef
  onUpdated: (item: CosmeticItem) => void
}) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const asset = item.assets?.[slot.key]
  const maxBytes = maxBytesForSlot(slot)
  const accept = acceptForSlot(slot)
  const groupText = (slot.mime_groups ?? []).map(group => MIME_GROUP_LABEL[group] ?? group).join(" / ")

  async function onPick(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = "" // 允许重复选择同一文件
    if (!file) return
    // 前端预校验：大小与 MIME（与后端 FILE_TOO_LARGE / MIME_NOT_ALLOWED 对齐）
    if (file.size > maxBytes) {
      toast.error(`文件过大：上限 ${formatBytes(maxBytes)}，当前 ${formatBytes(file.size)}`)
      return
    }
    const mimes = mimesForSlot(slot)
    if (mimes.length > 0 && !mimes.includes(file.type)) {
      toast.error(`该槽位不接受此格式（${file.type || "未知类型"}）`)
      return
    }
    setBusy(true)
    try {
      const next = await uploadCosmeticItemAsset(item.id, slot.key, file)
      onUpdated(next)
      toast.success(`「${slot.label || slot.key}」已上传`)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "上传失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3">
      <SlotPreview mime={asset?.mime} url={asset?.url} />
      <div className="min-w-0 flex-1">
        <p className="flex items-center gap-1 text-sm font-medium">
          {slot.label || slot.key}
          {slot.required && <span className="text-destructive">*</span>}
          <code className="ml-1 font-mono text-xs text-muted-foreground">{slot.key}</code>
        </p>
        <p className="text-xs text-muted-foreground">
          支持 {groupText || "任意格式"} · 上限 {formatBytes(maxBytes)}
          {asset && (
            <>
              {" "}
              · 已上传 {asset.mime}（{formatBytes(asset.size_bytes)}
              {asset.width > 0 ? `，${asset.width}×${asset.height}` : ""}）
            </>
          )}
        </p>
      </div>
      <input ref={inputRef} type="file" accept={accept || undefined} className="hidden" onChange={onPick} />
      <Button variant="outline" size="sm" disabled={busy} onClick={() => inputRef.current?.click()}>
        {busy ? <Loader2Icon data-icon="inline-start" className="animate-spin" /> : <UploadIcon data-icon="inline-start" />}
        {busy ? "上传中…" : asset ? "替换" : "上传"}
      </Button>
    </div>
  )
}

/** 槽位预览：按 mime 分型 img / video / audio */
function SlotPreview({ mime, url }: { mime?: string; url?: string }) {
  if (!url) {
    return (
      <div className="grid size-16 shrink-0 place-items-center rounded-lg border border-dashed bg-muted text-xs text-muted-foreground">
        未上传
      </div>
    )
  }
  if (mime?.startsWith("video/")) {
    return <video src={url} muted loop autoPlay playsInline className="size-16 shrink-0 rounded-lg border object-cover" />
  }
  if (mime?.startsWith("audio/")) {
    return (
      <div className="flex shrink-0 items-center gap-2">
        <div className="grid size-16 place-items-center rounded-lg border bg-muted text-muted-foreground">
          <MusicIcon className="size-5" />
        </div>
        <audio src={url} controls className="h-9 w-56" />
      </div>
    )
  }
  return <img src={url} alt="" className="size-16 shrink-0 rounded-lg border bg-muted object-cover" />
}

// ---------------------------------------------------------------------------
// ③ payload 动态表单（按品类 schema.payload_fields 渲染）
// ---------------------------------------------------------------------------

function PayloadCard({
  item,
  fields,
  onSaved,
}: {
  item: CosmeticItem
  fields: CosmeticPayloadFieldDef[]
  onSaved: (item: CosmeticItem) => void
}) {
  // 以 schema default 打底，item.payload 覆盖
  const [values, setValues] = useState<Record<string, unknown>>(() => buildInitialValues(fields, item.payload))
  // object 类型：textarea 原文 + 实时 parse 校验（非法禁保存）
  const [objTexts, setObjTexts] = useState<Record<string, string>>(() => buildObjectTexts(fields, item.payload))
  const [objErrors, setObjErrors] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState(false)

  const hasObjError = Object.values(objErrors).some(Boolean)

  function setValue(key: string, value: unknown) {
    setValues(current => ({ ...current, [key]: value }))
  }

  function onObjectChange(key: string, text: string) {
    setObjTexts(current => ({ ...current, [key]: text }))
    if (!text.trim()) {
      setObjErrors(current => ({ ...current, [key]: false }))
      setValues(current => {
        const next = { ...current }
        delete next[key]
        return next
      })
      return
    }
    try {
      const parsed = JSON.parse(text) as unknown
      setObjErrors(current => ({ ...current, [key]: false }))
      setValue(key, parsed)
    } catch {
      setObjErrors(current => ({ ...current, [key]: true }))
    }
  }

  async function onSave() {
    setBusy(true)
    try {
      // 保留 schema 未声明的历史键，避免保存时丢数据
      const payload: Record<string, unknown> = { ...item.payload }
      for (const field of fields) {
        if (values[field.key] !== undefined) payload[field.key] = values[field.key]
        else delete payload[field.key]
      }
      const next = await patchCosmeticItem(item.id, { payload })
      onSaved(next)
      toast.success("payload 已保存")
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "保存失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">渲染配置（payload）</CardTitle>
        <CardDescription>按品类 schema 的 payload_fields 动态生成表单；默认值来自 schema，保存整体覆盖。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {fields.length === 0 && (
          <EmptyState title="该品类未定义 payload 字段" description="无需额外渲染配置。" className="py-8" />
        )}
        {fields.map(field => (
          <PayloadField
            key={field.key}
            field={field}
            value={values[field.key]}
            objText={objTexts[field.key] ?? ""}
            objError={Boolean(objErrors[field.key])}
            onChange={value => setValue(field.key, value)}
            onObjectChange={text => onObjectChange(field.key, text)}
          />
        ))}
        {fields.length > 0 && (
          <div className="flex items-center justify-end gap-3">
            {hasObjError && <p className="text-xs text-destructive">存在非法 JSON 字段，请修正后保存</p>}
            <Button onClick={onSave} disabled={busy || hasObjError}>
              {busy ? "保存中…" : "保存配置"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function buildInitialValues(fields: CosmeticPayloadFieldDef[], payload: Record<string, unknown>) {
  const values: Record<string, unknown> = {}
  for (const field of fields) {
    if (field.default !== undefined) values[field.key] = field.default
    if (payload && payload[field.key] !== undefined) values[field.key] = payload[field.key]
  }
  return values
}

function buildObjectTexts(fields: CosmeticPayloadFieldDef[], payload: Record<string, unknown>) {
  const texts: Record<string, string> = {}
  for (const field of fields) {
    if (field.type !== "object") continue
    const value = payload?.[field.key] ?? field.default
    texts[field.key] = value === undefined ? "" : JSON.stringify(value, null, 2)
  }
  return texts
}

const HEX_COLOR = /^#[0-9a-fA-F]{6}$/

function PayloadField({
  field,
  value,
  objText,
  objError,
  onChange,
  onObjectChange,
}: {
  field: CosmeticPayloadFieldDef
  value: unknown
  objText: string
  objError: boolean
  onChange: (value: unknown) => void
  onObjectChange: (text: string) => void
}) {
  const id = `payload-${field.key}`
  const label = (
    <Label htmlFor={id} className="gap-1.5">
      {field.key}
      <span className="font-normal text-muted-foreground">（{field.type}）</span>
    </Label>
  )

  switch (field.type) {
    case "enum":
      return (
        <div className="flex flex-col gap-2">
          {label}
          <SimpleSelect
            ariaLabel={field.key}
            value={value === undefined ? null : String(value)}
            onChange={onChange}
            options={(field.values ?? []).map(option => ({ value: option, label: option }))}
            className="w-56"
          />
        </div>
      )
    case "bool":
      return (
        <div className="flex items-center gap-3">
          <Switch id={id} checked={Boolean(value)} onCheckedChange={next => onChange(Boolean(next))} />
          {label}
        </div>
      )
    case "number":
      return (
        <div className="flex flex-col gap-2">
          {label}
          <Input
            id={id}
            type="number"
            step="any"
            className="w-56"
            value={value === undefined || value === null ? "" : String(value)}
            onChange={event => {
              const raw = event.target.value
              if (raw === "") onChange(undefined)
              else {
                const parsed = Number(raw)
                onChange(Number.isFinite(parsed) ? parsed : raw)
              }
            }}
          />
        </div>
      )
    case "color": {
      const text = typeof value === "string" ? value : ""
      return (
        <div className="flex flex-col gap-2">
          {label}
          <div className="flex items-center gap-2">
            <input
              type="color"
              aria-label={`${field.key} 取色器`}
              value={HEX_COLOR.test(text) ? text : "#000000"}
              onChange={event => onChange(event.target.value)}
              className="size-9 cursor-pointer rounded-lg border bg-transparent p-1"
            />
            <Input
              id={id}
              value={text}
              onChange={event => onChange(event.target.value)}
              placeholder="#RRGGBB"
              className="w-32 font-mono"
              maxLength={7}
            />
          </div>
        </div>
      )
    }
    case "object":
      return (
        <div className="flex flex-col gap-2">
          {label}
          <Textarea
            id={id}
            value={objText}
            onChange={event => onObjectChange(event.target.value)}
            placeholder='JSON 对象，如 {"from": "#ff0000", "to": "#0000ff"}'
            aria-invalid={objError || undefined}
            className="min-h-28 font-mono text-xs"
            spellCheck={false}
          />
          {objError && <p className="text-xs text-destructive">JSON 语法非法</p>}
        </div>
      )
    default:
      // string 及未知类型回退为文本输入
      return (
        <div className="flex flex-col gap-2">
          {label}
          <Input
            id={id}
            value={typeof value === "string" ? value : value === undefined ? "" : String(value)}
            onChange={event => onChange(event.target.value)}
          />
        </div>
      )
  }
}
