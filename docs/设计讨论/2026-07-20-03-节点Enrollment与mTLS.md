# 2026-07-20 节点 Enrollment 与 mTLS 控制通道认证

| 字段 | 内容 |
|------|------|
| **结论状态** | **已定稿（认证主线）**；服级节点池见 [06 §5](./2026-07-20-06-评审定稿决议.md) |
| **前置文档** | [总体架构](./2026-07-20-总体架构与核心约束.md)、[RBAC](./2026-07-20-02-RBAC权限位与计算规则.md) |
| **范围** | Newt-Server ↔ Newt-SFU 控制面身份、接入、吊销；**不含** 客户端登录 |

---

## 1. 目标

1. **防止**未授权进程伪造成 SFU 加入集群。
2. **防止**错误/脏节点被调度进生产流量。
3. **防止**控制指令被窃听、篡改、重放。
4. 支持 **内网与公网** 部署同一套模型。
5. 与 **短时 Media Token** 分工：节点身份管控制通道；用户能力管进房。

---

## 2. 身份模型

### 2.1 参与方

| 身份 | 说明 |
|------|------|
| **Newt-Server** | 控制面权威；持有 **集群 CA**（或接入企业 PKI） |
| **Newt-SFU Node** | 每个进程/实例一个 `node_id`；持有 **节点证书 + 私钥** |
| **运维/管理员** | 通过 Server 管理 API 审批节点、启停调度 |

### 2.2 节点记录（Server 侧）

```text
SfuNode {
  node_id: UUID                    // 全局唯一
  display_name: string
  status: enum {
    PENDING_ENROLLMENT,            // 已发 enrollment，尚未完成
    ENROLLED,                      // 证书已签发，未/已上线
    ONLINE,                        // 控制通道已连接且心跳正常
    DRAINING,                      // 排空中，不再调度新房间
    DISABLED,                      // 管理员禁用，拒绝连接
    REVOKED                        // 证书吊销
  }
  cert_fingerprint: string         // 当前证书指纹
  cert_not_after: timestamp
  labels: map                      // region, network=private|public, pool=...
  endpoints: {
    control_advertise: string      // 节点连 Server 时自报，或 Server 连节点
    webrtc_hosts: [string]         // 下发给客户端的 ICE/host 提示（可选）
  }
  capacity: {
    max_users: int                 // 配置上限，如 1000
    current_users: int             // 心跳上报
    bandwidth_out_mbps: optional
    cpu_pct / mem_pct: optional
  }
  enabled_for_scheduling: bool     // 显式开关：未打开绝不调度
  created_at / enrolled_at / last_seen_at
}
```

**关键规则：`ENROLLED` 且 `enabled_for_scheduling=true` 且 `ONLINE` 才进入调度池。**

---

## 3. 信任根与证书

### 3.1 推荐方案：私有 CA + mTLS

| 证书 | 用途 |
|------|------|
| **Cluster CA** | 仅 Server（或独立 vault）持有签发权；根/中间证书分发给 SFU 用于校验 Server |
| **Server 证书** | 控制通道服务端身份；SAN 含控制面域名/内网名 |
| **Node 证书** | 每个 SFU 一张；SAN/URI 含 `spiffe://newtspeak/sfu/<node_id>` 或 CN=`node_id` |
| **有效期** | 节点证书建议 30–90 天；支持续期；CA 更长 |

### 3.2 备选（小型自托管）

- 若暂不上全量 CA：Server 生成 **节点密钥对**，公钥入库，控制通道用 **TLS + 节点 JWT（私钥签）** 双向校验。
- **仍要求** enrollment 一次性令牌；**禁止** 全局单一 `SFU_SECRET` 多机共用作为唯一防线。

本草案 **默认采用 mTLS + 私有 CA**；实现可先做「Server 签发节点证书」的内嵌 CA。

---

## 4. Enrollment 流程（节点接入）

### 4.1 总览

```text
运维                Newt-Server                         Newt-SFU
 │                      │                                  │
 │ 1. 创建节点占位        │                                  │
 │  POST /admin/sfu/nodes                              │
 │  ← node_id + enrollment_token (一次性, 短TTL)       │
 │                      │                                  │
 │ 2. 将 token/配置安全下发到目标机器（SSH/密钥注入，非聊天传）  │
 │                      │                                  │
 │                      │     3. POST /sfu/enroll            │
 │                      │        { node_id, token, CSR }   │
 │                      │  校验 token 未用、未过期、节点 PENDING
 │                      │  签发节点证书                      │
 │                      │  ← cert + ca_bundle + 控制面地址   │
 │                      │                                  │
 │                      │     4. 建立 mTLS 长连接/流        │
 │                      │        (gRPC 或 WSS)             │
 │                      │  校验证书指纹 ∈ 节点表            │
 │                      │  status → ONLINE                 │
 │                      │                                  │
 │ 5. 管理员 enable 调度 │                                  │
 │  PATCH enabled_for_scheduling=true                     │
 │                      │                                  │
 │                      │  此后可接收 CreateRoom 等指令      │
```

### 4.2 Enrollment Token

| 属性 | 要求 |
|------|------|
| 熵 | ≥ 128 bit 随机 |
| TTL | 建议 15–60 分钟 |
| 次数 | **一次性**；成功 enroll 后立即作废 |
| 绑定 | 绑定 `node_id`；不可挪到其他 node_id |
| 存储 | Server 只存 **哈希**（如 SHA-256） |
| 传输 | 仅 TLS；运维渠道保密 |

### 4.3 CSR 与签发

- 节点本地生成私钥（不离开主机）。
- CSR 中声明 `node_id`；Server **强制** 证书身份 = 该 `node_id`（忽略 CSR 内伪造 CN 时以 Server 记录为准重写）。
- 签发后节点 `status=ENROLLED`，写入 `cert_fingerprint`。

### 4.4 重装 / 证书轮换

| 场景 | 流程 |
|------|------|
| 证书临期 | 已在线节点用 **现有 mTLS** 调用 `RenewCertificate`，签发新证，短暂双指纹窗口 |
| 私钥泄露 | 管理员 `Revoke` → 踢控制连接 → 新 enrollment token 重新接入 |
| 主机重装丢私钥 | 同泄露：必须重新 enrollment，旧证吊销 |

---

## 5. 控制通道协议要求

### 5.1 传输

| 项 | 建议 |
|----|------|
| 协议 | **gRPC over mTLS** 优先；或 **WSS + mTLS** |
| 方向 | **推荐 SFU 主动外连 Server**（便于 SFU 在 NAT 后/公网动态 IP） |
| 备选 | Server 连 SFU（需 SFU 暴露控制口 + 防火墙白名单） |
| 心跳 | 15–30s；超时 → `ONLINE` 降级为断线，移出调度 |
| 重放防护 | 指令带 `command_id` + 时间窗；关键操作幂等键 |

### 5.2 双向认证

1. TLS 握手：节点校验 Server 证书链（CA）。
2. TLS 握手：Server 校验节点证书链，并查库：
   - 指纹匹配且 `status ∉ {REVOKED, DISABLED}`
   - 非 `PENDING_ENROLLMENT`
3. 应用层再绑 `node_id`（与证书一致，防证书与声明不一致）。

### 5.3 控制消息类型（逻辑）

| 方向 | 消息 | 说明 |
|------|------|------|
| SFU → Server | `Register` / `Heartbeat` | 容量、版本、区域 |
| SFU → Server | `RoomEvent` | 用户加入/离开媒体、异常 |
| Server → SFU | `CreateRoom` / `CloseRoom` | 编排 |
| Server → SFU | `GrantSession` 元数据（可选） | 多数情况仅客户端持 token |
| Server → SFU | `RevokeSession` / `DisconnectUser` | 踢人、权限变更 |
| Server → SFU | `UpdateParticipantCaps` | 关麦、禁发 |
| Server → SFU | `Drain` | 排空节点 |

**原则：媒体路径永不走该通道；通道只传控制面 JSON/protobuf。**

### 5.4 公网部署加固

- 控制口与 WebRTC 媒体口 **分离端口**。
- 控制面：mTLS 必须；可选 IP allowlist。
- 管理 API（创建节点/enable）：仅内网或 SSO 管理员。
- 审计日志：enrollment、enable、revoke、drain 全记。

---

## 6. Media Token 与节点认证的关系

| 凭证 | 签发者 | 校验者 | 生命周期 | 证明什么 |
|------|--------|--------|----------|----------|
| **节点证书** | Server CA | Server（控制通道） | 长（天～月） | 「我是合法 SFU 节点 X」 |
| **Media Token** | Server | SFU（用户进房） | 短（分钟级，可刷新） | 「用户 U 可在节点 X 的 room R 做 caps」 |

Media Token 建议内容：

```text
{
  "v": 1,
  "uid": "...",
  "gid": "...",          // guild
  "cid": "...",          // channel
  "nid": "...",          // node_id，必须与当前 SFU 一致
  "rid": "...",          // room_id
  "caps": ["join", "publish_audio", "subscribe_audio"],
  "iat": ...,
  "exp": ...,
  "jti": "..."           // 可吊销列表/布隆，短 TTL 时可仅靠 exp + RevokeSession
}
```

签名：HMAC（Server/SFU 共享密钥按节点或全局轮换）或 **非对称**（Server 私钥签，SFU 持公钥）。  
**推荐非对称**：避免所有 SFU 共享同一可签发密钥。

Token 中 `nid` 必须等于本机 `node_id`，防止 token 拿到其他节点重放。

---

## 7. 错误添加与入侵的防护清单

| 风险 | 缓解 |
|------|------|
| 扫描到开放端口伪造节点 | 无有效客户端证书无法完成 mTLS |
| 偷到 enrollment token | 短 TTL + 一次性 + 仅绑定 node_id；泄露则 revoke 占位 |
| 偷到节点私钥 | 吊销证书 + 主机轮换；私钥文件权限 600、可选 HSM/KMS |
| 管理员误 enable 测试节点 | 分环境 labels + 调度池隔离 + 二次确认 |
| 中间人 | mTLS；证书钉扎可选 |
| 客户端绕过 Server | SFU 只认 Media Token；无 token 拒绝 |
| 重放旧踢人指令 | command_id 幂等 + 时间戳窗口 |

---

## 8. 状态机（节点）

```text
(不存在)
    │ admin 创建 + 发 token
    ▼
PENDING_ENROLLMENT ──超时/取消──► (删除或 DISABLED)
    │ enroll 成功
    ▼
ENROLLED ──管理员 enable + 连上──► ONLINE
    │                              │
    │                              ├── drain ──► DRAINING ──► ENROLLED/ONLINE
    │                              ├── 心跳失败 ──► ENROLLED（离线）
    │                              └── disable ──► DISABLED
    │
    └── revoke ──► REVOKED（终态，需新 node 或新 enroll 策略）
```

---

## 9. 配置与密钥管理建议

| 项 | 建议 |
|----|------|
| CA 私钥 | 仅 Server/Vault；不进 SFU 镜像 |
| 节点私钥 | 本地卷或密钥挂载；不进镜像层 |
| Media 验签公钥 | 配置下发或 enroll 时下发；支持 kid 轮换 |
| 开发环境 | 可用自签一键脚本；**禁止** 与生产 CA 共用 |

---

## 10. 本轮结论

| # | 结论 |
|---|------|
| 1 | 节点接入必须 **Enrollment Token（一次、短时）→ 证书签发 → mTLS 控制通道** |
| 2 | **显式 `enabled_for_scheduling`** 后才进调度池 |
| 3 | 推荐 **SFU 主动外连 Server** 的 mTLS 长连接 |
| 4 | Media Token 与节点证书职责分离；Token 绑定 `node_id`；推荐 Server 非对称签发 |
| 5 | 吊销 / 轮换 / Drain 状态机完备 |
| 6 | 禁止「全局共享 SFU_SECRET」作为唯一安全机制 |

---

## 11. 评审定稿补充：服级节点池

| 项 | 定稿（06） |
|----|------------|
| 绑定时机 | 创建/配置「服务器」时，管控可用 SFU 节点集合 |
| 跨境 | 支持将跨境/跨区节点划入某服节点池 |
| 调度 | 该服语音 **仅** 在池内节点调度；配合级联与热迁移 |
| 系统管理员 | 可覆盖服的节点池配置 |

节点 **enrollment / mTLS / enable** 仍是进入「平台可用节点」的前提；**进服池** 是第二道调度白名单。

控制通道实现默认：**gRPC + mTLS**，SFU 主动外连；Media Token **非对称**签发（实现期可选 Ed25519）。

---

## 12. 仍待专项

1. 节点池变更时在途语音会话是否触发迁移。  
2. 级联场景下 token 的 `nid`/`rid` 与多节点关系。  
3. 热迁移指令是否走同一控制通道（预期：是）。

---

## 13. 变更记录

| 日期 | 变更 |
|------|------|
| 2026-07-20 | 初稿：enrollment、mTLS、状态机、与 media token 分工 |
| 2026-07-20 | 定稿补充：服级节点池、调度白名单 |
