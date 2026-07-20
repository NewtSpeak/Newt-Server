// OwlSpeak Bot SDK（JavaScript，零依赖；需要 Node >= 21 的原生 fetch 与 WebSocket）。
// 认证：Authorization: Bot <token>；基础地址默认 /bot-api/v1。

/** SDK 统一错误：携带 HTTP 状态码与服务端错误码。 */
export class OwlBotError extends Error {
  constructor(status, code, message) {
    super(message || code || `请求失败（${status}）`)
    this.name = "OwlBotError"
    this.status = status
    this.code = code
  }
}

export class OwlBotClient {
  /**
   * @param {{ baseUrl: string, token: string }} options
   *   baseUrl 形如 https://owl.example.com（自动拼接 /bot-api/v1）
   */
  constructor({ baseUrl, token }) {
    if (!baseUrl || !token) throw new Error("baseUrl 与 token 必填")
    this.apiBase = baseUrl.replace(/\/+$/, "") + "/bot-api/v1"
    this.token = token
  }

  async request(method, path, body) {
    const response = await fetch(this.apiBase + path, {
      method,
      headers: {
        Authorization: `Bot ${this.token}`,
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
    if (response.status === 204) return undefined
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
      throw new OwlBotError(response.status, data?.error?.code, data?.error?.message)
    }
    return data
  }

  // ---------- 基础资源 ----------

  /** 机器人档案与用户身份。 */
  me() {
    return this.request("GET", "/me")
  }

  /** 已安装的服务器列表。 */
  async guilds() {
    return (await this.request("GET", "/guilds")).guilds ?? []
  }

  /** 可见频道列表。 */
  async channels(guildId) {
    return (await this.request("GET", `/guilds/${guildId}/channels`)).channels ?? []
  }

  /** 成员目录（含 is_bot 标记）。 */
  async members(guildId) {
    return (await this.request("GET", `/guilds/${guildId}/members`)).members ?? []
  }

  /** 机器人在该服的最终权限位。 */
  permissions(guildId) {
    return this.request("GET", `/guilds/${guildId}/permissions/@me`)
  }

  // ---------- 消息 ----------

  /**
   * 发送消息。
   * @param {string} channelId
   * @param {{ content?: string, card?: object, replyToId?: string, attachmentIds?: string[], nonce?: string }} message
   */
  sendMessage(channelId, { content, card, replyToId, attachmentIds, nonce } = {}) {
    return this.request("POST", `/channels/${channelId}/messages`, {
      content,
      card,
      reply_to_id: replyToId,
      attachment_ids: attachmentIds,
      nonce: nonce ?? crypto.randomUUID(),
    })
  }

  /** 发送卡片消息（语法糖）。 */
  sendCard(channelId, card, options = {}) {
    return this.sendMessage(channelId, { ...options, card })
  }

  getMessages(channelId, { before, after, limit } = {}) {
    const params = new URLSearchParams()
    if (before) params.set("before", before)
    if (after) params.set("after", after)
    if (limit) params.set("limit", String(limit))
    const query = params.toString()
    return this.request("GET", `/channels/${channelId}/messages${query ? `?${query}` : ""}`).then(
      raw => raw.messages ?? []
    )
  }

  editMessage(channelId, messageId, content) {
    return this.request("PATCH", `/channels/${channelId}/messages/${messageId}`, { content })
  }

  deleteMessage(channelId, messageId) {
    return this.request("DELETE", `/channels/${channelId}/messages/${messageId}`)
  }

  addReaction(channelId, messageId, emoji) {
    return this.request("PUT", `/channels/${channelId}/messages/${messageId}/reactions/${encodeURIComponent(emoji)}/@me`)
  }

  removeReaction(channelId, messageId, emoji) {
    return this.request("DELETE", `/channels/${channelId}/messages/${messageId}/reactions/${encodeURIComponent(emoji)}/@me`)
  }

  /** 打字指示（生成回复前提示「正在输入」）。 */
  typing(channelId) {
    return this.request("POST", `/channels/${channelId}/typing`)
  }

  /** 全系统搜索（按机器人可见性过滤）。 */
  searchMessages(query, { guildId, channelId, limit } = {}) {
    const params = new URLSearchParams({ q: query })
    if (guildId) params.set("guild_id", guildId)
    if (channelId) params.set("channel_id", channelId)
    if (limit) params.set("limit", String(limit))
    return this.request("GET", `/search/messages?${params}`).then(raw => raw.messages ?? [])
  }

  // ---------- 流式消息 ----------

  /**
   * 开始一条流式消息，返回 MessageStream：
   *   const stream = await bot.startStream(channelId)
   *   await stream.append("你好")
   *   await stream.append("，世界")
   *   await stream.end({ card: {...} })
   */
  async startStream(channelId, { content, replyToId } = {}) {
    const message = await this.request("POST", `/channels/${channelId}/messages/stream`, {
      content,
      reply_to_id: replyToId,
      nonce: crypto.randomUUID(),
    })
    return new MessageStream(this, channelId, message)
  }

  // ---------- 语音 ----------

  /**
   * 加入语音频道：返回 { token, advertise_wss_url, session_id, caps, expires_at, ... }。
   * 用返回的 Media Token 连接 SFU 信令（auth → ready → offer/answer/ice）即可收发音频；
   * token TTL 2–5 分钟，需用 refreshVoiceToken 周期续签。
   */
  joinVoice(guildId, channelId, { selfMute = false, selfDeaf = false } = {}) {
    return this.request("POST", "/voice/join", {
      guild_id: guildId,
      channel_id: channelId,
      self_mute: selfMute,
      self_deaf: selfDeaf,
    })
  }

  leaveVoice(guildId) {
    return this.request("POST", "/voice/leave", { guild_id: guildId })
  }

  refreshVoiceToken(guildId) {
    return this.request("POST", "/voice/refresh-token", { guild_id: guildId })
  }

  voiceStates(guildId, channelId) {
    return this.request("GET", `/guilds/${guildId}/channels/${channelId}/voice-states`).then(
      raw => raw.voice_states ?? []
    )
  }

  // ---------- Gateway 实时事件 ----------

  /**
   * 连接 Gateway 订阅实时事件，返回 BotGateway（EventTarget 风格）：
   *   const gw = bot.connectGateway()
   *   gw.on("MESSAGE_CREATE", message => { ... })
   *   gw.on("ready", data => { ... })
   */
  connectGateway() {
    return new BotGateway(this.apiBase, this.token)
  }
}

/** 流式消息句柄。 */
export class MessageStream {
  constructor(client, channelId, message) {
    this.client = client
    this.channelId = channelId
    this.message = message
    this.id = message.id
    this.ended = false
  }

  /** 追加增量分片。 */
  async append(delta) {
    if (this.ended) throw new Error("流式消息已结束")
    return this.client.request("POST", `/channels/${this.channelId}/messages/${this.id}/stream`, { delta })
  }

  /** 结束流式消息；可整体覆盖正文或附加终态卡片。 */
  async end({ content, card } = {}) {
    if (this.ended) return this.message
    this.ended = true
    this.message = await this.client.request(
      "POST",
      `/channels/${this.channelId}/messages/${this.id}/stream/end`,
      { content, card }
    )
    return this.message
  }
}

/** Gateway 连接：HELLO/IDENTIFY/HEARTBEAT 自动处理，断线指数退避重连。 */
export class BotGateway {
  constructor(apiBase, token) {
    this.wsUrl = apiBase.replace(/^http/, "ws") + "/gateway"
    this.token = token
    this.handlers = new Map()
    this.closed = false
    this.backoffMs = 1000
    this._connect()
  }

  /** 订阅事件：事件名（如 MESSAGE_CREATE）或内置 "ready" / "close"。 */
  on(event, handler) {
    if (!this.handlers.has(event)) this.handlers.set(event, [])
    this.handlers.get(event).push(handler)
    return this
  }

  _emit(event, data) {
    for (const handler of this.handlers.get(event) ?? []) {
      try {
        handler(data)
      } catch {
        // 用户回调异常不干扰事件循环
      }
    }
  }

  _connect() {
    if (this.closed) return
    const ws = new WebSocket(this.wsUrl)
    this.ws = ws
    let heartbeat = null

    ws.onmessage = event => {
      let frame
      try {
        frame = JSON.parse(event.data)
      } catch {
        return
      }
      switch (frame.op) {
        case "HELLO":
          ws.send(JSON.stringify({ op: "IDENTIFY", d: { token: this.token } }))
          heartbeat = setInterval(() => {
            if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ op: "HEARTBEAT" }))
          }, frame.d.heartbeat_interval_ms ?? 30000)
          break
        case "READY":
          this.backoffMs = 1000
          this._emit("ready", frame.d)
          break
        case "DISPATCH":
          this._emit(frame.t, frame.d)
          break
      }
    }
    ws.onclose = () => {
      if (heartbeat) clearInterval(heartbeat)
      this._emit("close", undefined)
      if (!this.closed) {
        setTimeout(() => this._connect(), this.backoffMs)
        this.backoffMs = Math.min(this.backoffMs * 2, 30000)
      }
    }
    ws.onerror = () => ws.close()
  }

  close() {
    this.closed = true
    this.ws?.close()
  }
}
