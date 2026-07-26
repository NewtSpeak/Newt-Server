# OwlSpeak Bot 开放平台与 SDK

OwlSpeak 为机器人提供独立的开放 API 平面（`/bot-api/v1`），配套 JavaScript / Python / Go 三种语言的官方 SDK。
机器人不需要注册用户账号、不需要操作客户端：由管理员在控制台创建机器人并签发 **bot token**，
即可独立收发消息（含卡片与流式回复）、订阅实时事件、接入语音频道的音频流。

## 快速开始

1. 管理控制台 → 「开放平台 / 机器人」→ 创建机器人；
2. 「token 管理」→ 签发 token（明文形如 `owlbot_xxx`，仅显示一次）；
3. 「安装到服务器与权限赋予」→ 选择服务器安装，并为机器人绑定角色（权限与人类成员同一套 RBAC，可随时手动调整）；
4. 用任一 SDK 或直接调 HTTP API 开始工作。

```text
认证方式：HTTP 头 Authorization: Bot <token>   （也兼容 Bearer <token>）
基础地址：https://<你的服务器>/bot-api/v1
限   流：每 bot 20 QPS（突发 40），超限返回 429
```

## HTTP API 一览

### 基础资源

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/me` | 机器人档案与用户身份 |
| GET | `/guilds` | 已安装的服务器列表 |
| GET | `/guilds/{gid}/channels` | 可见频道（受 VIEW_CHANNEL 约束） |
| GET | `/guilds/{gid}/members` | 成员目录（含 `is_bot` 标记） |
| GET | `/guilds/{gid}/permissions/@me` | 机器人在该服的最终权限位 |

### 消息（与用户端同构，另加卡片与流式）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/channels/{cid}/messages` | 发消息：`{content?, card?, reply_to_id?, attachment_ids?, nonce?, visible_to_user_ids?}`；`card` 为任意 JSON 对象（≤8KB），可发纯卡片消息；带 `visible_to_user_ids`（≤20）即 ephemeral：仅名单用户 + bot 自己可见，且不能带附件 |
| POST | `/interactions/{id}/callback` | 回应按钮点击（`{token, type: "ack"\|"reply"\|"update_message", content?, card?, ephemeral?}`，见下） |
| GET | `/channels/{cid}/messages?before=&after=&limit=` | 拉历史（需 READ_MESSAGE_HISTORY） |
| PATCH / DELETE | `/channels/{cid}/messages/{mid}` | 编辑 / 删除 |
| PUT / DELETE | `/channels/{cid}/messages/{mid}/reactions/{emoji}/@me` | 表情反应 |
| POST | `/channels/{cid}/typing` | 打字指示 |
| POST | `/channels/{cid}/attachments/presign` → PUT `upload_url` | 附件二段式上传 |
| GET | `/search/messages?q=` | 全系统搜索（按可见性过滤） |

### 流式消息（AI 生成场景的三段协议）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/channels/{cid}/messages/stream` | 开始：创建占位消息（`stream_status=STREAMING`），可带首段 `content` |
| POST | `/channels/{cid}/messages/{mid}/stream` | 追加：`{delta}`，返回 `{seq, content_length}` |
| POST | `/channels/{cid}/messages/{mid}/stream/end` | 结束：可带 `{content?}`（整体覆盖）与 `{card?}`（终态卡片） |

对应 Gateway 事件：`MESSAGE_STREAM_START`（占位消息视图）→ `MESSAGE_STREAM_DELTA`（`{id, delta, seq}` 按 seq 拼接）→ `MESSAGE_STREAM_END`（终态视图，同时补发 `MESSAGE_UPDATE` 兼容旧客户端）。
闲置超过 10 分钟未 end 的流式消息由服务端自动收束。

### 语音（独立音频权限，无需客户端）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/voice/join` | `{guild_id, channel_id}` → 返回 `{token(Media Token), advertise_wss_url, session_id, caps, ice_servers, expires_at}` |
| POST | `/voice/leave` | `{guild_id}` |
| POST | `/voice/refresh-token` | Media Token 续签（TTL 2–5 分钟，须周期续签） |
| PATCH | `/voice/state` | `{guild_id, self_mute?, self_deaf?}` |
| GET | `/guilds/{gid}/channels/{cid}/voice-states` | 频道内语音成员 |
| GET | `/voice/public-key` | Media Token 验签公钥（调试） |

机器人的 Media Token 携带 `bot=true` claim；SFU 在 `ready` 快照与 `participant_joined` 信令中会带
`is_bot: true` —— **机器人在音频流中拥有独立的用户标记**。拿到 token 后按标准信令协议连接
`advertise_wss_url`（`auth` → `ready` → `offer/answer/ice` → WebRTC Opus 收发），能否发布/订阅音频
由机器人在该服被赋予的角色权限（CONNECT / SPEAK 等）独立投影为 caps。

### Gateway 实时事件

```text
WS  wss://<服务器>/bot-api/v1/gateway
S→C HELLO {heartbeat_interval_ms}
C→S IDENTIFY {token: "<bot token>"}
S→C READY {user, guild_ids}
C→S HEARTBEAT ←→ S→C HEARTBEAT_ACK
S→C DISPATCH {t: 事件名, d: 载荷}
```

可收到的事件与用户端一致（按频道可见性过滤）：`MESSAGE_CREATE/UPDATE/DELETE`、
`MESSAGE_REACTION_ADD/REMOVE`、`TYPING_START`、`MESSAGE_STREAM_*`、`VOICE_STATE_UPDATE`、
`VOICE_CAPS_UPDATE`、`PERMISSIONS_UPDATE` 等；bot 专属事件 `INTERACTION_CREATE`
（自己消息上的按钮被点击，载荷 `{id, token, guild_id, channel_id, message_id, custom_id, member, expires_at}`）。

### 卡片（card）载荷约定

服务端只校验「JSON 对象、≤8KB」（`buttons` 字段除外，见下），渲染 schema 由客户端约定。推荐结构：

```json
{
  "title": "部署完成",
  "description": "版本 v1.4.2 已发布到生产环境",
  "color": "#22c55e",
  "fields": [{ "name": "耗时", "value": "42s", "inline": true }],
  "buttons": [
    { "label": "查看日志", "url": "https://ci.example.com/run/42" },
    { "label": "批准", "custom_id": "approve:42", "style": "success" },
    { "label": "拒绝", "custom_id": "reject:42", "style": "danger", "row": 1 }
  ],
  "footer": "CI Bot"
}
```

`card.buttons` 元素 schema（服务端校验，每消息 ≤25 个按钮；旧 `{label, url}` 兼容）：

| 字段 | 说明 |
|---|---|
| `label` | 必填，1-40 字 |
| `url` / `custom_id` | 二选一互斥、必居其一；`custom_id` 消息内唯一，字符集 `[A-Za-z0-9_\-:.]`，1-64 字符，点击触发 `INTERACTION_CREATE` |
| `style` | `primary` / `secondary`（缺省）/ `success` / `danger` |
| `size` | `xs` / `sm`（缺省）/ `md` / `lg` |
| `disabled` | bool，可选 |
| `row` | 0-4，可选 |
| `visible_to` | 可选；`{users: [uuid]≤20, roles: [uuid]≤10}` 按钮级可见性 |

### 按钮交互与 ephemeral（Owl 扩展）

用户点击 `custom_id` 按钮后，bot 的 Gateway 连接收到 `INTERACTION_CREATE`，须在 15 分钟内
携带载荷中的一次性 `token` 调 `POST /interactions/{id}/callback` 回应：
`ack`（仅确认，之后仍可再 reply/update 一次）、`reply`（原频道发新消息，`ephemeral` 缺省 true 仅点击者可见）
或 `update_message`（更新原消息 card/content）。错误：404（token 不符/非本 bot）、
410 `INTERACTION_EXPIRED`、409 `ALREADY_RESPONDED`。

```js
// JavaScript SDK
await bot.sendCard(channelId, {
  title: "发布 v1.4.2？",
  buttons: [{ label: "批准", custom_id: "approve:42", style: "success" }],
})
await bot.sendEphemeral(channelId, userId, "只有你能看到这条提示")

const gw = bot.connectGateway()
gw.on("interaction", async (interaction) => {
  await interaction.updateMessage({ card: { title: "已批准 ✅" } })
})
```

完整协议（含各语言等价 API）见 OwlBotSdk 仓库 `docs/API.md` 的 Interactions 章节。

## 管理 API（后台 `/api/v1`，管理员 JWT）

`POST/GET/PATCH/DELETE /bots`、`POST/GET/DELETE /bots/{id}/tokens`、
`GET/PUT/DELETE /guilds/{gid}/bots/{botID}`（安装/卸载，需 `MANAGE_BOTS` 权限位）。
权限赋予复用成员角色端点：`PUT /guilds/{gid}/members/{memberID}/roles/{roleID}`。

## SDK

| 语言 | 目录 | 说明 |
|---|---|---|
| JavaScript / TypeScript | [`javascript/`](javascript/) | 零依赖（Node ≥ 21 原生 fetch/WebSocket），支持流式与 Gateway |
| Python | [`python/`](python/) | 标准库实现 REST；Gateway 需可选依赖 `websockets` |
| Go | [`go/`](go/) | 独立 module，Gateway 基于 gorilla/websocket；可配合 pion 接入语音媒体 |

各目录内含完整 README 与示例。
