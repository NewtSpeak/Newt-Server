import { useEffect, useMemo, useState } from "react"
import { PackageIcon, PencilIcon, PlusIcon, SearchIcon } from "lucide-react"
import { toast } from "sonner"

import { CosmeticTagBadge, CosmeticThumb } from "~/components/cosmetic-thumb"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Checkbox } from "~/components/ui/checkbox"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "~/components/ui/sheet"
import { Textarea } from "~/components/ui/textarea"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  createCosmeticBundle,
  getCosmeticBundle,
  listCosmeticBundles,
  listCosmeticCategories,
  listCosmeticItems,
  listCosmeticTags,
  patchCosmeticBundle,
  type CosmeticBundle,
  type CosmeticCategory,
  type CosmeticItem,
  type CosmeticTag,
} from "~/lib/api"
import {
  COSMETIC_STATUS_META,
  COSMETIC_STATUS_OPTIONS,
  formatPrice,
  isoToLocalInput,
  localInputToISO,
} from "~/lib/cosmetics"

/** 装扮捆绑包：多件单品打包售卖；成员在 Sheet 抽屉中全量勾选提交 */
export default function CosmeticBundlesPage() {
  const bundles = useAsyncData(() => listCosmeticBundles().then(raw => raw.bundles), [])
  const categories = useAsyncData(() => listCosmeticCategories().then(raw => raw.categories), [])
  const tags = useAsyncData(() => listCosmeticTags().then(raw => raw.tags), [])
  // 全量单品（≤500）供成员选择器搜索/过滤
  const allItems = useAsyncData(() => listCosmeticItems().then(raw => raw.items), [])

  const [sheetOpen, setSheetOpen] = useState(false)
  const [editingID, setEditingID] = useState<string | null>(null)

  const rows = bundles.data ?? []

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="捆绑包"
        description="将多件单品打包为一个售卖单元；成员单品需各自处于上架状态才对用户端可见。"
        actions={
          <Button
            size="sm"
            onClick={() => {
              setEditingID(null)
              setSheetOpen(true)
            }}
          >
            <PlusIcon data-icon="inline-start" />
            新建捆绑包
          </Button>
        }
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        {bundles.status === "loading" && <LoadingState rows={4} />}
        {bundles.status === "error" && <ErrorState message={bundles.error} onRetry={() => bundles.reload()} />}
        {bundles.status === "success" && rows.length === 0 && (
          <EmptyState icon={PackageIcon} title="暂无捆绑包" description="新建捆绑包并挑选成员单品。" />
        )}
        {bundles.status === "success" &&
          rows.map((bundle, index) => {
            const meta = COSMETIC_STATUS_META[bundle.status ?? ""] ?? COSMETIC_STATUS_META.draft
            return (
              <div
                key={bundle.id}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3"
              >
                {bundle.preview_url ? (
                  <img src={bundle.preview_url} alt="" className="size-12 shrink-0 rounded-lg border bg-muted object-cover" />
                ) : (
                  <div className="grid size-12 shrink-0 place-items-center rounded-lg border bg-muted text-muted-foreground">
                    <PackageIcon className="size-4" />
                  </div>
                )}
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-1.5 text-sm font-medium">
                    <span className="truncate">{bundle.name}</span>
                    <Badge variant={meta.variant}>{meta.label}</Badge>
                    <Badge variant="outline">{(bundle.item_ids ?? []).length} 件单品</Badge>
                    {(bundle.tags ?? []).map(tag => (
                      <CosmeticTagBadge key={tag.id} tag={tag} />
                    ))}
                  </p>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {bundle.id}
                    {bundle.description ? ` · ${bundle.description}` : ""}
                  </p>
                </div>
                <span className="text-sm tabular-nums text-muted-foreground">{formatPrice(bundle.price_points)}</span>
                <Button
                  variant="outline"
                  size="xs"
                  onClick={() => {
                    setEditingID(bundle.id)
                    setSheetOpen(true)
                  }}
                >
                  <PencilIcon data-icon="inline-start" />
                  编辑
                </Button>
              </div>
            )
          })}
      </section>

      <BundleSheet
        open={sheetOpen}
        bundleID={editingID}
        onClose={() => setSheetOpen(false)}
        onSaved={() => bundles.reload(true)}
        allItems={allItems.data ?? []}
        categories={categories.data ?? []}
        tags={tags.data ?? []}
      />
    </main>
  )
}

function BundleSheet({
  open,
  bundleID,
  onClose,
  onSaved,
  allItems,
  categories,
  tags,
}: {
  open: boolean
  bundleID: string | null
  onClose: () => void
  onSaved: () => void
  allItems: CosmeticItem[]
  categories: CosmeticCategory[]
  tags: CosmeticTag[]
}) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [price, setPrice] = useState("0")
  const [sortOrder, setSortOrder] = useState("0")
  const [status, setStatus] = useState("draft")
  const [availFrom, setAvailFrom] = useState("")
  const [availUntil, setAvailUntil] = useState("")
  const [tagIDs, setTagIDs] = useState<string[]>([])
  const [itemIDs, setItemIDs] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  // 成员选择器过滤
  const [memberSearch, setMemberSearch] = useState("")
  const [memberCategory, setMemberCategory] = useState("all")

  // 编辑时拉取详情回填（含 item_ids）
  useEffect(() => {
    if (!open) return
    setMemberSearch("")
    setMemberCategory("all")
    if (!bundleID) {
      setName("")
      setDescription("")
      setPrice("0")
      setSortOrder("0")
      setStatus("draft")
      setAvailFrom("")
      setAvailUntil("")
      setTagIDs([])
      setItemIDs([])
      return
    }
    setLoading(true)
    getCosmeticBundle(bundleID)
      .then((bundle: CosmeticBundle) => {
        setName(bundle.name)
        setDescription(bundle.description)
        setPrice(String(bundle.price_points))
        setSortOrder(String(bundle.sort_order))
        setStatus(typeof bundle.status === "string" && bundle.status ? bundle.status : "draft")
        setAvailFrom(isoToLocalInput(bundle.available_from))
        setAvailUntil(isoToLocalInput(bundle.available_until))
        setTagIDs((bundle.tags ?? []).map(tag => tag.id))
        setItemIDs(bundle.item_ids ?? [])
      })
      .catch(reason => toast.error(reason instanceof Error ? reason.message : "加载捆绑包失败"))
      .finally(() => setLoading(false))
  }, [open, bundleID])

  const categoryName = useMemo(() => {
    const map = new Map<string, string>()
    for (const cat of categories) map.set(cat.key, cat.name)
    return map
  }, [categories])

  const filteredItems = useMemo(() => {
    const query = memberSearch.trim().toLowerCase()
    return allItems.filter(item => {
      if (memberCategory !== "all" && item.category_key !== memberCategory) return false
      if (query && !item.name.toLowerCase().includes(query) && !item.id.includes(query)) return false
      return true
    })
  }, [allItems, memberSearch, memberCategory])

  function toggleItem(id: string) {
    setItemIDs(current => (current.includes(id) ? current.filter(x => x !== id) : [...current, id]))
  }

  function toggleTag(id: string) {
    setTagIDs(current => (current.includes(id) ? current.filter(x => x !== id) : [...current, id]))
  }

  async function onSave() {
    const priceValue = Number(price)
    if (!Number.isFinite(priceValue) || priceValue < 0) {
      toast.error("价格须为非负整数")
      return
    }
    setBusy(true)
    try {
      // 后端约束：PATCH 始终整体提交 name+description；item_ids/tag_ids 全量覆盖
      const body = {
        name: name.trim(),
        description: description.trim(),
        price_points: Math.floor(priceValue),
        sort_order: Number.isFinite(Number(sortOrder)) ? Math.floor(Number(sortOrder)) : 0,
        status,
        item_ids: itemIDs,
        tag_ids: tagIDs,
        available_from: localInputToISO(availFrom),
        available_until: localInputToISO(availUntil),
      }
      if (bundleID) {
        await patchCosmeticBundle(bundleID, body)
        toast.success("捆绑包已保存")
      } else {
        await createCosmeticBundle(body)
        toast.success("捆绑包已创建")
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
    <Sheet open={open} onOpenChange={next => !next && onClose()}>
      <SheetContent className="data-[side=right]:sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{bundleID ? "编辑捆绑包" : "新建捆绑包"}</SheetTitle>
          <SheetDescription>
            成员单品全量勾选提交；时间窗一经设置只能改期、不能清空。
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-1 flex-col gap-4 overflow-y-auto px-6">
          {loading && <LoadingState rows={4} />}
          {!loading && (
            <>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="flex flex-col gap-2">
                  <Label htmlFor="bundle-name">名称</Label>
                  <Input id="bundle-name" value={name} onChange={event => setName(event.target.value)} maxLength={100} />
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="bundle-price">价格（积分，0 = 免费）</Label>
                  <Input
                    id="bundle-price"
                    type="number"
                    min={0}
                    step={1}
                    value={price}
                    onChange={event => setPrice(event.target.value)}
                  />
                </div>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="bundle-description">描述</Label>
                <Textarea
                  id="bundle-description"
                  value={description}
                  onChange={event => setDescription(event.target.value)}
                  maxLength={2000}
                />
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="flex flex-col gap-2">
                  <Label>状态</Label>
                  <SimpleSelect ariaLabel="状态" value={status} onChange={setStatus} options={COSMETIC_STATUS_OPTIONS} />
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="bundle-sort">排序权重</Label>
                  <Input
                    id="bundle-sort"
                    type="number"
                    step={1}
                    value={sortOrder}
                    onChange={event => setSortOrder(event.target.value)}
                  />
                </div>
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="flex flex-col gap-2">
                  <Label htmlFor="bundle-avail-from">开售时间（可选）</Label>
                  <Input
                    id="bundle-avail-from"
                    type="datetime-local"
                    value={availFrom}
                    onChange={event => setAvailFrom(event.target.value)}
                  />
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="bundle-avail-until">停售时间（可选）</Label>
                  <Input
                    id="bundle-avail-until"
                    type="datetime-local"
                    value={availUntil}
                    onChange={event => setAvailUntil(event.target.value)}
                  />
                </div>
              </div>
              {tags.length > 0 && (
                <div className="flex flex-col gap-2">
                  <Label>标签</Label>
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
                </div>
              )}

              <div className="flex flex-col gap-2">
                <Label>
                  成员单品
                  <span className="font-normal text-muted-foreground">已选 {itemIDs.length} 件</span>
                </Label>
                <div className="flex flex-wrap items-center gap-2">
                  <div className="relative">
                    <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      aria-label="搜索单品"
                      placeholder="搜索名称 / ID"
                      value={memberSearch}
                      onChange={event => setMemberSearch(event.target.value)}
                      className="w-48 pl-8"
                    />
                  </div>
                  <SimpleSelect
                    ariaLabel="品类过滤"
                    value={memberCategory}
                    onChange={setMemberCategory}
                    options={[
                      { value: "all", label: "全部品类" },
                      ...categories.map(cat => ({ value: cat.key, label: cat.name })),
                    ]}
                    className="w-36"
                  />
                </div>
                <div className="flex max-h-72 flex-col gap-1.5 overflow-y-auto rounded-xl border p-2">
                  {filteredItems.length === 0 && (
                    <p className="px-2 py-6 text-center text-xs text-muted-foreground">没有匹配的单品</p>
                  )}
                  {filteredItems.map(item => {
                    const meta = COSMETIC_STATUS_META[item.status ?? ""] ?? COSMETIC_STATUS_META.draft
                    const checked = itemIDs.includes(item.id)
                    return (
                      <label
                        key={item.id}
                        className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 transition-colors hover:bg-muted/50"
                      >
                        <Checkbox checked={checked} onCheckedChange={() => toggleItem(item.id)} aria-label={item.name} />
                        <CosmeticThumb item={item} className="size-9" />
                        <span className="min-w-0 flex-1 truncate text-sm">{item.name}</span>
                        <Badge variant="outline">{categoryName.get(item.category_key) ?? item.category_key}</Badge>
                        <Badge variant={meta.variant}>{meta.label}</Badge>
                      </label>
                    )
                  })}
                </div>
                {itemIDs.some(id => {
                  const found = allItems.find(item => item.id === id)
                  return found && found.status !== "published"
                }) && (
                  <p className="text-xs text-amber-600 dark:text-amber-500">
                    已选成员中含未上架（草稿/归档）单品，用户端将看不到这些内容。
                  </p>
                )}
              </div>
            </>
          )}
        </div>
        <SheetFooter className="flex-row justify-end border-t">
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onSave} disabled={busy || loading || !name.trim()}>
            {busy ? "保存中…" : bundleID ? "保存修改" : "创建"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
