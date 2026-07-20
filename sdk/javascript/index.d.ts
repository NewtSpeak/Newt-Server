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

export interface SendMessageOptions {
  content?: string
  card?: Record<string, unknown>
  replyToId?: string
  attachmentIds?: string[]
  nonce?: string
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
