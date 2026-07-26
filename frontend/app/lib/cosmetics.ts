import type { CosmeticAssetSlotDef, CosmeticCategorySchema, CosmeticItem } from "~/lib/api"

/**
 * MIME 组 -> 允许的 Content-Type 列表（与 backend schema.go mimeBelongsGroup 对齐）。
 * 用于 <input accept> 与上传前端预校验。
 */
export const MIME_GROUP_ACCEPT: Record<string, string[]> = {
  image: ["image/png", "image/jpeg", "image/webp"],
  animated_image: ["image/gif", "image/png", "image/webp", "image/apng"],
  video: ["video/mp4", "video/webm"],
  audio: ["audio/ogg", "audio/mpeg", "audio/wav"],
}

/** MIME 组中文名（槽位说明文案用） */
export const MIME_GROUP_LABEL: Record<string, string> = {
  image: "静态图",
  animated_image: "动图",
  video: "视频",
  audio: "音频",
}

const DEFAULT_MAX_ASSET_BYTES = 12 * 1024 * 1024 // 与 common.go defaultMaxAssetBytes 对齐
const MAX_AUDIO_BYTES = 2 * 1024 * 1024 // 与 common.go maxAudioBytes 对齐

/** 该槽允许的 MIME 列表（去重） */
export function mimesForSlot(slot: CosmeticAssetSlotDef): string[] {
  const groups = slot.mime_groups ?? []
  const out: string[] = []
  for (const group of groups) {
    for (const mime of MIME_GROUP_ACCEPT[group] ?? []) {
      if (!out.includes(mime)) out.push(mime)
    }
  }
  return out
}

/** 生成 <input accept> 值；未声明组时不限制 */
export function acceptForSlot(slot: CosmeticAssetSlotDef): string {
  return mimesForSlot(slot).join(",")
}

/** 该槽字节上限：优先 schema 显式 max_bytes，否则按是否含 audio 组回退默认值 */
export function maxBytesForSlot(slot: CosmeticAssetSlotDef): number {
  if (slot.max_bytes && slot.max_bytes > 0) return slot.max_bytes
  if ((slot.mime_groups ?? []).includes("audio")) return MAX_AUDIO_BYTES
  return DEFAULT_MAX_ASSET_BYTES
}

/** 字节数人类可读格式 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B"
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** 单品/捆绑包状态 Badge 元数据 */
export const COSMETIC_STATUS_META: Record<
  string,
  { label: string; variant: "default" | "secondary" | "destructive" | "outline" }
> = {
  draft: { label: "草稿", variant: "secondary" },
  published: { label: "已上架", variant: "default" },
  archived: { label: "已归档", variant: "outline" },
}

export const COSMETIC_STATUS_OPTIONS = [
  { value: "draft", label: "草稿" },
  { value: "published", label: "已上架" },
  { value: "archived", label: "已归档" },
]

/** schema 安全取资产槽列表 */
export function schemaAssetSlots(schema: CosmeticCategorySchema | undefined | null): CosmeticAssetSlotDef[] {
  return schema?.asset_slots ?? []
}

/** 预览媒体：返回 URL 与是否为视频（列表缩略图用静音循环 video 标签渲染视频 mime） */
export function previewMedia(item: CosmeticItem): { url: string; isVideo: boolean } | null {
  const url = item.preview_url
  if (!url) return null
  for (const asset of Object.values(item.assets ?? {})) {
    if (asset.url === url) return { url, isVideo: asset.mime.startsWith("video/") }
  }
  return { url, isVideo: false }
}

/** ISO 时间 -> datetime-local 输入值（本地时区，分钟精度）；空值返回空串 */
export function isoToLocalInput(iso: string | null | undefined): string {
  if (!iso) return ""
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** datetime-local 输入值 -> ISO 时间；空值返回 undefined（后端忽略 = 不修改） */
export function localInputToISO(value: string): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return undefined
  return date.toISOString()
}

/** 价格展示：0 = 免费 */
export function formatPrice(points: number): string {
  return points === 0 ? "免费" : `${points} 积分`
}

/** 品类 schema JSON 模板（新建品类 textarea 打底，字段对齐 schema.go） */
export const CATEGORY_SCHEMA_TEMPLATE = `{
  "render_hint": "",
  "asset_slots": [
    { "key": "primary", "label": "主资产", "required": true, "mime_groups": ["image", "animated_image", "video"], "max_bytes": 8388608 }
  ],
  "payload_fields": [
    { "key": "motion", "type": "enum", "values": ["static", "animated"], "default": "static" }
  ]
}`
