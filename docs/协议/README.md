# Owl 协议（M0 定稿）

| 协议 | 载体 | 定义位置 |
|------|------|----------|
| 控制通道（Server ↔ SFU） | gRPC over mTLS，SFU 主动外连，一条双向流 | [`proto/owlsfu/v1/control.proto`](../../proto/owlsfu/v1/control.proto) |
| Enrollment（节点接入） | gRPC over TLS（首次）/ mTLS（续期） | [`proto/owlsfu/v1/enrollment.proto`](../../proto/owlsfu/v1/enrollment.proto) |
| Media Token | Ed25519 签名 JWT | 本文 §1 |
| 客户端信令（Client ↔ SFU） | WSS + JSON | 本文 §2 |
| 级联信令（SFU ↔ SFU）+ Cascade Token | mTLS TCP + JSON 帧；Ed25519 JWT | [级联信令.md](./级联信令.md) |
| 热迁移（状态机 + Gateway 事件 + MigrateParticipants） | gRPC 指令 + Gateway JSON 事件 | [热迁移.md](./热迁移.md) |

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
| `subscribe` | `{ user_id }` | 订阅某发布者（默认进房自动全订本房已授权轨） |
| `unsubscribe` | `{ user_id }` | 静音 = 退订，真实停转发（08 D.5） |
| `ping` | `{}` | 心跳（建议 15s） |

### 2.2 SFU → Client

| op | d | 说明 |
|----|---|------|
| `ready` | `{ session_id, room_id, participants: [{user_id, session_id, publishing}] }` | auth 通过后下发房间快照 |
| `answer` | `{ sdp }` | 应答客户端 offer |
| `offer` | `{ sdp }` | SFU 发起 renegotiation（新增/移除下行轨） |
| `ice` | `{ candidate, ... }` | trickle ICE |
| `participant_joined` / `participant_left` | `{ user_id, session_id }` | 房内成员变化 |
| `track_published` / `track_ended` | `{ user_id, kind }` | 发布状态 |
| `caps_updated` | `{ caps: [...] }` | 本会话 caps 变更（如被禁言） |
| `speaking` | `{ user_ids: [...] }` | RFC 6464 audio level 聚合，~250ms 节流 |
| `pong` | `{}` | |
| `closed` | `{ code, message }` | 关闭前告知原因 |

### 2.3 协商约定

1. 客户端 `auth` → SFU `ready` → 客户端发 `offer`（含上行音频）→ SFU `answer` → ICE → 媒体连通，SFU 经控制通道上报 `PARTICIPANT_JOINED`
2. 房内他人发布/退订变化 → **SFU 发起 `offer`**，客户端回 `answer`
3. token 刷新：新 token 经 `auth` 帧重发即可（在位更新，不断媒体）

### 2.4 关闭码（05 §3.3）

`TOKEN_EXPIRED` / `TOKEN_INVALID` / `WRONG_NODE` / `ROOM_MISMATCH` / `CAP_DENIED` / `SESSION_REVOKED` / `NODE_DRAINING` / `AUTH_TIMEOUT`

---

## 3. 控制通道语义摘要（详见 proto 注释）

- 心跳间隔由 `RegisterAck.heartbeat_interval_ms` 下发（定稿 5000ms；Server 判死 = 连续 3 次丢失 + 提前信号，15 BI）
- 所有 `Command` 携带 `command_id`，SFU 去重窗口内幂等，处理后回 `CommandAck`
- `UpdateParticipantCaps` 为全量替换；去掉 `publish_audio` 时 SFU 立即停收上行并停止转发
- `DisconnectUser` / `RevokeSession` 生效目标 **P99 < 1s**（05 §7.4）
- 级联指令（`SetAnchorLease` / `SetCascadeEdges` / `CloseLogicalRoom` 级联收尾）与 `EdgeStatus` 上报 **M3 已实装**（语义见 [级联信令.md](./级联信令.md)）
- `MigrateParticipants`（含 `phase = MARK|EXECUTE`）与 `Drain.cancel` **M4 已实装**（语义见 [热迁移.md](./热迁移.md)）；`VOICE_MIGRATING` / `VOICE_SERVER_UPDATE` / `VOICE_MIGRATED` Gateway 事件载荷同文档
