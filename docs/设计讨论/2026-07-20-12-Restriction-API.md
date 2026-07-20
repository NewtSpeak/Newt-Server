# 2026-07-20 Restriction API（实现专项）

| 字段 | 内容 |
|------|------|
| **结论状态** | **已定稿** |
| **前置** | [07](./2026-07-20-07-专项1至5定稿决议.md)、[02 RBAC](./2026-07-20-02-RBAC权限位与计算规则.md)、[05 信令](./2026-07-20-05-进房信令时序.md)、[11 舞台](./2026-07-20-11-舞台状态机.md) |
| **范围** | 模型、作用域/维度、API、权限、计算叠加、展示、过期、与 mute/Ban/舞台边界 |

---

## 1. 目标

| 目标 | 说明 |
|------|------|
| 多维限制 | 文字 **看/说**、语音 **听/讲** × 单频道或全服类型 |
| 只收紧 | 叠加在 RBAC 之上，**不允许** 用 Restriction 放宽权限 |
| 可审计 | reason 默认必填；完整管理记录；当事人/他人差异展示 |
| 边界清晰 | 与 Ban Member、舞台 `CAPACITY_QUEUE`、快捷静音关系明确 |

---

## 2. 资源模型（AG）

| 编号 | 定稿 |
|------|------|
| **AG.1** | 统一资源名：**`Restriction`** |
| **AG.2** | **一条记录可含多个 deny 维** |
| **AG.3** | **长期频道 ban 与临时制裁同表**；`expires_at = null` 表示长期；`kind` 区分 |
| **AG.4** | **`Ban Member` 独立**：移出服务器并阻止再加入 |

### 2.1 记录结构

```text
Restriction {
  id: UUID
  guild_id: ID
  target_user_id: ID
  scope: TEXT_CHANNEL | VOICE_CHANNEL | GUILD_ALL_TEXT | GUILD_ALL_VOICE
  channel_id: ID?              // TEXT_CHANNEL / VOICE_CHANNEL 必填
  deny: {
    view_text: bool
    send_text: bool
    listen_voice: bool
    speak_voice: bool
  }
  kind: SANCTION | CHANNEL_BAN
  reason: string               // 当事人可见；默认必填
  expires_at: timestamp?       // null = 长期（CHANNEL_BAN 或特许）
  created_at, created_by
  lifted_at?, lifted_by?       // 提前解除
  active: bool                 // 或由 expires/lifted 推导
}
```

### 2.2 非法组合（AH.3）

| scope | 允许的 deny |
|-------|-------------|
| `TEXT_CHANNEL` / `GUILD_ALL_TEXT` | 仅 `view_text` / `send_text` |
| `VOICE_CHANNEL` / `GUILD_ALL_VOICE` | 仅 `listen_voice` / `speak_voice` |

其它组合 → **HTTP 400**。

### 2.3 蕴含规则（AH.4 / AH.5）

| 规则 | 定稿 |
|------|------|
| **禁看 ⇒ 禁发** | 设置 `view_text=true` 时 **自动** `send_text=true` |
| **禁听 ⇒ 禁说** | 设置 `listen_voice=true` 时 **自动** `speak_voice=true` |

解除时按记录存储的 flags 处理；若只解除「说」而仍「看/听」禁，允许单独存在「只禁说」。

---

## 3. 时长与重叠（AI）

| 编号 | 定稿 |
|------|------|
| **AI.1** | `reason` 最大长度 **512** |
| **AI.2** | **reason 默认必填**（系统管可配置是否强制） |
| **AI.3** | 临时限制：最短 **60s**；最长默认 **28 天**（可配）；`expires_at=null` 用于 `CHANNEL_BAN` 或高权限永久制裁 |
| **AI.4** | 多条 **允许重叠**；生效为 deny **并集**（任一禁则禁） |

`kind=CHANNEL_BAN`：通常 `expires_at=null`，针对单频道 scope。

---

## 4. HTTP API（AJ）

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/guilds/{gid}/restrictions` | 创建 |
| `GET` | `/guilds/{gid}/restrictions` | 列表；query：`user_id` `channel_id` `active` `scope` |
| `GET` | `/guilds/{gid}/restrictions/{id}` | 详情（权限不足时字段脱敏） |
| `GET` | `/guilds/{gid}/restrictions/@me` | 当事人查看自己生效中的限制 |
| `PATCH` | `/guilds/{gid}/restrictions/{id}` | 仅 `expires_at` / `reason` 等；**禁止改 scope/channel/target**（AJ.4） |
| `DELETE` | `/guilds/{gid}/restrictions/{id}` | 解除（lift） |

| 编号 | 定稿 |
|------|------|
| **AJ.5** | 首期 **不做** 批量多人创建 |

### 4.1 创建 body 示例

```json
{
  "target_user_id": "...",
  "scope": "VOICE_CHANNEL",
  "channel_id": "...",
  "deny": { "speak_voice": true },
  "kind": "SANCTION",
  "reason": "刷麦",
  "expires_at": "2026-08-01T00:00:00Z"
}
```

创建时服务端应用蕴含规则后落库。

---

## 5. 操作权限（AK）

| 角色 | 定稿 |
|------|------|
| **系统管理员** | 任意对象（**含所有者**）；**必审计** |
| **服务器管理员 / 持 `MODERATE_MEMBERS` 或细分节点者** | 本服内；**不可** 限所有者；**不可** 限 role position ≥ 自己的成员 |
| **协管（AK.2）** | 默认仅 **本语音频道** `speak_voice`（快捷禁说）；**不可** `GUILD_ALL_*`、不可默认禁文字全服；可按权限节点扩展「本文字频道禁言」 |
| **自己（AK.5）** | **禁止** 对自己创建 Restriction |

角色层级规则与 02 踢人/改角色一致。

---

## 6. 权限计算接入（AL）

### 6.1 顺序（AL.1）

```text
bits = channel_or_guild_permissions(member)   // RBAC，含管理员短路
bits = apply_restriction_union(bits, active_restrictions)
// 再投影 media caps / 文本 API 校验
```

`ADMINISTRATOR` **不绕过** Restriction（与「管理员无视频道 overwrite」不同）：  
- 频道 overwrite：管理员可进私密频排障  
- Restriction：对 **目标用户** 的制裁；若目标是普管，仍生效  
- **所有者** 默认不被服管限制；系统管可限所有者  

> 若目标拥有 ADMINISTRATOR，服管通常因层级无法限制；系统管可以。

### 6.2 各维效果

| Deny | 效果 |
|------|------|
| `view_text` | 文字频道不可见；相关 API **404**；Gateway 不推该频消息 |
| `send_text` | 不可发消息/反应等（按位再拆） |
| `listen_voice`（AL.3） | **禁止 join 语音**；已在房内则 **disconnect** |
| `speak_voice` | 可进房旁听（若未禁听）；无 `publish_audio`；舞台不可上台 |

### 6.3 实时生效（AL.5）

创建/解除/过期时：

1. 失效权限缓存  
2. 若在语音：改 caps 或 disconnect  
3. Gateway：`RESTRICTION_CREATE` / `RESTRICTION_LIFT` / `RESTRICTION_UPDATE`（命名实现可调）  
4. 当事人必推；管理订阅可选  

---

## 7. 展示与隐私（AM）

| 观众 | 定稿 |
|------|------|
| **当事人（AM.1）** | reason、expires_at、scope/维度说明 |
| **其他成员（AM.2）** | 仅「该用户被限制」类文案；**不展示 reason** |
| **有权管理者（AM.3）** | 完整记录 |
| **成员列表徽章（AM.4）** | **默认开启** 显示受限图标；服配置可关 |

---

## 8. 过期（AN）

| 编号 | 定稿 |
|------|------|
| **AN.1** | **定时扫描 + 访问时惰性过期** |
| **AN.2** | 过期推送当事人 lift 事件 |
| **AN.3** | 时钟以 **Server UTC** 为准 |

过期后 `active=false`，记录可保留供审计（保留策略可配）。

---

## 9. 与其它系统边界（AO）

| 概念 | 关系 |
|------|------|
| **快捷服务器静音（AO.1）** | UI「静音」→ 底层创建/解除 **Restriction**（通常 `VOICE_CHANNEL` + `speak_voice`，短时或会话级 expires） |
| **server_deafen** | 可用 `listen_voice` Restriction 表达（禁听⇒禁说） |
| **舞台 CAPACITY_QUEUE（AO.2）** | **不是** Restriction；source 独立（见 11） |
| **Ban Member（AO.3）** | 踢出并禁入；同时 **清理（失活）该用户本服全部 Restriction**；历史可归档 |
| **RBAC 不给 SPEAK** | 常态能力不足；Restriction 是额外惩罚层 |

---

## 10. 场景示例

### 10.1 文字频禁言 24h

```text
scope=TEXT_CHANNEL, deny.send_text=true, expires=+24h, reason=...
```

### 10.2 全服禁听语音（禁进房）

```text
scope=GUILD_ALL_VOICE, deny.listen_voice=true
→ 蕴含 speak_voice；所有语音 join 拒绝；已在房 disconnect
```

### 10.3 某语音频长期 ban（不能进）

```text
kind=CHANNEL_BAN, scope=VOICE_CHANNEL, listen_voice=true, expires_at=null
```

### 10.4 协管禁说

```text
仅当前 VOICE_CHANNEL + speak_voice；校验协管任命与节点
```

---

## 11. 错误码（逻辑）

| 码 | 含义 |
|----|------|
| `INVALID_SCOPE_DENY` | 维度与 scope 不匹配 |
| `REASON_REQUIRED` | 未填 reason |
| `DURATION_OUT_OF_RANGE` | 短于 60s 或超最长 |
| `CANNOT_RESTRICT_TARGET` | 所有者/层级/自己 |
| `CANNOT_RESTRICT_SELF` | 自限 |
| `NOT_FOUND` | 无权限看时亦可能统一 404 |

---

## 12. 观测与审计

- 创建/解除/过期次数 by scope、kind、actor 类型  
- 语音 disconnect 因 Restriction 的次数  
- 与 Ban 联动清理次数  
- 审计日志：全字段 + actor  

---

## 13. 结论汇总

| # | 结论 |
|---|------|
| 1 | 资源名 Restriction；多维单条；长期 ban 同表 |
| 2 | 禁看⇒禁发；禁听⇒禁说；禁听=禁止 join |
| 3 | reason 默认必填；临时最长默认 28 天；重叠并集 |
| 4 | 不可改 scope（重建）；首期无批量 |
| 5 | 协管默认仅本语音禁说；禁止自限 |
| 6 | RBAC 后并集收紧；404/断麦实时 |
| 7 | 他人不见 reason；徽章默认开 |
| 8 | 定时+惰性过期；快捷静音=Restriction；Ban 清理 Restriction |

---

## 14. 后续

| 项 | 说明 |
|----|------|
| **13 消息/搜索/附件** | 下一专项 |
| OpenAPI 正式稿 | 实现前落 `docs/协议` |
| 与 02 权限节点表合并 | `CREATE_RESTRICTION` 等细分可选 |

---

## 15. 变更记录

| 日期 | 变更 |
|------|------|
| 2026-07-20 | 根据 AG–AO 选项定稿 Restriction API |
