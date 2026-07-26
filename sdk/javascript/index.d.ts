// OwlSpeak Bot SDK 类型声明。

export interface Message {
  id: string
  guild_id: string
  channel_id: string
  author_id: string
  author_username?: string
  author_is_bot?: boolean
  content: string
  card?: Record<string, unknown>
  stream_status?: string
  reply_to_id?: string | null
  created_at: string
  [key: string]: unknown
}

export interface VoiceJoinResult {
  token: string
  node_id: string
  room_id: string
  advertise_wss_url: string
  sfu_endpoint: string
  caps: string[]
  session_id: string
  ice_servers: unknown[]
  expires_at: number
}

/** 卡片按钮：url 与 custom_id 二选一互斥必居其一；每消息 ≤25 个按钮。 */
export interface CardButton {
  /** 按钮文字（1-40 字）。 */
  label: string
  /** 链接按钮：点击打开 URL。与 custom_id 互斥。 */
  url?: string
  /** 交互按钮：点击触发 INTERACTION_CREATE。消息内唯一，[A-Za-z0-9_\-:.] 1-64 字符。与 url 互斥。 */
  custom_id?: string
  /** 样式，缺省 "secondary"。 */
  style?: "primary" | "secondary" | "success" | "danger"
  /** 尺寸，缺省 "sm"。 */
  size?: "xs" | "sm" | "md" | "lg"
  disabled?: boolean
  /** 行号 0-4，可选。 */
  row?: number
  /** 按钮可见性白名单（users ≤20、roles ≤10）。 */
  visible_to?: { users?: string[]; roles?: string[] }
}

export interface SendMessageOptions {
  content?: string
  card?: Record<string, unknown>
  replyToId?: string
  attachmentIds?: string[]
  nonce?: string
  /** ephemeral 白名单（≤20 user_id）：仅名单用户 + bot 自己可见；不能带附件。 */
  visibleToUserIds?: string[]
}

/** INTERACTION_CREATE 载荷中的成员信息。 */
export interface InteractionMember {
  user_id: string
  username: string
  roles: string[]
}

/** 按钮点击交互（Gateway INTERACTION_CREATE 载荷包装）。 */
export class Interaction {
  /** 交互 ID（雪花）。 */
  id: string
  /** 一次性回应令牌（owlint_...）。 */
  token: string
  guildId: string
  channelId: string
  /** 按钮所在消息 ID。 */
  messageId: string
  customId: string
  member: InteractionMember
  /** 过期时间（创建 +15 分钟，ISO 字符串）。 */
  expiresAt: string
  /** 原始事件载荷。 */
  raw: Record<string, unknown>
  /** 仅确认（按钮停止转圈）；之后仍可再 reply / updateMessage 一次（defer 模式）。 */
  ack(): Promise<void>
  /** 以 bot 身份在原频道回复；ephemeral 缺省 true（仅点击者可见）。 */
  reply(
    content: string,
    options?: { card?: Record<string, unknown>; ephemeral?: boolean }
  ): Promise<Message>
  /** 更新原消息的 card 和/或 content。 */
  updateMessage(options?: { content?: string; card?: Record<string, unknown> }): Promise<Message>
}

export class OwlBotError extends Error {
  status: number
  code?: string
}

export class MessageStream {
  id: string
  message: Message
  append(delta: string): Promise<{ seq: number; content_length: number }>
  end(options?: { content?: string; card?: Record<string, unknown> }): Promise<Message>
}

export class BotGateway {
  /** 按钮点击（INTERACTION_CREATE 的 Interaction 包装）。 */
  on(event: "interaction", handler: (interaction: Interaction) => void): this
  on(event: string, handler: (data: unknown) => void): this
  close(): void
}

export class OwlBotClient {
  constructor(options: { baseUrl: string; token: string })
  me(): Promise<{ bot: Record<string, unknown>; user: Record<string, unknown> }>
  guilds(): Promise<Array<Record<string, unknown>>>
  channels(guildId: string): Promise<Array<Record<string, unknown>>>
  members(guildId: string): Promise<Array<Record<string, unknown>>>
  permissions(guildId: string): Promise<{ guild_id: string; permissions: number }>
  sendMessage(channelId: string, message?: SendMessageOptions): Promise<Message>
  sendCard(channelId: string, card: Record<string, unknown>, options?: SendMessageOptions): Promise<Message>
  /** 发送 ephemeral 消息（仅 userId 与 bot 自己可见）。 */
  sendEphemeral(
    channelId: string,
    userId: string,
    content: string,
    options?: { card?: Record<string, unknown> }
  ): Promise<Message>
  getMessages(channelId: string, options?: { before?: string; after?: string; limit?: number }): Promise<Message[]>
  editMessage(channelId: string, messageId: string, content: string): Promise<Message>
  deleteMessage(channelId: string, messageId: string): Promise<void>
  addReaction(channelId: string, messageId: string, emoji: string): Promise<void>
  removeReaction(channelId: string, messageId: string, emoji: string): Promise<void>
  typing(channelId: string): Promise<void>
  searchMessages(query: string, options?: { guildId?: string; channelId?: string; limit?: number }): Promise<Message[]>
  startStream(channelId: string, options?: { content?: string; replyToId?: string }): Promise<MessageStream>
  joinVoice(guildId: string, channelId: string, options?: { selfMute?: boolean; selfDeaf?: boolean }): Promise<VoiceJoinResult>
  leaveVoice(guildId: string): Promise<{ left: boolean }>
  refreshVoiceToken(guildId: string): Promise<{ token: string; caps: string[]; expires_at: number }>
  voiceStates(guildId: string, channelId: string): Promise<Array<Record<string, unknown>>>
  connectGateway(): BotGateway
}
