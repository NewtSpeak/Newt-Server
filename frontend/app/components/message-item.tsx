import { useState } from "react"
import { HistoryIcon, PaperclipIcon, Trash2Icon } from "lucide-react"
import { toast } from "sonner"

import { Avatar, AvatarFallback } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "~/components/ui/sheet"
import { listMessageEdits, type Message, type MessageEdit } from "~/lib/api"
import { formatBytes, formatFullTime, formatTime } from "~/lib/format"

export function MessageItem({ message, index }: { message: Message; index: number }) {
  const [historyOpen, setHistoryOpen] = useState(false)
  const [edits, setEdits] = useState<MessageEdit[] | null>(null)
  const [loadingEdits, setLoadingEdits] = useState(false)

  const author = message.author_username ?? message.author_id
  const deleted = Boolean(message.deleted_at)
  const editCount = message.edit_count ?? 0

  async function openHistory() {
    setHistoryOpen(true)
    if (edits || loadingEdits) return
    setLoadingEdits(true)
    try {
      setEdits(await listMessageEdits(message.channel_id, message.id))
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "编辑历史加载失败")
    } finally {
      setLoadingEdits(false)
    }
  }

  // 软删墓碑（文档 13 AQ.3）
  if (deleted) {
    return (
      <div
        style={{ "--stagger-index": Math.min(index, 12) } as React.CSSProperties}
        className="anim-item flex items-center gap-2 rounded-xl border border-dashed px-4 py-3 text-xs text-muted-foreground"
      >
        <Trash2Icon className="size-3.5" />
        该消息已被删除（保留审计记录） · {formatTime(message.deleted_at)}
      </div>
    )
  }

  return (
    <article
      style={{ "--stagger-index": Math.min(index, 12) } as React.CSSProperties}
      className="anim-item flex gap-3 rounded-xl border px-4 py-3 transition-[background-color] hover:bg-muted/40"
    >
      <Avatar className="size-9 shrink-0">
        <AvatarFallback>{author.slice(0, 2).toUpperCase()}</AvatarFallback>
      </Avatar>
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span className="text-sm font-medium">{author}</span>
          <time className="text-xs text-muted-foreground tabular-nums">{formatFullTime(message.created_at)}</time>
          {editCount > 0 && (
            <button
              type="button"
              onClick={openHistory}
              className="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] text-muted-foreground transition-[background-color,color] hover:bg-muted hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none"
            >
              <HistoryIcon className="size-3" />
              已编辑 <span className="tabular-nums">{editCount}</span> 次
            </button>
          )}
        </p>
        {message.content && <p className="mt-1 text-sm break-words whitespace-pre-wrap">{message.content}</p>}
        {(message.attachments?.length ?? 0) > 0 && (
          <div className="mt-2 flex flex-wrap gap-1.5">
            {message.attachments!.map(attachment => (
              <Badge key={attachment.id} variant="outline" className="h-6 max-w-60 gap-1 font-normal">
                <PaperclipIcon />
                <span className="truncate">{attachment.filename ?? attachment.id}</span>
                <span className="shrink-0 text-muted-foreground tabular-nums">{formatBytes(attachment.size)}</span>
              </Badge>
            ))}
          </div>
        )}
      </div>

      <Sheet open={historyOpen} onOpenChange={setHistoryOpen}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>编辑历史</SheetTitle>
            <SheetDescription>每次编辑保存全文快照；普通成员仅可见编辑次数。</SheetDescription>
          </SheetHeader>
          <div className="flex flex-col gap-3 overflow-y-auto px-6 pb-6">
            <div className="rounded-xl border border-primary/30 bg-primary/5 p-3">
              <p className="text-xs font-medium text-muted-foreground">当前版本</p>
              <p className="mt-1 text-sm break-words whitespace-pre-wrap">{message.content}</p>
            </div>
            {loadingEdits && <p className="text-xs text-muted-foreground">加载中…</p>}
            {edits
              ?.slice()
              .sort((a, b) => b.version - a.version)
              .map(edit => (
                <div key={edit.version} className="rounded-xl border p-3">
                  <p className="flex justify-between text-xs text-muted-foreground">
                    <span>
                      版本 <span className="tabular-nums">{edit.version}</span>
                    </span>
                    <time className="tabular-nums">{formatFullTime(edit.edited_at)}</time>
                  </p>
                  <p className="mt-1 text-sm break-words whitespace-pre-wrap">{edit.content}</p>
                </div>
              ))}
            {edits && edits.length === 0 && <p className="text-xs text-muted-foreground">暂无历史版本记录。</p>}
          </div>
        </SheetContent>
      </Sheet>
    </article>
  )
}
