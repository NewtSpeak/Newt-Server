export type User = {
  id: string
  username: string
  email: string
  system_admin: boolean
  avatar_url?: string
  avatar_animated?: boolean
  banner_url?: string
  accent_color?: string
}

export type TokenResponse = {
  access_token: string
  refresh_token: string
  access_expires_at: string
  refresh_expires_at: string
  user: User
}

/** 服务器 banner（多张有序，docs 协议/服务器外观资产.md） */
export type GuildBanner = {
  id: string
  guild_id: string
  /** 公开访问路径（/public-assets/profile/...），不可变可长缓存 */
  url: string
  /** 展示顺序（0 起连续升序） */
  position: number
  created_at?: string
  updated_at?: string
}

export type Guild = {
  id: string
  name: string
  description?: string
  owner_user_id: string
  /** 服务器图标 / 横幅公开 URL（/public-assets/profile/...），空串表示未设置 */
  icon_url?: string
  banner_url?: string
  /** 多 banner 列表（position 升序）；列表/详情响应与 GUILD_UPDATE 事件均携带 */
  banners?: GuildBanner[]
  /** 受限徽章服级开关（docs 08 AM.4） */
  restriction_badge_visible?: boolean
  /** Restriction 创建是否强制填写 reason（docs 08 AI.2，仅系统管可改） */
  restriction_reason_required?: boolean
}

export type Role = {
  id: string
  guild_id: string
  name: string
  permissions: number
  position: number
  is_everyone: boolean
  /** 角色名样式 JSON 字符串（RoleStyle schema），"{}" 表示无样式 */
  style?: string
  /** 角色主色（#RRGGBB，空串 = 默认色） */
  color?: string
  /** 是否在成员列表单独分组显示 */
  hoist?: boolean
  /** 是否允许任何人 @提及该角色 */
  mentionable?: boolean
}

export type RegistrationStatus = { registration_open: boolean }

type ApiError = { error?: { code?: string; message?: string } }

const baseURL = "/api/v1"

export function getSession(): TokenResponse | null {
  if (typeof window === "undefined") return null
  const value = localStorage.getItem("owl-session")
  if (!value) return null
  try {
    return JSON.parse(value) as TokenResponse
  } catch {
    localStorage.removeItem("owl-session")
    return null
  }
}

export function saveSession(session: TokenResponse | null) {
  if (typeof window === "undefined") return
  if (session) localStorage.setItem("owl-session", JSON.stringify(session))
  else localStorage.removeItem("owl-session")
}

export function hasUsableSession() {
  const session = getSession()
  return Boolean(session?.refresh_token && new Date(session.refresh_expires_at).getTime() > Date.now())
}

async function refreshSession(session: TokenResponse) {
  const response = await fetch(`${baseURL}/auth/refresh`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: session.refresh_token }),
  })
  if (!response.ok) {
    saveSession(null)
    return null
  }
  const next = (await response.json()) as TokenResponse
  saveSession(next)
  return next
}

export async function logout() {
  const session = getSession()
  if (session) {
    await fetch(`${baseURL}/auth/logout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: session.refresh_token }),
    }).catch(() => undefined)
  }
  saveSession(null)
}

export async function api<T>(path: string, init: RequestInit = {}, retry = true): Promise<T> {
  let session = getSession()
  if (session && new Date(session.access_expires_at).getTime() <= Date.now() && retry) {
    session = await refreshSession(session)
  }
  const headers = new Headers(init.headers)
  if (init.body) headers.set("Content-Type", "application/json")
  if (session) headers.set("Authorization", `Bearer ${session.access_token}`)
  const response = await fetch(`${baseURL}${path}`, { ...init, headers })
  if (response.status === 401 && session && retry) {
    const refreshed = await refreshSession(session)
    if (refreshed) return api<T>(path, init, false)
  }
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message ?? `请求失败（${response.status}）`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

// ---------------------------------------------------------------------------
// 通用工具
// ---------------------------------------------------------------------------

function qs(params: Record<string, string | number | boolean | undefined | null>) {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") search.set(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ""
}

export function gatewayURL() {
  if (typeof window === "undefined") return ""
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${protocol}//${window.location.host}${baseURL}/gateway`
}

// ---------------------------------------------------------------------------
// 频道 / 成员 / 角色 / 权限
// ---------------------------------------------------------------------------

export type ChannelType = "TEXT" | "VOICE" | "CATEGORY"

export type Channel = {
  id: string
  guild_id: string
  name: string
  type: ChannelType
  topic?: string
  position: number
  /** 所属分类频道 ID（docs 03 FR-03），null/缺省 = 未分组 */
  parent_id?: string | null
  /** 语音频道人数上限（0 = 不限，docs 09 FR-40） */
  user_limit?: number
  /** 文本频道慢速模式秒数（0 = 关闭，docs 03 §8-9） */
  rate_limit_per_user?: number
  /** 慢速模式豁免角色；为空表示对所有成员生效 */
  rate_limit_exempt_role_ids?: string[]
}

export type GuildMember = {
  user_id: string
  username?: string
  nickname?: string | null
  role_ids?: string[]
  joined_at?: string
}

export function memberName(member: GuildMember) {
  return member.nickname || member.username || member.user_id
}

export const listGuilds = () => api<Guild[]>("/guilds")
export const createGuild = (name: string) => api<Guild>("/guilds", { method: "POST", body: JSON.stringify({ name }) })
/** 服务器详情 + 成员总数（docs 02 §8-1） */
export const getGuildDetail = (gid: string) => api<{ guild: Guild; member_count: number }>(`/guilds/${gid}`)
export const listChannels = (gid: string) => api<Channel[]>(`/guilds/${gid}/channels`)
export const createChannel = (
  gid: string,
  body: { name: string; type: ChannelType; parent_id?: string; user_limit?: number; rate_limit_per_user?: number; rate_limit_exempt_role_ids?: string[] }
) => api<Channel>(`/guilds/${gid}/channels`, { method: "POST", body: JSON.stringify(body) })
export const listMembers = (gid: string) => api<GuildMember[]>(`/guilds/${gid}/members`)
export const listRoles = (gid: string) => api<Role[]>(`/guilds/${gid}/roles`)
export const createRole = (
  gid: string,
  body: { name: string; position: number; permissions: number; color?: string; hoist?: boolean; mentionable?: boolean }
) => api<Role>(`/guilds/${gid}/roles`, { method: "POST", body: JSON.stringify(body) })
export const updateRole = (
  gid: string,
  rid: string,
  body: Partial<Pick<Role, "name" | "position" | "permissions" | "color" | "hoist" | "mentionable">>
) => api<Role>(`/guilds/${gid}/roles/${rid}`, { method: "PATCH", body: JSON.stringify(body) })
/** 角色批量排序（docs 04 §8：拖拽调整层级，@everyone 不参与） */
export const reorderRoles = (gid: string, entries: { id: string; position: number }[]) =>
  api<void>(`/guilds/${gid}/roles`, { method: "PATCH", body: JSON.stringify(entries) })
export const addMemberRole = (gid: string, mid: string, rid: string) =>
  api<void>(`/guilds/${gid}/members/${mid}/roles/${rid}`, { method: "PUT" })
export const removeMemberRole = (gid: string, mid: string, rid: string) =>
  api<void>(`/guilds/${gid}/members/${mid}/roles/${rid}`, { method: "DELETE" })
export const getMyPermissions = (gid: string) => api<{ permissions: number | string }>(`/guilds/${gid}/permissions/@me`)

export type OverwriteType = "ROLE" | "MEMBER"

export const putChannelOverwrite = (
  gid: string,
  cid: string,
  targetID: string,
  body: { type: OverwriteType; allow: number; deny: number }
) => api<void>(`/guilds/${gid}/channels/${cid}/overwrites/${targetID}`, { method: "PUT", body: JSON.stringify(body) })

// ---------------------------------------------------------------------------
// SFU 节点与节点池（后端并行开发中；集成期若命名有差异，仅需调整本区路径）
// ---------------------------------------------------------------------------

export type SfuNodeStatus = "PENDING_ENROLLMENT" | "ENROLLED" | "ONLINE" | "DRAINING" | "DISABLED" | "REVOKED"

export type SfuNode = {
  node_id: string
  display_name: string
  status: SfuNodeStatus
  cert_fingerprint?: string
  cert_not_after?: string
  labels: Record<string, string>
  /** 节点程序版本（Register/Heartbeat 上报） */
  node_version?: string
  capacity?: {
    max_users?: number
    current_users?: number
    bandwidth_out_mbps?: number
    cpu_pct?: number
    mem_pct?: number
  }
  enabled_for_scheduling: boolean
  platform_default?: boolean
  online?: boolean
  created_at?: string
  enrolled_at?: string
  last_seen_at?: string
}

export type SfuNodeCreated = { node_id: string; enrollment_token: string }
export type SfuNodeAction = "enable" | "disable" | "drain" | "undrain" | "revoke"

// 后端存在两种节点视图（capacity 嵌套的摘要版 / 平铺字段版 / {nodes:[]} 包裹），此处统一归一化
type RawSfuNode = {
  id?: string
  node_id?: string
  display_name: string
  status: SfuNodeStatus
  cert_fingerprint?: string
  cert_not_after?: string
  labels?: Record<string, string> | null
  node_version?: string
  capacity?: {
    max_users?: number
    current_users?: number
    bandwidth_out_mbps?: number
    cpu_pct?: number
    mem_pct?: number
  }
  max_users?: number
  current_users?: number
  bandwidth_out_mbps?: number
  cpu_pct?: number
  mem_pct?: number
  enabled_for_scheduling?: boolean
  platform_default?: boolean
  online?: boolean
  created_at?: string
  enrolled_at?: string
  last_seen_at?: string
}

function normalizeSfuNode(raw: RawSfuNode): SfuNode {
  return {
    node_id: raw.id ?? raw.node_id ?? "",
    display_name: raw.display_name,
    status: raw.status,
    cert_fingerprint: raw.cert_fingerprint,
    cert_not_after: raw.cert_not_after,
    labels: raw.labels ?? {},
    node_version: raw.node_version ?? "",
    capacity: raw.capacity ?? {
      max_users: raw.max_users,
      current_users: raw.current_users,
      bandwidth_out_mbps: raw.bandwidth_out_mbps,
      cpu_pct: raw.cpu_pct,
      mem_pct: raw.mem_pct,
    },
    enabled_for_scheduling: Boolean(raw.enabled_for_scheduling),
    platform_default: raw.platform_default,
    online: raw.online,
    created_at: raw.created_at,
    enrolled_at: raw.enrolled_at,
    last_seen_at: raw.last_seen_at,
  }
}

export const listSfuNodes = () =>
  api<RawSfuNode[] | { nodes: RawSfuNode[] }>("/admin/sfu/nodes").then(raw => {
    const rows = Array.isArray(raw) ? raw : (raw.nodes ?? [])
    return rows.map(normalizeSfuNode)
  })
export const getSfuNode = (id: string) => api<RawSfuNode>(`/admin/sfu/nodes/${id}`).then(normalizeSfuNode)

/** 级联拓扑（管理台可视化）：节点 + 边累计字节/路径类型；前端差分得 bps */
export type SfuTopologyPathType = "lan" | "wan" | "unknown" | string

export type SfuTopologyEdge = {
  room_id: string
  epoch: number
  parent_node_id: string
  child_node_id: string
  up: boolean
  rtt_ms: number
  bytes_tx: number
  bytes_rx: number
  path_type: SfuTopologyPathType
  local_ip?: string
  remote_ip?: string
  last_seen_at?: string
}

export type SfuTopologyAggEdge = {
  parent_node_id: string
  child_node_id: string
  up: boolean
  rtt_ms: number
  bytes_tx: number
  bytes_rx: number
  path_type: SfuTopologyPathType
  room_count: number
  local_ip?: string
  remote_ip?: string
}

/** Owl-Server 控制面节点（拓扑图中心） */
export type SfuTopologyServer = {
  id: string
  display_name: string
  role: string
  http_address?: string
  sfu_control_endpoint?: string
  sfu_control_listen?: string
  public_base_url?: string
  online: boolean
  connected_sfu_count: number
}

/** Server ↔ SFU 控制通道（gRPC mTLS，无媒体） */
export type SfuTopologyControlLink = {
  server_id: string
  node_id: string
  up: boolean
  kind: string
}

export type SfuTopology = {
  generated_at: string
  server: SfuTopologyServer
  nodes: SfuNode[]
  control_links: SfuTopologyControlLink[]
  edges: SfuTopologyEdge[]
  aggregated_edges: SfuTopologyAggEdge[]
}

type RawSfuTopology = {
  generated_at: string
  server?: SfuTopologyServer
  nodes: RawSfuNode[]
  control_links?: SfuTopologyControlLink[]
  edges?: SfuTopologyEdge[]
  aggregated_edges?: SfuTopologyAggEdge[]
}

export const getSfuTopology = () =>
  api<RawSfuTopology>("/admin/sfu/topology").then(
    (raw): SfuTopology => ({
      generated_at: raw.generated_at,
      server: raw.server ?? {
        id: "owl-server",
        display_name: "Owl-Server",
        role: "control_plane",
        online: true,
        connected_sfu_count: 0,
      },
      nodes: (raw.nodes ?? []).map(normalizeSfuNode),
      control_links: raw.control_links ?? [],
      edges: raw.edges ?? [],
      aggregated_edges: raw.aggregated_edges ?? [],
    }),
  )
export const createSfuNode = (body: { display_name: string; labels?: Record<string, string> }) =>
  api<{ node?: RawSfuNode; node_id?: string; enrollment_token: string }>("/admin/sfu/nodes", {
    method: "POST",
    body: JSON.stringify(body),
  }).then(
    (raw): SfuNodeCreated => ({
      node_id: raw.node?.id ?? raw.node?.node_id ?? raw.node_id ?? "",
      enrollment_token: raw.enrollment_token,
    }),
  )
/** 更新名称/地域标签/调度开关/平台默认池（不改变生命周期状态） */
export const updateSfuNode = (
  id: string,
  body: {
    display_name?: string
    labels?: Record<string, string>
    enabled_for_scheduling?: boolean
    platform_default?: boolean
  },
) =>
  api<RawSfuNode>(`/admin/sfu/nodes/${id}`, {
    method: "PATCH",
    body: JSON.stringify(body),
  }).then(normalizeSfuNode)
export const sfuNodeAction = (id: string, action: SfuNodeAction) =>
  api<RawSfuNode>(`/admin/sfu/nodes/${id}/${action}`, { method: "POST" }).then(normalizeSfuNode)

export type SfuRelease = {
  filename: string
  version: string
  goos: string
  goarch: string
  size: number
  url: string
}

export const listSfuReleases = () =>
  api<{ releases: SfuRelease[]; release_dir: string }>("/admin/sfu/releases")

export type UpdateSfuBinaryBody = {
  target_version?: string
  download_url?: string
  sha256_hex?: string
  force?: boolean
  goos?: string
  goarch?: string
  /** 升级前排空并均匀迁到附近节点（默认 true） */
  drain_first?: boolean
  /** 等待迁空秒数，默认 90 */
  drain_timeout_sec?: number
}

export type RollingUpgradeDrain = {
  sessions_before: number
  sessions_after: number
  jobs_created: number
  drained: boolean
  elapsed_ms: number
  forced?: boolean
}

export type UpdateSfuBinaryResult = {
  node_id: string
  target_version: string
  download_url: string
  sha256_hex?: string
  command_id?: string
  ok: boolean
  error_code?: string
  error_message?: string
  note?: string
  drain?: RollingUpgradeDrain
}

/** 远程升级节点 SFU 程序（节点在线时） */
export const updateSfuBinary = (id: string, body: UpdateSfuBinaryBody) =>
  api<UpdateSfuBinaryResult>(`/admin/sfu/nodes/${id}/update-binary`, {
    method: "POST",
    body: JSON.stringify(body),
  })

export type NodePool = { node_ids?: string[] }

// 后端返回 { candidates, selected, fallback_to_default }；页面只关心已勾选集合
type RawNodePool = { candidates?: { id: string }[]; selected?: { id: string }[]; fallback_to_default?: boolean }

const normalizePool = (raw: RawNodePool): NodePool => ({ node_ids: (raw.selected ?? []).map(node => node.id) })

export const getGuildNodePool = (gid: string) => api<RawNodePool>(`/guilds/${gid}/node-pool`).then(normalizePool)
export const putGuildNodePool = (gid: string, nodeIDs: string[], asSystemAdmin: boolean) =>
  api<RawNodePool>(asSystemAdmin ? `/admin/guilds/${gid}/node-pool` : `/guilds/${gid}/node-pool`, {
    method: "PUT",
    // 系统管路径同时授权候选集与勾选集；服管路径仅勾选（docs 07 专项 2.1）
    body: JSON.stringify(
      asSystemAdmin ? { candidate_node_ids: nodeIDs, selected_node_ids: nodeIDs } : { node_ids: nodeIDs },
    ),
  }).then(normalizePool)

// ---------------------------------------------------------------------------
// 语音状态与迁移
// ---------------------------------------------------------------------------

export type StageRole = "AUDIENCE" | "QUEUED" | "SPEAKER"

export type VoiceState = {
  user_id: string
  username?: string
  guild_id?: string
  channel_id: string
  self_mute?: boolean
  self_deaf?: boolean
  server_mute?: boolean
  server_deaf?: boolean
  stage_role?: StageRole
  capacity_muted?: boolean
  self_stream?: boolean
  node_id?: string
  joined_at?: string
}

// 后端响应为 { voice_states: [...] }（docs 05 §2.2 模型字段），此处解包为数组
export const listVoiceStates = (gid: string, cid: string) =>
  api<{ voice_states?: VoiceState[] }>(`/guilds/${gid}/channels/${cid}/voice-states`).then(raw => raw.voice_states ?? [])
// 管理员断开（docs 05 §8.1）：POST /guilds/{gid}/voice/disconnect
export const disconnectVoiceUser = (gid: string, _cid: string, uid: string) =>
  api<void>(`/guilds/${gid}/voice/disconnect`, { method: "POST", body: JSON.stringify({ user_id: uid }) })
/** 管理员移动成员到另一语音频道（docs 09 FR-29：MOVE_MEMBERS + 层级） */
export const moveVoiceUser = (gid: string, userID: string, channelID: string) =>
  api<{ moved: boolean; to_channel_id?: string }>(`/guilds/${gid}/voice/move`, {
    method: "POST",
    body: JSON.stringify({ user_id: userID, channel_id: channelID }),
  })
// 手动热迁移（docs 09 §3.6）：以「用户语音会话」为粒度
export const createVoiceMigration = (body: { guild_id: string; user_id: string; to_node_id?: string }) =>
  api<{ migration_id?: string }>("/admin/voice/migrations", { method: "POST", body: JSON.stringify(body) })

// ---------------------------------------------------------------------------
// 舞台（文档 11 §8）
// ---------------------------------------------------------------------------

export type VoiceChannelMode = "FREE_DISCUSSION" | "STAGE"

export type StageConfig = {
  mode: VoiceChannelMode
  max_speakers: number
  request_to_speak_enabled: boolean
  allow_co_mod_change_mode?: boolean
  co_moderator_ids?: string[]
}

export type StageQueueEntry = {
  user_id: string
  username?: string
  requested_at?: string
  source?: "USER_APPLY" | "CAPACITY_QUEUE" | "ADMIN"
}

// 后端响应 { channel_id, queue: [{position,user_id,name}], queue_extended?: [{source,requested_at,…}] }
// 全员简表 + 管理者扩展字段合并为页面使用的条目结构（docs 11 AE.1）
type RawStageQueue = {
  queue?: { position: number; user_id: string; name?: string }[]
  queue_extended?: { user_id: string; source?: StageQueueEntry["source"]; requested_at?: string }[]
}

export const patchVoiceStage = (cid: string, body: Partial<StageConfig>) =>
  api<StageConfig>(`/channels/${cid}/voice-stage`, { method: "PATCH", body: JSON.stringify(body) })
export const getStageQueue = (cid: string) =>
  api<RawStageQueue>(`/channels/${cid}/stage/queue`).then((raw): StageQueueEntry[] => {
    const extended = new Map((raw.queue_extended ?? []).map(item => [item.user_id, item]))
    return (raw.queue ?? []).map(item => ({
      user_id: item.user_id,
      username: item.name || undefined,
      requested_at: extended.get(item.user_id)?.requested_at,
      source: extended.get(item.user_id)?.source,
    }))
  })
export const stageBringUp = (cid: string, userID: string) =>
  api<void>(`/channels/${cid}/stage/bring-up`, { method: "POST", body: JSON.stringify({ user_id: userID }) })
export const stageBringDown = (cid: string, userID: string) =>
  api<void>(`/channels/${cid}/stage/bring-down`, { method: "POST", body: JSON.stringify({ user_id: userID }) })
/** 管理员将他人移出麦序队列（docs 10 FR-15，需 STAGE_MANAGE_QUEUE / STAGE_BRING_UP） */
export const stageRemoveFromQueue = (cid: string, userID: string) =>
  api<void>(`/channels/${cid}/stage/queue/${userID}`, { method: "DELETE" })

// ---------------------------------------------------------------------------
// Restriction（文档 12 §4）
// ---------------------------------------------------------------------------

export type RestrictionScope = "TEXT_CHANNEL" | "VOICE_CHANNEL" | "GUILD_ALL_TEXT" | "GUILD_ALL_VOICE"
export type RestrictionKind = "SANCTION" | "CHANNEL_BAN"

export type RestrictionDeny = {
  view_text?: boolean
  send_text?: boolean
  listen_voice?: boolean
  speak_voice?: boolean
}

export type Restriction = {
  id: string
  guild_id: string
  target_user_id: string
  target_username?: string
  scope: RestrictionScope
  channel_id?: string | null
  deny: RestrictionDeny
  kind: RestrictionKind
  reason?: string
  expires_at?: string | null
  created_at?: string
  created_by?: string
  lifted_at?: string | null
  lifted_by?: string | null
  active?: boolean
}

export const listRestrictions = (
  gid: string,
  filters: { user_id?: string; channel_id?: string; active?: boolean; scope?: RestrictionScope } = {}
) => api<Restriction[]>(`/guilds/${gid}/restrictions${qs(filters)}`)
export const createRestriction = (
  gid: string,
  body: {
    target_user_id: string
    scope: RestrictionScope
    channel_id?: string
    deny: RestrictionDeny
    kind: RestrictionKind
    reason: string
    expires_at?: string | null
  }
) => api<Restriction>(`/guilds/${gid}/restrictions`, { method: "POST", body: JSON.stringify(body) })
export const patchRestriction = (gid: string, id: string, body: { expires_at?: string | null; reason?: string }) =>
  api<Restriction>(`/guilds/${gid}/restrictions/${id}`, { method: "PATCH", body: JSON.stringify(body) })
export const liftRestriction = (gid: string, id: string) => api<void>(`/guilds/${gid}/restrictions/${id}`, { method: "DELETE" })

// ---------------------------------------------------------------------------
// 成员治理：邀请 / 踢出 / 封禁
// ---------------------------------------------------------------------------

export type Invite = {
  code: string
  guild_id?: string
  expires_at?: string | null
  /** 0 = 不限次数 */
  max_uses?: number
  uses?: number
}
export type Ban = { user_id: string; username?: string; reason?: string; created_at?: string; created_by?: string }

export const createInvite = (gid: string) => api<Invite>(`/guilds/${gid}/invites`, { method: "POST", body: JSON.stringify({}) })
export const kickMember = (gid: string, memberID: string) => api<void>(`/guilds/${gid}/members/${memberID}`, { method: "DELETE" })
export const listBans = (gid: string) => api<Ban[]>(`/guilds/${gid}/bans`)
export const banUser = (gid: string, userID: string, reason?: string) =>
  api<void>(`/guilds/${gid}/bans/${userID}`, { method: "PUT", body: JSON.stringify({ reason }) })
export const unbanUser = (gid: string, userID: string) => api<void>(`/guilds/${gid}/bans/${userID}`, { method: "DELETE" })

// ---------------------------------------------------------------------------
// 消息与搜索（文档 13）
// ---------------------------------------------------------------------------

export type Attachment = {
  id: string
  filename?: string
  mime?: string
  size?: number
  url?: string
  /** 图片像素尺寸（docs 07 §8-5，非图片为 0/缺省） */
  width?: number
  height?: number
}

/** 消息反应聚合（docs 05 FR-26）：me 表示调用者是否已反应 */
export type ReactionSummary = { emoji: string; count: number; me: boolean }

export type Message = {
  id: string
  guild_id?: string
  channel_id: string
  author_id: string
  author_username?: string
  type?: string
  content: string
  reply_to_id?: string | null
  attachments?: Attachment[]
  reactions?: ReactionSummary[]
  edit_count?: number
  edited_at?: string | null
  created_at: string
  deleted_at?: string | null
}

export type MessageEdit = {
  message_id: string
  version: number
  content: string
  edited_at: string
  editor_id: string
}

// 后端列表响应为 { messages: [...] }，此处解包为数组
export const listMessages = (cid: string, params: { before?: string; limit?: number } = {}) =>
  api<{ messages?: Message[] }>(`/channels/${cid}/messages${qs(params)}`).then(raw => raw.messages ?? [])
export const sendMessage = (cid: string, content: string) =>
  api<Message>(`/channels/${cid}/messages`, {
    method: "POST",
    body: JSON.stringify({ content, nonce: crypto.randomUUID() }),
  })
// 后端响应分别为 { edits, edit_count } 与 { messages }，此处解包为数组
export const listMessageEdits = (cid: string, mid: string) =>
  api<{ edits?: MessageEdit[] }>(`/channels/${cid}/messages/${mid}/edits`).then(raw => raw.edits ?? [])
export type SearchResult = { messages: Message[]; total: number }

export const searchMessages = (params: {
  q: string
  guild_id?: string
  channel_id?: string
  author_id?: string
  limit?: number
}) =>
  api<{ messages?: Message[]; total?: number }>(`/search/messages${qs(params)}`).then(
    (raw): SearchResult => ({ messages: raw.messages ?? [], total: raw.total ?? raw.messages?.length ?? 0 }),
  )

// ---------------------------------------------------------------------------
// 机器人（bot 专项）：档案 / 令牌 / 安装到服；权限赋予复用成员角色端点
// ---------------------------------------------------------------------------

export type Bot = {
  id: string
  user_id: string
  owner_user_id: string
  /** 非空 = 服级独属机器人 */
  home_guild_id?: string | null
  name: string
  description: string
  avatar_url: string
  username: string
  token_count: number
  guild_count: number
  created_at: string
}

export type BotToken = {
  id: string
  bot_id: string
  name: string
  prefix: string
  last_used_at?: string | null
  expires_at?: string | null
  revoked_at?: string | null
  created_at: string
}

export type GuildBot = {
  id: string
  user_id: string
  owner_user_id?: string
  /** 非空 = 本服独属机器人（服主创建）；空 = 平台级 */
  home_guild_id?: string | null
  name: string
  description: string
  avatar_url: string
  username: string
  member_id: string
  role_ids: string[]
  created_at?: string
  updated_at?: string
}

export const listBots = () => api<Bot[]>("/bots")
export const createBot = (body: { name: string; username: string; description?: string; avatar_url?: string }) =>
  api<Bot>("/bots", { method: "POST", body: JSON.stringify(body) })
export const updateBot = (id: string, body: { name?: string; description?: string; avatar_url?: string }) =>
  api<Bot>(`/bots/${id}`, { method: "PATCH", body: JSON.stringify(body) })
export const deleteBot = (id: string) => api<void>(`/bots/${id}`, { method: "DELETE" })

export const listBotTokens = (botID: string) =>
  api<{ tokens?: BotToken[] }>(`/bots/${botID}/tokens`).then(raw => raw.tokens ?? [])
export const createBotToken = (botID: string, body: { name?: string; expires_at?: string | null } = {}) =>
  api<{ token: BotToken; plain: string }>(`/bots/${botID}/tokens`, { method: "POST", body: JSON.stringify(body) })
export const revokeBotToken = (botID: string, tokenID: string) =>
  api<void>(`/bots/${botID}/tokens/${tokenID}`, { method: "DELETE" })

export const listGuildBots = (gid: string) => api<GuildBot[]>(`/guilds/${gid}/bots`)
/** 在本服创建独属机器人（自动安装） */
export const createGuildBot = (
  gid: string,
  body: { name: string; username: string; description?: string; avatar_url?: string },
) => api<GuildBot>(`/guilds/${gid}/bots`, { method: "POST", body: JSON.stringify(body) })
export const updateGuildBot = (
  gid: string,
  botID: string,
  body: { name?: string; description?: string; avatar_url?: string },
) => api<GuildBot>(`/guilds/${gid}/bots/${botID}`, { method: "PATCH", body: JSON.stringify(body) })
export const installBot = (gid: string, botID: string) =>
  api<{ member_id: string }>(`/guilds/${gid}/bots/${botID}`, { method: "PUT" })
/** 服级 bot 整档删除；平台 bot 仅卸载 */
export const uninstallBot = (gid: string, botID: string) =>
  api<void>(`/guilds/${gid}/bots/${botID}`, { method: "DELETE" })
export const listGuildBotTokens = (gid: string, botID: string) =>
  api<{ tokens?: BotToken[] }>(`/guilds/${gid}/bots/${botID}/tokens`).then(raw => raw.tokens ?? [])
export const createGuildBotToken = (
  gid: string,
  botID: string,
  body: { name?: string; expires_at?: string | null } = {},
) =>
  api<{ token: BotToken; plain: string }>(`/guilds/${gid}/bots/${botID}/tokens`, {
    method: "POST",
    body: JSON.stringify(body),
  })
export const revokeGuildBotToken = (gid: string, botID: string, tokenID: string) =>
  api<void>(`/guilds/${gid}/bots/${botID}/tokens/${tokenID}`, { method: "DELETE" })

// ---------------------------------------------------------------------------
// 审计日志（治理）
// ---------------------------------------------------------------------------

export type AuditActorType = "user" | "system_admin" | "guild_admin" | "auto" | "node"

export type AuditUndoStatus = "none" | "available" | "undone" | "blocked" | "irreversible"

export type AuditLogEntry = {
  id: string
  actor_id?: string | null
  actor_type: AuditActorType
  actor_username?: string
  guild_id?: string | null
  guild_name?: string
  action: string
  action_label?: string
  target_type: string
  target_id: string
  target_summary?: string
  detail: Record<string, unknown>
  created_at: string
  reversible?: boolean
  undo_status?: AuditUndoStatus
  undo_hint?: string
  undo_of_id?: string | null
  undone_by_id?: string | null
  undone_at?: string | null
}

export type AuditLogPage = {
  items: AuditLogEntry[]
  next_cursor?: string
  has_more: boolean
}

export type AuditLogFilters = {
  guild_id?: string
  actor_id?: string
  /** action 前缀匹配，如 "restriction." */
  action?: string
  target_type?: string
  undo_status?: AuditUndoStatus
  since?: string
  until?: string
  /** 游标分页：上一页返回的 next_cursor */
  before?: string
  limit?: number
}

export type UndoAuditLogResult = {
  original: AuditLogEntry
  undo: AuditLogEntry
}

/** 系统管理员：跨服全量审计流水 */
export const listAdminAuditLogs = (filters: AuditLogFilters = {}) =>
  api<AuditLogPage>(`/admin/audit-logs${qs(filters)}`)
/** 单服审计流水（需 VIEW_AUDIT_LOG 权限位或服管） */
export const listGuildAuditLogs = (gid: string, filters: Omit<AuditLogFilters, "guild_id"> = {}) =>
  api<AuditLogPage>(`/guilds/${gid}/audit-logs${qs(filters)}`)
/** 系统管理员全量入口撤销 */
export const undoAdminAuditLog = (logId: string) =>
  api<UndoAuditLogResult>(`/admin/audit-logs/${logId}/undo`, { method: "POST" })
/** 本服撤销 */
export const undoGuildAuditLog = (gid: string, logId: string) =>
  api<UndoAuditLogResult>(`/guilds/${gid}/audit-logs/${logId}/undo`, { method: "POST" })

// ---------------------------------------------------------------------------
// 屏幕共享配额（文档 14 §7.2）
// ---------------------------------------------------------------------------

export type ScreenQuota = {
  base_limit: number
  effective_limit: number
  active: number
  dynamic_enabled: boolean
  channel_default_limit?: number
}

// 后端字段：base_limit / effective_limit / dynamic_enabled / used（前端沿用 active 命名）
type RawScreenQuota = {
  base_limit: number
  effective_limit: number
  dynamic_enabled: boolean
  dynamic_cap?: number
  used: number
}

const normalizeQuota = (raw: RawScreenQuota): ScreenQuota => ({
  base_limit: raw.base_limit,
  effective_limit: raw.effective_limit,
  active: raw.used,
  dynamic_enabled: raw.dynamic_enabled,
})

// ---------------------------------------------------------------------------
// 展示自定义：角色名样式 / 徽章 / 头像横幅 / 成员展示聚合
// ---------------------------------------------------------------------------

export type RoleStyleType = "" | "solid" | "linear" | "radial"

/** 文字/icon 共用表面样式字段 */
export type RoleSurfaceStyle = {
  type: RoleStyleType
  colors?: string[]
  angle?: number
  shape?: "circle" | "ellipse"
  animated?: boolean
  /** 流动动画周期（秒），0.5–20，默认 4 */
  speed?: number
}

/** 文字装饰（用户名 / 徽章文本共用） */
export type RoleTextDecor = {
  bold?: boolean
  italic?: boolean
  underline?: boolean
  strikethrough?: boolean
}

/** 角色徽章（消息流/成员列表标签） */
export type RoleBadgeStyle = RoleTextDecor & {
  enabled?: boolean
  background?: RoleSurfaceStyle
  /** 背景图（可与渐变叠加） */
  background_image_url?: string
  icon_url?: string
  show_name?: boolean
  text_color?: string
}

/**
 * 角色名样式（Role.Style jsonb）：
 * - 文字侧 type/colors/…/speed + bold/italic/underline/strikethrough
 * - icon_sync / icon：色点
 * - badge：徽章背景 + 自定义 icon + 文字装饰
 */
export type RoleStyle = RoleSurfaceStyle &
  RoleTextDecor & {
    icon_sync?: boolean
    icon?: RoleSurfaceStyle
    badge?: RoleBadgeStyle
  }

function parseSurface(raw: RoleSurfaceStyle | undefined | null): RoleSurfaceStyle | undefined {
  if (!raw?.type) return undefined
  if (raw.type !== "solid" && raw.type !== "linear" && raw.type !== "radial") return undefined
  return {
    type: raw.type,
    colors: raw.colors,
    angle: raw.angle,
    shape: raw.shape,
    animated: raw.animated,
    speed: raw.speed,
  }
}

function parseTextDecor(raw: RoleTextDecor | undefined | null): RoleTextDecor {
  return {
    bold: raw?.bold || undefined,
    italic: raw?.italic || undefined,
    underline: raw?.underline || undefined,
    strikethrough: raw?.strikethrough || undefined,
  }
}

export function parseRoleStyle(raw: string | undefined | null): RoleStyle {
  if (!raw) return { type: "" }
  try {
    const parsed = JSON.parse(raw) as RoleStyle
    if (!parsed) return { type: "" }
    const type =
      parsed.type === "solid" || parsed.type === "linear" || parsed.type === "radial"
        ? parsed.type
        : ("" as const)
    const textDecor = parseTextDecor(parsed)
    const badgeDecor = parseTextDecor(parsed.badge)
    const badge = parsed.badge
      ? {
          enabled: parsed.badge.enabled,
          background: parseSurface(parsed.badge.background),
          background_image_url: parsed.badge.background_image_url || undefined,
          icon_url: parsed.badge.icon_url || undefined,
          show_name: parsed.badge.show_name,
          text_color: parsed.badge.text_color || undefined,
          ...badgeDecor,
        }
      : undefined
    const hasBadge =
      badge &&
      (badge.enabled ||
        badge.background ||
        badge.background_image_url ||
        badge.icon_url ||
        badge.text_color ||
        badge.bold ||
        badge.italic ||
        badge.underline ||
        badge.strikethrough)
    const hasTextDecor =
      textDecor.bold || textDecor.italic || textDecor.underline || textDecor.strikethrough
    if (!type && !hasBadge && !hasTextDecor) return { type: "" }
    return {
      type,
      colors: parsed.colors,
      angle: parsed.angle,
      shape: parsed.shape,
      animated: parsed.animated,
      speed: parsed.speed,
      ...textDecor,
      icon_sync: parsed.icon_sync,
      icon: parseSurface(parsed.icon),
      badge: hasBadge ? badge : undefined,
    }
  } catch {
    return { type: "" }
  }
}

async function uploadRoleBadgeAsset(
  gid: string,
  rid: string,
  path: "badge-icon" | "badge-background",
  file: File,
) {
  let session = getSession()
  if (session && new Date(session.access_expires_at).getTime() <= Date.now()) {
    session = await refreshSession(session)
  }
  const headers = new Headers()
  headers.set("Content-Type", file.type || "application/octet-stream")
  if (session) headers.set("Authorization", `Bearer ${session.access_token}`)
  const response = await fetch(`${baseURL}/guilds/${gid}/roles/${rid}/${path}`, {
    method: "PUT",
    headers,
    body: file,
  })
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message ?? `上传失败（${response.status}）`)
  }
  return response.json() as Promise<{
    role: Role
    icon_url?: string
    background_image_url?: string
  }>
}

/** 角色徽章 icon 上传（原始字节，勿走 JSON Content-Type） */
export async function uploadRoleBadgeIcon(gid: string, rid: string, file: File) {
  return uploadRoleBadgeAsset(gid, rid, "badge-icon", file)
}

export const deleteRoleBadgeIcon = (gid: string, rid: string) =>
  api<Role>(`/guilds/${gid}/roles/${rid}/badge-icon`, { method: "DELETE" })

/** 角色徽章背景图上传 */
export async function uploadRoleBadgeBackground(gid: string, rid: string, file: File) {
  return uploadRoleBadgeAsset(gid, rid, "badge-background", file)
}

export const deleteRoleBadgeBackground = (gid: string, rid: string) =>
  api<Role>(`/guilds/${gid}/roles/${rid}/badge-background`, { method: "DELETE" })

/** 解析出 icon 实际应用的表面样式（sync 用文字；独立用 icon；无则 null） */
export function resolveRoleIconStyle(style: RoleStyle | null | undefined): RoleSurfaceStyle | null {
  if (!style?.type) return null
  if (style.icon_sync) {
    return {
      type: style.type,
      colors: style.colors,
      angle: style.angle,
      shape: style.shape,
      animated: style.animated,
      speed: style.speed,
    }
  }
  if (style.icon?.type) return style.icon
  // 无高级 icon 样式时回退文字纯色/首色，便于色点仍有颜色
  if (style.colors?.[0]) {
    return { type: "solid", colors: [style.colors[0]] }
  }
  return null
}

export const updateRoleStyle = (gid: string, rid: string, style: RoleStyle) =>
  api<Role>(`/guilds/${gid}/roles/${rid}/style`, { method: "PUT", body: JSON.stringify(style) })

export type RoleFeatureBits = {
  manage_bots: boolean
  manage_badges: boolean
  manage_customization: boolean
}

export const getRoleFeatureBits = (gid: string, rid: string) =>
  api<RoleFeatureBits>(`/guilds/${gid}/roles/${rid}/feature-bits`)
export const patchRoleFeatureBits = (gid: string, rid: string, body: Partial<RoleFeatureBits>) =>
  api<RoleFeatureBits>(`/guilds/${gid}/roles/${rid}/feature-bits`, { method: "PATCH", body: JSON.stringify(body) })

export type GuildBadge = {
  id: string
  guild_id: string
  name: string
  description: string
  emoji: string
  icon_url: string
  color: string
  created_at?: string
}

export type BadgeGrant = {
  id: string
  badge_id: string
  user_id: string
  username?: string
  granted_by: string
  granted_at?: string
  expires_at?: string | null
}

export const listBadges = (gid: string) => api<GuildBadge[]>(`/guilds/${gid}/badges`)
export const createBadge = (gid: string, body: Omit<GuildBadge, "id" | "guild_id" | "created_at">) =>
  api<GuildBadge>(`/guilds/${gid}/badges`, { method: "POST", body: JSON.stringify(body) })
export const updateBadge = (gid: string, bid: string, body: Omit<GuildBadge, "id" | "guild_id" | "created_at">) =>
  api<GuildBadge>(`/guilds/${gid}/badges/${bid}`, { method: "PATCH", body: JSON.stringify(body) })
export const deleteBadge = (gid: string, bid: string) => api<void>(`/guilds/${gid}/badges/${bid}`, { method: "DELETE" })
export const listBadgeGrants = (gid: string, bid: string) => api<BadgeGrant[]>(`/guilds/${gid}/badges/${bid}/grants`)
/** 授予徽章：body 缺省为永久；days 有效天数；until 截止时间（RFC3339） */
export const grantBadge = (gid: string, bid: string, userID: string, body: { days?: number; until?: string } = {}) =>
  api<BadgeGrant>(`/guilds/${gid}/badges/${bid}/members/${userID}`, { method: "PUT", body: JSON.stringify(body) })
export const revokeBadge = (gid: string, bid: string, userID: string) =>
  api<void>(`/guilds/${gid}/badges/${bid}/members/${userID}`, { method: "DELETE" })

export type MemberBadgeView = {
  badge_id: string
  name: string
  description: string
  emoji: string
  icon_url: string
  color: string
  granted_at: string
  expires_at?: string | null
}

/** 成员展示聚合：头像/横幅/强调色 + 解析后的用户名样式 + 有效徽章 */
export type MemberDisplay = {
  id: string
  user_id: string
  username: string
  nickname: string
  is_owner: boolean
  is_bot: boolean
  avatar_url: string
  avatar_animated: boolean
  banner_url: string
  accent_color: string
  role_ids: string[]
  name_style: RoleStyle | Record<string, never>
  name_style_role_id?: string | null
  badges: MemberBadgeView[]
}

export const listMembersDisplay = (gid: string) => api<MemberDisplay[]>(`/guilds/${gid}/members/display`)

export const patchMyProfile = (body: { accent_color?: string; clear_avatar?: boolean; clear_banner?: boolean }) =>
  api<User>("/users/@me/profile", { method: "PATCH", body: JSON.stringify(body) })

/** 上传本人头像/横幅（原始字节 PUT，Content-Type 声明格式；GIF 视为动态头像） */
export async function uploadMyProfileImage(kind: "avatar" | "banner", file: File): Promise<User> {
  const session = getSession()
  const response = await fetch(`${baseURL}/users/@me/${kind}`, {
    method: "PUT",
    headers: {
      "Content-Type": file.type || "application/octet-stream",
      ...(session ? { Authorization: `Bearer ${session.access_token}` } : {}),
    },
    body: file,
  })
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message ?? `上传失败（${response.status}）`)
  }
  return response.json() as Promise<User>
}

// ---------------------------------------------------------------------------
// 邀请分享落地页：内容管理 / 邀请列表 / 全局门户配置
// ---------------------------------------------------------------------------

export type InviteNoticeKind = "ANNOUNCEMENT" | "NOTICE" | "AGREEMENT"

export type InviteNotice = {
  id: string
  guild_id: string
  kind: InviteNoticeKind
  title: string
  body: string
  position: number
  enabled: boolean
}

export type InviteLandingConfig = {
  guild_id: string
  description: string
  enabled: boolean
  auto_deep_link: boolean
}

export type InvitePortalConfig = {
  app_name: string
  deep_link_scheme: string
  windows_url: string
  macos_url: string
  linux_url: string
  android_url: string
  ios_url: string
  website_url: string
}

export type SharedInvite = Invite & {
  id?: string
  created_at?: string
  share_url: string
  deep_link: string
}

export const getInviteLanding = (gid: string) =>
  api<{ config: InviteLandingConfig; notices: InviteNotice[] }>(`/guilds/${gid}/invite-landing`)
export const putInviteLanding = (gid: string, body: Partial<InviteLandingConfig>) =>
  api<InviteLandingConfig>(`/guilds/${gid}/invite-landing`, { method: "PUT", body: JSON.stringify(body) })
export const createInviteNotice = (
  gid: string,
  body: { kind: InviteNoticeKind; title: string; body: string; position?: number; enabled?: boolean }
) => api<InviteNotice>(`/guilds/${gid}/invite-notices`, { method: "POST", body: JSON.stringify(body) })
export const updateInviteNotice = (
  gid: string,
  nid: string,
  body: { kind: InviteNoticeKind; title: string; body: string; position?: number; enabled?: boolean }
) => api<InviteNotice>(`/guilds/${gid}/invite-notices/${nid}`, { method: "PATCH", body: JSON.stringify(body) })
export const deleteInviteNotice = (gid: string, nid: string) =>
  api<void>(`/guilds/${gid}/invite-notices/${nid}`, { method: "DELETE" })
export const listInvites = (gid: string) => api<SharedInvite[]>(`/guilds/${gid}/invites`)
export const revokeInvite = (gid: string, code: string) => api<void>(`/guilds/${gid}/invites/${code}`, { method: "DELETE" })
export const createInviteWithTTL = (gid: string, ttlSeconds?: number, maxUses?: number) =>
  api<Invite>(`/guilds/${gid}/invites`, {
    method: "POST",
    body: JSON.stringify({
      ...(ttlSeconds ? { ttl_seconds: ttlSeconds } : {}),
      ...(maxUses ? { max_uses: maxUses } : {}),
    }),
  })
export const getInvitePortal = () => api<InvitePortalConfig>("/admin/invite-portal")
export const putInvitePortal = (body: Partial<InvitePortalConfig>) =>
  api<InvitePortalConfig>("/admin/invite-portal", { method: "PUT", body: JSON.stringify(body) })

// ---------------------------------------------------------------------------
// 系统管理员临场 / 音频审计（adminpresence）
// ---------------------------------------------------------------------------

export type PlatformAuditConfig = {
  record_default: boolean
  notify_default: boolean
  /** 主节点是否配置了 AUDIT_INGEST_TOKEN（SFU 上传录音所需） */
  ingest_enabled?: boolean
  updated_at?: string
}
export type ChannelAuditConfig = {
  channel_id: string
  guild_id?: string
  has_override: boolean
  record: boolean
  notify: boolean
}
export type AuditRecord = {
  id: string
  guild_id: string
  channel_id: string
  user_id: string
  /** 说话者用户名（列表接口批量补齐） */
  username?: string
  /** 说话者显示名（可空） */
  display_name?: string
  session_id: string
  node_id: string
  mime: string
  size: number
  started_at: string
  ended_at: string
  created_at: string
}

export const getPlatformAudit = () => api<PlatformAuditConfig>("/admin/audit-config")
export const putPlatformAudit = (body: Partial<PlatformAuditConfig>) =>
  api<PlatformAuditConfig>("/admin/audit-config", { method: "PUT", body: JSON.stringify(body) })
export const getChannelAudit = (cid: string) => api<ChannelAuditConfig>(`/admin/channels/${cid}/audit-config`)
export const putChannelAudit = (cid: string, body: { inherit?: boolean; record?: boolean; notify?: boolean }) =>
  api<ChannelAuditConfig>(`/admin/channels/${cid}/audit-config`, { method: "PUT", body: JSON.stringify(body) })
export const postPresenceMessage = (cid: string, content: string) =>
  api<{ id: string; channel_id: string; author_id: string; content: string; created_at: string }>(
    `/admin/channels/${cid}/presence/message`,
    { method: "POST", body: JSON.stringify({ content }) }
  )
export const getVoiceStealth = (gid: string) =>
  api<{ guild_id: string; hidden: boolean }>(`/admin/voice/stealth?guild_id=${gid}`)
export const putVoiceStealth = (gid: string, hidden: boolean) =>
  api<{ guild_id: string; hidden: boolean }>("/admin/voice/stealth", {
    method: "PUT",
    body: JSON.stringify({ guild_id: gid, hidden }),
  })
export const listAuditRecords = (filters: { guild_id?: string; channel_id?: string; user_id?: string } = {}) =>
  api<{ records: AuditRecord[] }>(`/admin/audit-records${qs(filters)}`).then(raw => raw.records ?? [])

// downloadAuditRecord 带鉴权头拉取录音并触发浏览器下载（端点需系统管理员，故不能用裸链接）。
export async function downloadAuditRecord(id: string) {
  let session = getSession()
  if (session && new Date(session.access_expires_at).getTime() <= Date.now()) {
    session = await refreshSession(session)
  }
  const response = await fetch(`${baseURL}/admin/audit-records/${id}/audio`, {
    headers: session ? { Authorization: `Bearer ${session.access_token}` } : {},
  })
  if (!response.ok) throw new Error(`下载失败（${response.status}）`)
  const blob = await response.blob()
  const url = URL.createObjectURL(blob)
  const link = document.createElement("a")
  link.href = url
  link.download = `${id}.ogg`
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

export const getScreenQuota = (gid: string) => api<RawScreenQuota>(`/guilds/${gid}/screen-quota`).then(normalizeQuota)
// 服基准与平台动态开关分属两个后端端点（docs 14 §7.2），此处聚合为一次保存
export const patchScreenQuota = async (gid: string, body: { base_limit?: number; dynamic_enabled?: boolean }) => {
  if (body.dynamic_enabled !== undefined) {
    await api("/admin/screen-quota/settings", {
      method: "PATCH",
      body: JSON.stringify({ dynamic_screen_quota_enabled: body.dynamic_enabled }),
    })
  }
  if (body.base_limit !== undefined) {
    return api<RawScreenQuota>(`/admin/guilds/${gid}/screen-quota`, {
      method: "PATCH",
      body: JSON.stringify({ max_concurrent_screens: body.base_limit }),
    }).then(normalizeQuota)
  }
  return getScreenQuota(gid)
}

// ---------------------------------------------------------------------------
// 服务器结构治理：生命周期 / 频道管理 / 覆盖读回 / 昵称（guildapi + moderation）
// ---------------------------------------------------------------------------

export const updateGuild = (
  gid: string,
  body: { name?: string; description?: string; restriction_badge_visible?: boolean; restriction_reason_required?: boolean }
) => api<Guild>(`/guilds/${gid}`, { method: "PATCH", body: JSON.stringify(body) })

/** 上传服务器图标 / 横幅（multipart 字段 file，≤8MB，PNG/JPEG/WebP/GIF/MP4；需 MANAGE_GUILD） */
export async function uploadGuildImage(gid: string, kind: "icon" | "banner", file: File) {
  const session = getSession()
  const form = new FormData()
  form.append("file", file)
  const response = await fetch(`${baseURL}/guilds/${gid}/${kind}`, {
    method: "POST",
    headers: session ? { Authorization: `Bearer ${session.access_token}` } : {},
    body: form,
  })
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message ?? `上传失败（${response.status}）`)
  }
  return response.json() as Promise<{ url: string; guild: Guild }>
}
export const deleteGuildImage = (gid: string, kind: "icon" | "banner") =>
  api<Guild>(`/guilds/${gid}/${kind}`, { method: "DELETE" })

// ---------------------------------------------------------------------------
// 服务器多 banner（docs 协议/服务器外观资产.md）：每服多张有序，默认上限 10
// ---------------------------------------------------------------------------

export const listGuildBanners = (gid: string) =>
  api<{ guild_id: string; banners: GuildBanner[]; limit: number }>(`/guilds/${gid}/banners`)

/** 新增 banner（multipart 字段 file，≤8MB，PNG/JPEG/WebP/GIF/MP4；需 MANAGE_GUILD），追加到末尾 */
export async function addGuildBanner(gid: string, file: File) {
  const session = getSession()
  const form = new FormData()
  form.append("file", file)
  const response = await fetch(`${baseURL}/guilds/${gid}/banners`, {
    method: "POST",
    headers: session ? { Authorization: `Bearer ${session.access_token}` } : {},
    body: form,
  })
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message ?? `上传失败（${response.status}）`)
  }
  return response.json() as Promise<{ banner: GuildBanner; banners: GuildBanner[] }>
}

/** 重排序：banner_ids 为全量有序数组（必须恰好覆盖全部 banner，服务端按下标重排） */
export const reorderGuildBanners = (gid: string, bannerIDs: string[]) =>
  api<{ banners: GuildBanner[] }>(`/guilds/${gid}/banners`, {
    method: "PATCH",
    body: JSON.stringify({ banner_ids: bannerIDs }),
  })

export const deleteGuildBanner = (gid: string, bannerID: string) =>
  api<{ banners: GuildBanner[] }>(`/guilds/${gid}/banners/${bannerID}`, { method: "DELETE" })
/** 删除服务器：confirm_name 必须与服务器名完全一致（防误删） */
export const deleteGuild = (gid: string, confirmName: string) =>
  api<void>(`/guilds/${gid}`, { method: "DELETE", body: JSON.stringify({ confirm_name: confirmName }) })
export const transferGuildOwnership = (gid: string, newOwnerUserID: string) =>
  api<Guild>(`/guilds/${gid}/transfer-ownership`, {
    method: "POST",
    body: JSON.stringify({ new_owner_user_id: newOwnerUserID }),
  })

export const deleteRole = (gid: string, rid: string) => api<void>(`/guilds/${gid}/roles/${rid}`, { method: "DELETE" })

export const updateChannel = (
  cid: string,
  body: { name?: string; topic?: string; parent_id?: string | null; user_limit?: number; rate_limit_per_user?: number; rate_limit_exempt_role_ids?: string[] }
) => api<Channel>(`/channels/${cid}`, { method: "PATCH", body: JSON.stringify(body) })
export const deleteChannel = (cid: string) => api<void>(`/channels/${cid}`, { method: "DELETE" })
/** 批量保存频道排序（拖拽后整体提交） */
export const reorderChannels = (gid: string, entries: { id: string; position: number }[]) =>
  api<void>(`/guilds/${gid}/channels`, { method: "PATCH", body: JSON.stringify(entries) })

/** 频道既有权限覆盖读回（编辑器回显） */
export type ChannelOverwriteView = {
  id: string
  channel_id: string
  type: OverwriteType
  target_id: string
  target_name: string
  allow: number
  deny: number
  allow_str: string
  deny_str: string
}

export const listChannelOverwrites = (gid: string, cid: string) =>
  api<ChannelOverwriteView[]>(`/guilds/${gid}/channels/${cid}/overwrites`)
export const deleteChannelOverwrite = (gid: string, cid: string, targetID: string, type: OverwriteType) =>
  api<void>(`/guilds/${gid}/channels/${cid}/overwrites/${targetID}?type=${type}`, { method: "DELETE" })

/** 修改成员昵称（本人 CHANGE_NICKNAME / 他人 MANAGE_NICKNAMES）；空字符串清除 */
export const updateMemberNickname = (gid: string, memberID: string, nickname: string) =>
  api<{ id: string; nickname: string }>(`/guilds/${gid}/members/${memberID}`, {
    method: "PATCH",
    body: JSON.stringify({ nickname }),
  })

// ---------------------------------------------------------------------------
// 服务器策略：上传上限 / 消息保留 / 语音包 / 舞台配置（message + stage）
// ---------------------------------------------------------------------------

export type UploadLimit = {
  guild_id: string
  /** 0 = 未配置（跟随平台默认） */
  upload_limit_bytes: number
  effective_bytes: number
  default_bytes?: number
}

export const getUploadLimit = (gid: string) => api<UploadLimit>(`/admin/guilds/${gid}/upload-limit`)
export const patchUploadLimit = (gid: string, bytes: number) =>
  api<UploadLimit>(`/admin/guilds/${gid}/upload-limit`, {
    method: "PATCH",
    body: JSON.stringify({ upload_limit_bytes: bytes }),
  })

export const getMessageRetention = (gid: string) =>
  api<{ guild_id: string; retention_days: number }>(`/guilds/${gid}/message-retention`)
export const patchMessageRetention = (gid: string, days: number) =>
  api<{ guild_id: string; retention_days: number }>(`/guilds/${gid}/message-retention`, {
    method: "PATCH",
    body: JSON.stringify({ retention_days: days }),
  })

export type VoicePackConfig = {
  guild_id: string
  enabled: boolean
  audio_url: string
  scope: string
  trigger: string
}

export const getGuildVoicePack = (gid: string) => api<VoicePackConfig>(`/guilds/${gid}/voice-pack`)
export const patchGuildVoicePack = (
  gid: string,
  body: { enabled?: boolean; audio_url?: string; scope?: string; trigger?: string }
) => api<VoicePackConfig>(`/guilds/${gid}/voice-pack`, { method: "PATCH", body: JSON.stringify(body) })

/** 频道级语音包开关：无记录时默认允许播放 */
export type ChannelVoicePack = { channel_id: string; guild_id: string; allowed: boolean }

export const getChannelVoicePack = (gid: string, cid: string) =>
  api<ChannelVoicePack>(`/guilds/${gid}/channels/${cid}/voice-pack`)
export const putChannelVoicePack = (gid: string, cid: string, allowed: boolean) =>
  api<ChannelVoicePack>(`/guilds/${gid}/channels/${cid}/voice-pack`, {
    method: "PUT",
    body: JSON.stringify({ allowed }),
  })

export type VoiceStageConfig = {
  channel_id: string
  mode: VoiceChannelMode
  max_speakers: number
  request_to_speak_enabled: boolean
  allow_co_mod_change_mode: boolean
  co_moderator_ids: string[]
  /** -1 = 未独立配置（跟随默认） */
  max_concurrent_screens: number
}

export const getVoiceStageConfig = (cid: string) => api<VoiceStageConfig>(`/channels/${cid}/voice-stage`)
export const patchVoiceStageConfig = (
  cid: string,
  body: Partial<Omit<VoiceStageConfig, "channel_id">>
) => api<VoiceStageConfig>(`/channels/${cid}/voice-stage`, { method: "PATCH", body: JSON.stringify(body) })

// ---------------------------------------------------------------------------
// 平台用户治理（platformadmin，系统管理员）
// ---------------------------------------------------------------------------

export type PlatformUser = User & {
  is_bot: boolean
  display_name?: string
  disabled_at?: string | null
  created_at?: string
}

export type PlatformUserPage = { users: PlatformUser[]; total: number; limit: number; offset: number }
export type PlatformUserFilter = "" | "disabled" | "admin" | "bot"

export const listPlatformUsers = (params: { q?: string; limit?: number; offset?: number; filter?: PlatformUserFilter } = {}) =>
  api<PlatformUserPage>(`/admin/users${qs(params)}`)
export const disablePlatformUser = (uid: string) =>
  api<PlatformUser>(`/admin/users/${uid}/disable`, { method: "POST" })
export const enablePlatformUser = (uid: string) => api<PlatformUser>(`/admin/users/${uid}/enable`, { method: "POST" })
export const resetPlatformUserPassword = (uid: string, password: string) =>
  api<{ user_id: string; sessions_revoked: boolean }>(`/admin/users/${uid}/reset-password`, {
    method: "POST",
    body: JSON.stringify({ password }),
  })
export const patchPlatformUserSystemAdmin = (uid: string, systemAdmin: boolean) =>
  api<PlatformUser>(`/admin/users/${uid}/system-admin`, {
    method: "PATCH",
    body: JSON.stringify({ system_admin: systemAdmin }),
  })

export type RegistrationSetting = { signup_enabled: boolean; source: "db" | "default" }

export const getRegistrationSetting = () => api<RegistrationSetting>("/admin/registration")
export const putRegistrationSetting = (enabled: boolean) =>
  api<RegistrationSetting>("/admin/registration", { method: "PUT", body: JSON.stringify({ signup_enabled: enabled }) })

/** 注册邀请链接（凭码注册可绕过注册开关，仅系统管理员） */
export type RegistrationInviteStatus = "active" | "expired" | "exhausted" | "revoked"

export type RegistrationInvite = {
  id: string
  code: string
  share_url: string
  deep_link: string
  created_by: string
  created_at: string
  expires_at: string | null
  max_uses: number
  uses: number
  revoked: boolean
  status: RegistrationInviteStatus
}

export const listRegistrationInvites = () => api<RegistrationInvite[]>("/admin/registration-invites")
export const createRegistrationInvite = (ttlSeconds?: number, maxUses?: number) =>
  api<RegistrationInvite>("/admin/registration-invites", {
    method: "POST",
    body: JSON.stringify({ ttl_seconds: ttlSeconds || undefined, max_uses: maxUses || undefined }),
  })
export const revokeRegistrationInvite = (id: string) =>
  api<void>(`/admin/registration-invites/${id}`, { method: "DELETE" })

// ---------------------------------------------------------------------------
// 账号安全（userapi）：改密码 / 登录会话
// ---------------------------------------------------------------------------

export type LoginSession = {
  id: string
  audience: "admin" | "client"
  created_at: string
  last_used_at: string
  expires_at: string
  current: boolean
  /** 设备元数据（docs 01 FR-27）：登录/最近轮换时采集，历史会话为空串 */
  device_name?: string
  platform?: string
  ip_address?: string
}

export const changeMyPassword = (currentPassword: string, newPassword: string) =>
  api<void>("/users/@me/password", {
    method: "PATCH",
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  })
export const listMySessions = () =>
  api<{ sessions?: LoginSession[] }>("/users/@me/sessions").then(raw => raw.sessions ?? [])
export const revokeMySession = (sessionID: string) => api<void>(`/users/@me/sessions/${sessionID}`, { method: "DELETE" })
/** 登出所有其他设备（保留当前会话，docs 01 FR-27） */
export const revokeOtherSessions = () =>
  api<{ revoked: number }>("/users/@me/sessions", { method: "DELETE" })
/** 服务器时间（docs 08 §8-9：客户端校准时钟偏差） */
export const getServerTime = () => api<{ server_time: string; unix_ms: number }>("/time")
