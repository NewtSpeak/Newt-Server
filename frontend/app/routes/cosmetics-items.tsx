import { useEffect, useMemo, useState } from "react"
import { Link, useNavigate } from "react-router"
import { PlusIcon, SearchIcon, ShoppingBagIcon } from "lucide-react"
import { toast } from "sonner"

import { CosmeticTagBadge, CosmeticThumb } from "~/components/cosmetic-thumb"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
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
import { useAsyncData } from "~/hooks/use-async-data"
import {
  createCosmeticItem,
  listCosmeticCategories,
  listCosmeticItems,
  listCosmeticTags,
  type CosmeticCategory,
} from "~/lib/api"
import { COSMETIC_STATUS_META, COSMETIC_STATUS_OPTIONS, formatPrice } from "~/lib/cosmetics"

/** 装扮单品列表：品类/状态服务端筛选 + 标签/关键词前端筛选 */
export default function CosmeticItemsPage() {
  const navigate = useNavigate()
  const [category, setCategory] = useState("all")
  const [status, setStatus] = useState("all")
  const [tagID, setTagID] = useState("all")
  const [keyword, setKeyword] = useState("")
  const [search, setSearch] = useState("")
  const [createOpen, setCreateOpen] = useState(false)

  const categories = useAsyncData(() => listCosmeticCategories().then(raw => raw.categories), [])
  const tags = useAsyncData(() => listCosmeticTags().then(raw => raw.tags), [])
  const page = useAsyncData(
    () =>
      listCosmeticItems({
        category: category === "all" ? undefined : category,
        status: status === "all" ? undefined : status,
      }).then(raw => raw.items),
    [category, status]
  )

  // 关键词防抖：仅前端过滤，避免频繁 setState 抖动列表
  useEffect(() => {
    const timer = setTimeout(() => setSearch(keyword.trim().toLowerCase()), 300)
    return () => clearTimeout(timer)
  }, [keyword])

  const categoryName = useMemo(() => {
    const map = new Map<string, string>()
    for (const cat of categories.data ?? []) map.set(cat.key, cat.name)
    return map
  }, [categories.data])

  // 标签与关键词筛选在前端做（服务端只支持 category/status）
  const items = useMemo(() => {
    let list = page.data ?? []
    if (tagID !== "all") list = list.filter(item => (item.tags ?? []).some(tag => tag.id === tagID))
    if (search)
      list = list.filter(
        item =>
          item.name.toLowerCase().includes(search) ||
          item.description.toLowerCase().includes(search) ||
          item.id.includes(search)
      )
    return list
  }, [page.data, tagID, search])

  const categoryOptions = [
    { value: "all", label: "全部品类" },
    ...(categories.data ?? []).map(cat => ({ value: cat.key, label: cat.name })),
  ]
  const statusOptions = [{ value: "all", label: "全部状态" }, ...COSMETIC_STATUS_OPTIONS]
  const tagOptions = [
    { value: "all", label: "全部标签" },
    ...(tags.data ?? []).map(tag => ({ value: tag.id, label: tag.name })),
  ]

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="装扮单品"
        description="商店单品目录：草稿编辑、资产上传、上架与归档。品类/状态为服务端筛选，标签与关键词为本地过滤。"
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon data-icon="inline-start" />
            新建单品
          </Button>
        }
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              aria-label="搜索单品"
              placeholder="搜索名称 / 描述 / ID"
              value={keyword}
              onChange={event => setKeyword(event.target.value)}
              className="w-64 pl-8"
            />
          </div>
          <SimpleSelect ariaLabel="品类筛选" value={category} onChange={setCategory} options={categoryOptions} className="w-40" />
          <SimpleSelect ariaLabel="状态筛选" value={status} onChange={setStatus} options={statusOptions} className="w-32" />
          <SimpleSelect ariaLabel="标签筛选" value={tagID} onChange={setTagID} options={tagOptions} className="w-32" />
          <p className="ml-auto text-sm text-muted-foreground">
            共 <span className="tabular-nums">{items.length}</span> 件
          </p>
        </div>

        {page.status === "loading" && <LoadingState rows={6} />}
        {page.status === "error" && <ErrorState message={page.error} onRetry={() => page.reload()} />}
        {page.status === "success" && items.length === 0 && (
          <EmptyState icon={ShoppingBagIcon} title="没有匹配的单品" description="调整筛选条件，或新建一件单品。" />
        )}
        {page.status === "success" &&
          items.map((item, index) => {
            const meta = COSMETIC_STATUS_META[item.status ?? ""] ?? COSMETIC_STATUS_META.draft
            return (
              <Link
                key={item.id}
                to={`/cosmetics/items/${item.id}`}
                style={{ "--stagger-index": index } as React.CSSProperties}
                className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 transition-colors hover:bg-muted/50"
              >
                <CosmeticThumb item={item} />
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-1.5 text-sm font-medium">
                    <span className="truncate">{item.name}</span>
                    <Badge variant="outline">{categoryName.get(item.category_key) ?? item.category_key}</Badge>
                    <Badge variant={meta.variant}>{meta.label}</Badge>
                    {(item.tags ?? []).map(tag => (
                      <CosmeticTagBadge key={tag.id} tag={tag} />
                    ))}
                  </p>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {item.id}
                    {item.description ? ` · ${item.description}` : ""}
                  </p>
                </div>
                <span className="text-sm tabular-nums text-muted-foreground">{formatPrice(item.price_points)}</span>
              </Link>
            )
          })}
      </section>

      <CreateItemDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        categories={categories.data ?? []}
        onCreated={id => navigate(`/cosmetics/items/${id}`)}
      />
    </main>
  )
}

function CreateItemDialog({
  open,
  onClose,
  categories,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  categories: CosmeticCategory[]
  onCreated: (itemID: string) => void
}) {
  const [categoryKey, setCategoryKey] = useState("")
  const [name, setName] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setCategoryKey("")
      setName("")
    }
  }, [open])

  async function onCreate() {
    setBusy(true)
    try {
      const item = await createCosmeticItem({ category_key: categoryKey, name: name.trim() })
      toast.success("单品已创建（草稿）")
      onClose()
      onCreated(item.id)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "创建失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={next => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建单品</DialogTitle>
          <DialogDescription>创建后进入详情页补充资产与配置；品类一经创建不可变更，请谨慎选择。</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="new-item-category">品类（创建后不可变更）</Label>
            <SimpleSelect
              ariaLabel="品类"
              value={categoryKey || null}
              onChange={setCategoryKey}
              options={categories.map(cat => ({
                value: cat.key,
                label: cat.enabled ? cat.name : `${cat.name}（已停用）`,
              }))}
              placeholder="选择品类"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="new-item-name">名称</Label>
            <Input
              id="new-item-name"
              value={name}
              onChange={event => setName(event.target.value)}
              placeholder="单品名称"
              maxLength={100}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={onCreate} disabled={!categoryKey || !name.trim() || busy}>
            {busy ? "创建中…" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
