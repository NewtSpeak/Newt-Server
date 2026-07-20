# Owl 协议（M0 定稿）

| 协议 | 载体 | 定义位置 |
|------|------|----------|
| 控制通道（Server ↔ SFU） | gRPC over mTLS，SFU 主动外连，一条双向流 | [`proto/owlsfu/v1/control.proto`](../../proto/owlsfu/v1/control.proto) |
| Enrollment（节点接入） | gRPC over TLS（首次）/ mTLS（续期） | [`proto/owlsfu/v1/enrollment.proto`](../../proto/owlsfu/v1/enrollment.proto) |
| Media Token | Ed25519 签名 JWT | 本文 §1 |
| 客户端信令（Client ↔ SFU） | WSS + JSON | 本文 §2 |
| 级联信令（SFU ↔ SFU）+ Cascade Token | mTLS TCP + JSON 帧；Ed25519 JWT | [级联信令.md](./级联信令.md) |
| 热迁移（状态机 + Gateway 事件 + MigrateParticipants） | gRPC 指令 + Gateway JSON 事件 | [热迁移.md](./热迁移.md) |
| 服务器外观资产（图标 + 多 Banner REST API + GUILD_UPDATE 载荷） | REST + Gateway JSON 事件 | [服务器外观资产.md](./服务器外观资产.md) |

代码生成：仓库根 `buf generate --template buf.gen.server.yaml`（→ `backend/gen/`）与 `--template buf.gen.sfu.yaml`（→ `../Owl-SFU/gen/`）。

---

## 1. Media Token（03 §6 + 15 BJ）

- 算法：**EdDSA (Ed25519)**；JWT header 含 `kid`（支持轮换，公钥经 Enroll/RegisterAck 下发）
- TTL：**2–5 分钟**，过期前经 Server `POST /voice/refresh-token` 刷新（05 §4）
- SFU 校验：签名、`exp`、`nid == 本机 node_id`、`rid`、caps；失败原因码见 §2.4

### Claims

```json
{
  "v": 1,
  "uid": "<user_id>",
  "gid": "<guild_id>",
  "cid": "<channel_id>",
  "nid": "<node_id>",
  "rid": "<logical_room_id>",
  "sid": "<voice_session_id>",
  "caps": ["join", "publish_audio", "subscribe_audio"],
  "iat": 0, "exp": 0, "jti": "<uuid>"
}
```

caps 字符串 ↔ proto `Cap` 枚举映射：`join`=CAP_JOIN、`publish_audio`=CAP_PUBLISH_AUDIO、`subscribe_audio`=CAP_SUBSCRIBE_AUDIO、`publish_screen`=CAP_PUBLISH_SCREEN（预留）。

**SFU 侧会话键 = `sid`**（迁移期同一 uid 可在新旧节点各持一会话）。

---

## 2. 客户端信令（WSS，15 BF）

- 端点：`wss://<node>/ws`；RTT 探测 `GET /rtt`（无鉴权、限速）
- 连接后 **5s 内必须发 `auth` 首帧**（token 不放 URL），否则关闭
- 帧格式：`{"op": "<string>", "d": { ... }}`，UTF-8 JSON 文本帧

### 2.1 Client → SFU

| op | d | 说明 |
|----|---|------|
| `auth` | `{ token }` | 首帧；Media Token |
| `offer` | `{ sdp }` | 客户端侧 SDP offer（含上行音频轨） |
| `answer` | `{ sdp }` | 应答 SFU 发起的 renegotiation |
| `ice` | `{ candidate, sdp_mid, sdp_mline_index }` | trickle ICE |
| `subscribe` | `{ user_id, kinds? }` | 订阅某发布者（默认进房自动全订本房已授权轨） |
| `unsubscribe` | `{ user_id, kinds? }` | 静音 = 退订，真实停转发（08 D.5） |
| `ping` | `{}` | 心跳（建议 15s） |

#### 订阅粒度：`kinds` 字段（按轨类型退订，向后兼容）

- `kinds` 可选，取值数组 `["audio","video"]` 的非空子集；**缺省 = 全部轨类型**（旧客户端行为完全不变，协议零破坏）。全部取值未知的帧被忽略
- 维度语义：`audio` 作用于发布者的音频轨；`video` 作用于屏幕轨 **及其系统音频伴轨**（伴轨跟随屏幕会话，docs 14 BA.4）
- SFU 侧订阅模型为 **per-(subscriber, publisher, kind) 转发开关**：`unsubscribe {user_id, kinds:["video"]}` 只停该发布者的视频转发、音频不受影响；再 `subscribe {user_id, kinds:["video"]}` 恢复（下行轨保留、零协商恢复，恢复时 SFU 主动向发布者请求关键帧）
- 退订状态持久在会话上：先 `unsubscribe kinds:["video"]`、发布者后开播的场景，新轨发布时按维度检查，不会误转发
- **进房默认值不变**：SFU 仍默认全订（含视频轨）。「视频不点观看不拉流」由客户端主动剪枝实现：`ready` 后立即对快照内全部参与者、以及此后每个 `participant_joined` 的新人发 `unsubscribe {user_id, kinds:["video"]}`；点观看 → `subscribe kinds:["video"]`，停止观看 → `unsubscribe kinds:["video"]`。本地静音（audio 维度）与视频观看（video 维度）互相独立、可叠加
- 级联（多节点同房）：跨节点屏幕轨需求（NodeWant）按 video 维度聚合——本节点全部成员都退订了某发布者的视频时，该屏幕轨不跨节点拉流（08 D.4/D.5）；音频需求按 audio 维度聚合，两者独立剪枝

### 2.2 SFU → Client

| op | d | 说明 |
|----|---|------|
| `ready` | `{ session_id, room_id, participants: [{user_id, session_id, publishing}] }` | auth 通过后下发房间快照 |
| `answer` | `{ sdp }` | 应答客户端 offer |
| `offer` | `{ sdp }` | SFU 发起 renegotiation（新增/移除下行轨） |
| `ice` | `{ candidate, ... }` | trickle ICE |
| `participant_joined` / `participant_left` | `{ user_id, session_id }` | 房内成员变化 |
| `track_published` / `track_ended` | `{ user_id, kind }` | 发布状态（kind ∈ `audio`\|`screen`\|`screen_audio`；观看端据此感知屏幕共享上下线） |
| `caps_updated` | `{ caps: [...] }` | 本会话 caps 变更（如被禁言） |
| `speaking` | `{ user_ids: [...] }` | RFC 6464 audio level 聚合，~250ms 节流 |
| `pong` | `{}` | |
| `closed` | `{ code, message }` | 关闭前告知原因 |

### 2.3 协商约定

1. 客户端 `auth` → SFU `ready` → 客户端发 `offer`（含上行音频）→ SFU `answer` → ICE → 媒体连通，SFU 经控制通道上报 `PARTICIPANT_JOINED`
2. 房内他人发布/退订变化 → **SFU 发起 `offer`**，客户端回 `answer`
3. token 刷新：新 token 经 `auth` 帧重发即可（在位更新，不断媒体）
4. **无 `publish_audio` 的上行音频轨被挂起接纳**（M5，docs 11 AD.4）：SFU 收轨但不广播 `track_published`、不转发、不计 speaking；`UpdateParticipantCaps` 授予 cap 后立即对外发布（舞台抱上 → 可发声 <1s，无需客户端重新 offer）。视频（屏幕）轨维持原语义：无 `publish_screen` 经 renegotiation 剥离，恢复须客户端重新发布（配合 `screen/start` 审批占坑 → `refresh-token` 取含 cap 新 token）

### 2.4 关闭码（05 §3.3）

`TOKEN_EXPIRED` / `TOKEN_INVALID` / `WRONG_NODE` / `ROOM_MISMATCH` / `CAP_DENIED` / `SESSION_REVOKED` / `NODE_DRAINING` / `AUTH_TIMEOUT`

---

## 3. 控制通道语义摘要（详见 proto 注释）

- 心跳间隔由 `RegisterAck.heartbeat_interval_ms` 下发（定稿 5000ms；Server 判死 = 连续 3 次丢失 **或** BI.3 提前判死：≥2 独立信号源（级联邻居 EdgeDown 指控 / 客户端 `POST /voice/ice-failed` 上报）+ ≥1 次心跳丢失，见 [热迁移.md](./热迁移.md)）
- 所有 `Command` 携带 `command_id`，SFU 去重窗口内幂等，处理后回 `CommandAck`
- `UpdateParticipantCaps` 为全量替换；去掉 `publish_audio` 时 SFU 立即停收上行并停止转发
- `DisconnectUser` / `RevokeSession` 生效目标 **P99 < 1s**（05 §7.4）
- 级联指令（`SetAnchorLease` / `SetCascadeEdges` / `CloseLogicalRoom` 级联收尾）与 `EdgeStatus` 上报 **M3 已实装**（语义见 [级联信令.md](./级联信令.md)）
- `MigrateParticipants`（含 `phase = MARK|EXECUTE`）与 `Drain.cancel` **M4 已实装**（语义见 [热迁移.md](./热迁移.md)）；`VOICE_MIGRATING` / `VOICE_SERVER_UPDATE` / `VOICE_MIGRATED` Gateway 事件载荷同文档
