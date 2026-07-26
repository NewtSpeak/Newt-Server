import { useEffect, useMemo, useState } from "react"
import { CheckCircle2Icon, CoinsIcon, GiftIcon, SearchIcon, ShoppingBagIcon } from "lucide-react"
import { toast } from "sonner"

import { CosmeticThumb } from "~/components/cosmetic-thumb"
import { PageHeader } from "~/components/page-header"
import { SimpleSelect } from "~/components/simple-select"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs"
import { useAsyncData } from "~/hooks/use-async-data"
import {
  grantCosmeticItem,
  grantCosmeticPoints,
  listCosmeticCategories,
  listCosmeticItems,
  listPlatformUsers,
  type PlatformUser,
} from "~/lib/api"
import { COSMETIC_STATUS_META, formatPrice, localInputToISO } from "~/lib/cosmetics"
import { cn } from "~/lib/utils"

/** 发放工具：搜索选中用户后，为其直接发放装扮或调整积分 */
export default function CosmeticGrantsPage() {
  const [query, setQuery] = useState("")
  const [search, setSearch] = useState("")
  const [selected, setSelected] = useState<PlatformUser | null>(null)

  // 用户防抖搜索（复用平台用户目录接口）
  useEffect(() => {
    const timer = setTimeout(() => setSearch(query.trim()), 300)
    return () => clearTimeout(timer)
  }, [query])

  const users = useAsyncData(
    search ? () => listPlatformUsers({ q: search, limit: 20 }).then(raw => raw.users) : null,
    [search]
  )

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="发放工具"
        description="面向单个用户的运营发放：装扮（幂等，可带过期时间）与积分（可负数扣减）。"
      />

      <section className="flex flex-col gap-4 px-4 lg:px-6">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">选择用户</CardTitle>
            <CardDescription>按用户名 / 邮箱搜索，点击结果选中；发放操作均针对选中用户。</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="relative w-full max-w-md">
              <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                aria-label="搜索用户"
                placeholder="搜索用户名 / 邮箱"
                value={query}
                onChange={event => setQuery(event.target.value)}
                className="pl-8"
              />
            </div>
            {selected && (
              <div className="flex items-center gap-3 rounded-xl border border-primary/40 bg-primary/5 px-4 py-3">
                <Avatar className="size-9">
                  {selected.avatar_url && <AvatarImage src={selected.avatar_url} alt="" />}
                  <AvatarFallback>{selected.username.slice(0, 2).toUpperCase()}</AvatarFallback>
                </Avatar>
                <div className="min-w-0 flex-1">
                  <p className="flex items-center gap-1.5 truncate text-sm font-medium">
                    {selected.username}
                    <Badge variant="default" className="gap-1">
                      <CheckCircle2Icon className="size-3" />
                      已选中
                    </Badge>
                  </p>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {selected.email} · {selected.id}
                  </p>
                </div>
                <Button variant="ghost" size="xs" onClick={() => setSelected(null)}>
                  取消选择
                </Button>
              </div>
            )}
            {users.status === "loading" && <LoadingState rows={3} />}
            {users.status === "error" && <ErrorState message={users.error} onRetry={() => users.reload()} />}
            {users.status === "success" && (users.data ?? []).length === 0 && (
              <EmptyState title="没有匹配的用户" description="换个关键词试试。" className="py-8" />
            )}
            {users.status === "success" &&
              (users.data ?? [])
                .filter(user => user.id !== selected?.id)
                .map(user => (
                  <button
                    key={user.id}
                    type="button"
                    onClick={() => setSelected(user)}
                    className={cn(
                      "flex items-center gap-3 rounded-xl border px-4 py-2.5 text-left transition-colors hover:bg-muted/50",
                      "outline-none focus-visible:ring-3 focus-visible:ring-ring/30"
                    )}
                  >
                    <Avatar className="size-8">
                      {user.avatar_url && <AvatarImage src={user.avatar_url} alt="" />}
                      <AvatarFallback>{user.username.slice(0, 2).toUpperCase()}</AvatarFallback>
                    </Avatar>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{user.username}</p>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {user.email} · {user.id}
                      </p>
                    </div>
                    {user.is_bot && <Badge variant="secondary">机器人</Badge>}
                  </button>
                ))}
          </CardContent>
        </Card>

        {selected ? (
          <Tabs defaultValue="item">
            <TabsList>
              <TabsTrigger value="item">
                <ShoppingBagIcon data-icon="inline-start" />
                发放装扮
              </TabsTrigger>
              <TabsTrigger value="points">
                <CoinsIcon data-icon="inline-start" />
                发放积分
              </TabsTrigger>
            </TabsList>
            <TabsContent value="item" className="pt-3">
              <GrantItemTab user={selected} />
            </TabsContent>
            <TabsContent value="points" className="pt-3">
              <GrantPointsTab user={selected} />
            </TabsContent>
          </Tabs>
        ) : (
          <EmptyState icon={GiftIcon} title="先选择一个用户" description="搜索并选中用户后可发放装扮或积分。" />
        )}
      </section>
    </main>
  )
}

// ---------------------------------------------------------------------------
// 发放装扮
// ---------------------------------------------------------------------------

function GrantItemTab({ user }: { user: PlatformUser }) {
  const items = useAsyncData(() => listCosmeticItems().then(raw => raw.items), [])
  const categories = useAsyncData(() => listCosmeticCategories().then(raw => raw.categories), [])
  const [keyword, setKeyword] = useState("")
  const [category, setCategory] = useState("all")
  const [itemID, setItemID] = useState("")
  const [expiresAt, setExpiresAt] = useState("")
  const [busy, setBusy] = useState(false)

  const categoryName = useMemo(() => {
    const map = new Map<string, string>()
    for (const cat of categories.data ?? []) map.set(cat.key, cat.name)
    return map
  }, [categories.data])

  const filtered = useMemo(() => {
    const query = keyword.trim().toLowerCase()
    return (items.data ?? []).filter(item => {
      if (category !== "all" && item.category_key !== category) return false
      if (query && !item.name.toLowerCase().includes(query) && !item.id.includes(query)) return false
      return true
    })
  }, [items.data, keyword, category])

  async function onGrant() {
    if (!itemID) return
    setBusy(true)
    try {
      const result = await grantCosmeticItem({
        user_id: user.id,
        item_id: itemID,
        expires_at: localInputToISO(expiresAt),
      })
      if (result.created) toast.success(`已向「${user.username}」发放该装扮`)
      else toast.info(`「${user.username}」已拥有该装扮（若指定了更晚的过期时间会自动续期）`)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "发放失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">发放装扮 · {user.username}</CardTitle>
        <CardDescription>幂等发放：已拥有时不会重复入库；可选过期时间，到期后自动失效并卸下。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              aria-label="搜索单品"
              placeholder="搜索名称 / ID"
              value={keyword}
              onChange={event => setKeyword(event.target.value)}
              className="w-56 pl-8"
            />
          </div>
          <SimpleSelect
            ariaLabel="品类过滤"
            value={category}
            onChange={setCategory}
            options={[
              { value: "all", label: "全部品类" },
              ...(categories.data ?? []).map(cat => ({ value: cat.key, label: cat.name })),
            ]}
            className="w-36"
          />
        </div>

        {items.status === "loading" && <LoadingState rows={4} />}
        {items.status === "error" && <ErrorState message={items.error} onRetry={() => items.reload()} />}
        {items.status === "success" && (
          <div className="flex max-h-80 flex-col gap-1.5 overflow-y-auto rounded-xl border p-2">
            {filtered.length === 0 && (
              <p className="px-2 py-6 text-center text-xs text-muted-foreground">没有匹配的单品</p>
            )}
            {filtered.map(item => {
              const meta = COSMETIC_STATUS_META[item.status ?? ""] ?? COSMETIC_STATUS_META.draft
              const active = itemID === item.id
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => setItemID(active ? "" : item.id)}
                  aria-pressed={active}
                  className={cn(
                    "flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-left transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/30",
                    active ? "bg-primary/10 ring-1 ring-primary/40" : "hover:bg-muted/50"
                  )}
                >
                  <CosmeticThumb item={item} className="size-9" />
                  <span className="min-w-0 flex-1 truncate text-sm">{item.name}</span>
                  <Badge variant="outline">{categoryName.get(item.category_key) ?? item.category_key}</Badge>
                  <Badge variant={meta.variant}>{meta.label}</Badge>
                  <span className="text-xs tabular-nums text-muted-foreground">{formatPrice(item.price_points)}</span>
                </button>
              )
            })}
          </div>
        )}

        <div className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="grant-expires">过期时间（可选，留空为永久）</Label>
            <Input
              id="grant-expires"
              type="datetime-local"
              value={expiresAt}
              onChange={event => setExpiresAt(event.target.value)}
              className="w-60"
            />
          </div>
          <Button onClick={onGrant} disabled={!itemID || busy}>
            <GiftIcon data-icon="inline-start" />
            {busy ? "发放中…" : "发放装扮"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 发放积分
// ---------------------------------------------------------------------------

function GrantPointsTab({ user }: { user: PlatformUser }) {
  const [amount, setAmount] = useState("")
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)

  const amountValue = Number(amount)
  const amountValid = Number.isFinite(amountValue) && Number.isInteger(amountValue) && amountValue !== 0

  async function onGrant() {
    if (!amountValid) return
    setBusy(true)
    try {
      const result = await grantCosmeticPoints({
        user_id: user.id,
        amount: amountValue,
        reason: reason.trim() || undefined,
      })
      toast.success(`积分已调整，「${user.username}」新余额：${result.balance}`)
      setAmount("")
      setReason("")
    } catch (error) {
      // 扣减至负数时后端返回 INSUFFICIENT_POINTS（中文 message 直接透出）
      toast.error(error instanceof Error ? error.message : "积分调整失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">发放积分 · {user.username}</CardTitle>
        <CardDescription>正数发放、负数扣减（余额不足会被拒绝）；变动实时推送到用户端。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor="points-amount">数额（可负，不能为 0）</Label>
            <Input
              id="points-amount"
              type="number"
              step={1}
              value={amount}
              onChange={event => setAmount(event.target.value)}
              placeholder="如 100 或 -50"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="points-reason">原因（可选，记入流水）</Label>
            <Input
              id="points-reason"
              value={reason}
              onChange={event => setReason(event.target.value)}
              placeholder="如 活动奖励"
              maxLength={200}
            />
          </div>
        </div>
        <div className="flex justify-end">
          <Button onClick={onGrant} disabled={!amountValid || busy}>
            <CoinsIcon data-icon="inline-start" />
            {busy ? "提交中…" : amountValue < 0 ? "扣减积分" : "发放积分"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
