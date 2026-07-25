# OAuth2 Agent CLI 与 aud=agent

| 字段 | 内容 |
|------|------|
| 日期 | 2026-07-26 |
| 状态 | 已落地 MVP |
| 相关仓 | Owl-Server / Owl-Desktop / Owl-Agent |

## 摘要

为 CLI 与 AI Agent 增加 OAuth2 **设备码**授权，签发独立受众 `aud=agent` 的用户委托令牌。授权 UI 仅在 **Owl-Desktop / 用户 Web**（`/oauth/device`），不进入管理台。

## 端点

| 方法 | 路径 | 认证 |
|------|------|------|
| POST | `/oauth/v1/device/code` | 无 |
| GET | `/oauth/v1/device/:user_code` | 无 |
| POST | `/oauth/v1/device/approve` | Bearer aud=client |
| POST | `/oauth/v1/device/deny` | Bearer aud=client |
| POST | `/oauth/v1/authorize/approve` | Bearer aud=client（PKCE 同意 → code） |
| POST | `/oauth/v1/token` | 无（device_code / authorization_code / refresh_token） |
| POST | `/oauth/v1/revoke` | 无 |
| GET | `/oauth/v1/grants` | Bearer aud=client |
| DELETE | `/oauth/v1/grants/:sessionID` | Bearer aud=client |
| POST | `/oauth/v1/grants/revoke-all` | Bearer aud=client |
| GET | `/oauth/v1/userinfo` | Bearer aud=agent |
| GET | `/oauth/v1/.well-known/oauth-authorization-server` | 无 |

### 前端路由（Desktop / 用户 Web）

| 路径 | 用途 |
|------|------|
| `/oauth/device` | 设备码授权（可勾选缩减 scope；platform.* 默认不勾选） |
| `/oauth/authorize` | PKCE 授权（同上） |
| 深链 `owlspeak://oauth/*` | Tauri deep-link + 单实例聚焦 |
| 设置 → 已授权应用 | 列出/吊销 agent grants（scope 标签） |

### Owl-Agent 发布

- CI：`.github/workflows/ci.yml`（test + 多平台 artifact）
- Release：tag `v*` → `.github/workflows/release.yml`

## Scope

`openid` `profile` `offline_access` `gapi.full` `gapi.read` `gapi.guilds.manage` `platform.read` `platform.admin`

- `gapi.*` → `/gapi/v1`（仍过 RBAC）
- `platform.*` → `/api/v1` 且用户必须 `system_admin`

## 配置

- `PUBLIC_CLIENT_ORIGIN`：授权页绝对 URL 基址（verification_uri）
- `PUBLIC_API_BASE`：discovery 文档 issuer

## 客户端

- Desktop 路由：`/oauth/device`
- CLI：`Owl-Agent` 仓 `owl login --server …`
