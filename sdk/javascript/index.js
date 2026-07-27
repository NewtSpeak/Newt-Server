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

  /** 成员目录（含 is_bot、is_owner、role_ids）。 */
  async members(guildId) {
    return (await this.request("GET", `/guilds/${guildId}/members`)).members ?? []
  }

  /**
   * 单成员详情；memberId 可为 members.id 或 user_id
   *（MESSAGE_REACTION_* 事件载荷给的是 user_id）。
   */
  member(guildId, memberId) {
    return this.request("GET", `/guilds/${guildId}/members/${memberId}`)
  }

  /** 机器人在该服的最终权限位。 */
  permissions(guildId) {
    return this.request("GET", `/guilds/${guildId}/permissions/@me`)
  }

  /** 机器人在指定频道的最终权限投影。 */
  channelPermissions(guildId, channelId) {
    return this.request("GET", `/guilds/${guildId}/channels/${channelId}/permissions/@me`)
  }

  // ---------- 角色与成员角色（反应角色 / 验证门） ----------

  /** 列出服务器全部角色（成员即可读）。 */
  roles(guildId) {
    return this.request("GET", `/guilds/${guildId}/roles`)
  }

  /** 单角色详情。 */
  role(guildId, roleId) {
    return this.request("GET", `/guilds/${guildId}/roles/${roleId}`)
  }

  /**
   * 创建角色（需 MANAGE_ROLES；权限位与层级不可超过自身）。
   * @param {string} guildId
   * @param {{ name: string, permissions?: number, position?: number, color?: string, hoist?: boolean, mentionable?: boolean }} role
   */
  createRole(guildId, { name, permissions = 0, position = 1, color, hoist, mentionable }) {
    return this.request("POST", `/guilds/${guildId}/roles`, {
      name,
      permissions,
      position,
      color,
      hoist,
      mentionable,
    })
  }

  /**
   * 更新角色（需 MANAGE_ROLES + 层级）。
   * @param {string} guildId
   * @param {string} roleId
   * @param {{ name: string, permissions?: number, position?: number, color?: string, hoist?: boolean, mentionable?: boolean }} role
   */
  updateRole(guildId, roleId, { name, permissions = 0, position = 1, color, hoist, mentionable }) {
    return this.request("PATCH", `/guilds/${guildId}/roles/${roleId}`, {
      name,
      permissions,
      position,
      color,
      hoist,
      mentionable,
    })
  }

  /** 删除角色（需 MANAGE_ROLES；@everyone / managed 不可删）。 */
  deleteRole(guildId, roleId) {
    return this.request("DELETE", `/guilds/${guildId}/roles/${roleId}`)
  }

  /**
   * 给成员绑定角色（需 MANAGE_ROLES + 双重层级校验）。
   * memberId 接受 members.id 或 user_id。
   */
  addMemberRole(guildId, memberId, roleId) {
    return this.request("PUT", `/guilds/${guildId}/members/${memberId}/roles/${roleId}`)
  }

  /** 移除成员角色。 */
  removeMemberRole(guildId, memberId, roleId) {
    return this.request("DELETE", `/guilds/${guildId}/members/${memberId}/roles/${roleId}`)
  }

  // ---------- 频道权限覆盖（验证门搭建） ----------

  /** 列出频道覆盖（需 MANAGE_ROLES）。 */
  overwrites(guildId, channelId) {
    return this.request("GET", `/guilds/${guildId}/channels/${channelId}/overwrites`)
  }

  /**
   * 创建/更新频道覆盖（需 MANAGE_ROLES）。
   * @param {string} guildId
   * @param {string} channelId
   * @param {string} targetId 角色 ID 或成员 ID
   * @param {{ type: "ROLE"|"MEMBER", allow?: number, deny?: number }} overwrite
   */
  setOverwrite(guildId, channelId, targetId, { type, allow = 0, deny = 0 }) {
    return this.request("PUT", `/guilds/${guildId}/channels/${channelId}/overwrites/${targetId}`, {
      type,
      allow,
      deny,
    })
  }

  /** 删除频道覆盖；type 可选 ROLE|MEMBER。 */
  deleteOverwrite(guildId, channelId, targetId, { type } = {}) {
    const params = new URLSearchParams()
    if (type) params.set("type", type)
    const query = params.toString()
    return this.request(
      "DELETE",
      `/guilds/${guildId}/channels/${channelId}/overwrites/${targetId}${query ? `?${query}` : ""}`
    )
  }

  // ---------- 服务器 / 频道结构（Discord Guild + Channel） ----------

  /** 服务器详情（含 member_count）。 */
  guild(guildId) {
    return this.request("GET", `/guilds/${guildId}`)
  }

  /** 修改服务器（需 MANAGE_GUILD）。 */
  updateGuild(guildId, body) {
    return this.request("PATCH", `/guilds/${guildId}`, body)
  }

  /** 单频道详情。 */
  channel(channelId) {
    return this.request("GET", `/channels/${channelId}`)
  }

  /** 创建频道（需 MANAGE_CHANNELS）。 */
  createChannel(guildId, body) {
    return this.request("POST", `/guilds/${guildId}/channels`, body)
  }

  /** 修改频道。 */
  updateChannel(channelId, body) {
    return this.request("PATCH", `/channels/${channelId}`, body)
  }

  /** 删除频道。 */
  deleteChannel(channelId) {
    return this.request("DELETE", `/channels/${channelId}`)
  }

  // ---------- 成员治理（Discord Member / Ban / Invite） ----------

  /**
   * 踢出成员（需 KICK_MEMBERS）；memberId=@me 时为 bot 主动退服。
   */
  kickMember(guildId, memberId) {
    return this.request("DELETE", `/guilds/${guildId}/members/${memberId}`)
  }

  /** bot 主动离开服务器。 */
  leaveGuild(guildId) {
    return this.kickMember(guildId, "@me")
  }

  /** 修改昵称（本人 CHANGE_NICKNAME 或 MANAGE_NICKNAMES）。 */
  updateMember(guildId, memberId, body) {
    return this.request("PATCH", `/guilds/${guildId}/members/${memberId}`, body)
  }

  /** 封禁用户（需 BAN_MEMBERS）。 */
  banUser(guildId, userId, { reason } = {}) {
    return this.request("PUT", `/guilds/${guildId}/bans/${userId}`, reason !== undefined ? { reason } : {})
  }

  /** 解除封禁。 */
  unbanUser(guildId, userId) {
    return this.request("DELETE", `/guilds/${guildId}/bans/${userId}`)
  }

  /** 封禁列表。 */
  bans(guildId) {
    return this.request("GET", `/guilds/${guildId}/bans`)
  }

  /** 创建邀请（需 CREATE_INSTANT_INVITE）。 */
  createInvite(guildId, { ttlSeconds, maxUses } = {}) {
    return this.request("POST", `/guilds/${guildId}/invites`, {
      ttl_seconds: ttlSeconds,
      max_uses: maxUses,
    })
  }

  /** 本服邀请列表。 */
  invites(guildId) {
    return this.request("GET", `/guilds/${guildId}/invites`)
  }

  /** 撤销邀请。 */
  deleteInvite(code) {
    return this.request("DELETE", `/invites/${code}`)
  }

  // ---------- Restriction（Discord Timeout） ----------

  createRestriction(guildId, body) {
    return this.request("POST", `/guilds/${guildId}/restrictions`, body)
  }

  restrictions(guildId, query = {}) {
    const params = new URLSearchParams()
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null) params.set(k, String(v))
    }
    const q = params.toString()
    return this.request("GET", `/guilds/${guildId}/restrictions${q ? `?${q}` : ""}`)
  }

  restriction(guildId, restrictionId) {
    return this.request("GET", `/guilds/${guildId}/restrictions/${restrictionId}`)
  }

  updateRestriction(guildId, restrictionId, body) {
    return this.request("PATCH", `/guilds/${guildId}/restrictions/${restrictionId}`, body)
  }

  liftRestriction(guildId, restrictionId) {
    return this.request("DELETE", `/guilds/${guildId}/restrictions/${restrictionId}`)
  }

  // ---------- 审计日志 ----------

  auditLogs(guildId, query = {}) {
    const params = new URLSearchParams()
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null) params.set(k, String(v))
    }
    const q = params.toString()
    return this.request("GET", `/guilds/${guildId}/audit-logs${q ? `?${q}` : ""}`)
  }

  // ---------- 语音管理（Discord Voice State 管理） ----------

  /** 管理员将用户踢出语音。 */
  voiceDisconnect(guildId, body) {
    return this.request("POST", `/guilds/${guildId}/voice/disconnect`, body)
  }

  /** 移动成员到另一语音频道（需 MOVE_MEMBERS）。 */
  voiceMove(guildId, body) {
    return this.request("POST", `/guilds/${guildId}/voice/move`, body)
  }

  /** 服务器静音/闭听他人（需 MUTE_MEMBERS / DEAFEN_MEMBERS）。 */
  updateVoiceState(guildId, userId, body) {
    return this.request("PATCH", `/guilds/${guildId}/voice/states/${userId}`, body)
  }

  // ---------- 舞台 / 屏幕共享 ----------

  getVoiceStage(channelId) {
    return this.request("GET", `/channels/${channelId}/voice-stage`)
  }

  patchVoiceStage(channelId, body) {
    return this.request("PATCH", `/channels/${channelId}/voice-stage`, body)
  }

  stageApply(channelId) {
    return this.request("POST", `/channels/${channelId}/stage/apply`)
  }

  stageCancelApply(channelId) {
    return this.request("DELETE", `/channels/${channelId}/stage/apply`)
  }

  stageBringUp(channelId, body) {
    return this.request("POST", `/channels/${channelId}/stage/bring-up`, body)
  }

  stageBringDown(channelId, body) {
    return this.request("POST", `/channels/${channelId}/stage/bring-down`, body)
  }

  stageSelfLeave(channelId) {
    return this.request("POST", `/channels/${channelId}/stage/self-leave`)
  }

  // ---------- 消息 ----------

  /**
   * 发送消息。
   * @param {string} channelId
   * @param {{ content?: string, card?: object, replyToId?: string, attachmentIds?: string[], nonce?: string, visibleToUserIds?: string[] }} message
   *   visibleToUserIds：ephemeral 白名单（≤20 个 user_id）；带此字段即仅名单用户 + bot 自己可见，且不能带附件。
   */
  sendMessage(channelId, { content, card, replyToId, attachmentIds, nonce, visibleToUserIds } = {}) {
    return this.request("POST", `/channels/${channelId}/messages`, {
      content,
      card,
      reply_to_id: replyToId,
      attachment_ids: attachmentIds,
      nonce: nonce ?? crypto.randomUUID(),
      visible_to_user_ids: visibleToUserIds,
    })
  }

  /** 发送卡片消息（语法糖）。 */
  sendCard(channelId, card, options = {}) {
    return this.sendMessage(channelId, { ...options, card })
  }

  /**
   * 发送 ephemeral 消息（语法糖）：仅 userId 与 bot 自己可见。
   * @param {string} channelId
   * @param {string} userId
   * @param {string} content
   * @param {{ card?: object }} [options]
   */
  sendEphemeral(channelId, userId, content, { card } = {}) {
    return this.sendMessage(channelId, { content, card, visibleToUserIds: [userId] })
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

  /** 单条消息详情。 */
  getMessage(channelId, messageId) {
    return this.request("GET", `/channels/${channelId}/messages/${messageId}`)
  }

  addReaction(channelId, messageId, emoji) {
    return this.request("PUT", `/channels/${channelId}/messages/${messageId}/reactions/${encodeURIComponent(emoji)}/@me`)
  }

  removeReaction(channelId, messageId, emoji) {
    return this.request("DELETE", `/channels/${channelId}/messages/${messageId}/reactions/${encodeURIComponent(emoji)}/@me`)
  }

  /** 列出对某消息打了指定反应的用户。 */
  async listReactionUsers(channelId, messageId, emoji) {
    const raw = await this.request(
      "GET",
      `/channels/${channelId}/messages/${messageId}/reactions/${encodeURIComponent(emoji)}`
    )
    return raw.users ?? []
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
   *   gw.on("interaction", interaction => { ... })  // 按钮点击（Interaction 包装）
   *   gw.on("ready", data => { ... })
   */
  connectGateway() {
    return new BotGateway(this.apiBase, this.token, this)
  }
}

/**
 * 按钮点击交互（Gateway INTERACTION_CREATE 载荷包装）。
 * 通过 gw.on("interaction", handler) 获取；15 分钟内需用一次性 token 回应：
 *   ack() 仅确认（按钮停止转圈）；之后仍可再 reply/updateMessage 一次（defer 模式）
 *   reply() 以 bot 身份在原频道发新消息，默认 ephemeral（仅点击者可见）
 *   updateMessage() 更新原消息的 card 和/或 content
 * 服务端错误：404（token 不符/非本 bot）、410 INTERACTION_EXPIRED、409 ALREADY_RESPONDED。
 */
export class Interaction {
  constructor(client, payload) {
    this.client = client
    this.raw = payload
    this.id = payload.id
    this.token = payload.token
    this.guildId = payload.guild_id
    this.channelId = payload.channel_id
    this.messageId = payload.message_id
    this.customId = payload.custom_id
    this.member = payload.member
    this.expiresAt = payload.expires_at
  }

  /** 仅确认，让用户按钮停止转圈；之后仍可再 reply / updateMessage 一次。 */
  ack() {
    return this.client.request("POST", `/interactions/${this.id}/callback`, {
      token: this.token,
      type: "ack",
    })
  }

  /**
   * 以 bot 身份在原频道回复；ephemeral 缺省 true（仅点击者可见）。返回新消息对象。
   * @param {string} content
   * @param {{ card?: object, ephemeral?: boolean }} [options]
   */
  reply(content, { card, ephemeral } = {}) {
    return this.client.request("POST", `/interactions/${this.id}/callback`, {
      token: this.token,
      type: "reply",
      content,
      card,
      ephemeral: ephemeral ?? true,
    })
  }

  /**
   * 更新原消息的 card 和/或 content。
   * @param {{ content?: string, card?: object }} [options]
   */
  updateMessage({ content, card } = {}) {
    return this.client.request("POST", `/interactions/${this.id}/callback`, {
      token: this.token,
      type: "update_message",
      content,
      card,
    })
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
  constructor(apiBase, token, client) {
    this.wsUrl = apiBase.replace(/^http/, "ws") + "/gateway"
    this.token = token
    this.client = client
    this.handlers = new Map()
    this.closed = false
    this.backoffMs = 1000
    this._connect()
  }

  /** 订阅事件：事件名（如 MESSAGE_CREATE）、包装事件 "interaction" 或内置 "ready" / "close"。 */
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
          // 按钮点击：包装成 Interaction 再派发 "interaction" 事件
          if (frame.t === "INTERACTION_CREATE" && this.client) {
            this._emit("interaction", new Interaction(this.client, frame.d))
          }
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
