# Newt-Server

NewtSpeak **控制面**：业务权威状态、权限裁决、实时事件、SFU 调度与机器人开放平面。  
媒体（音频/屏幕）**不经本服务中转**，由 [Newt-SFU](https://github.com/NewtSpeak/Newt-SFU) 负责。

```text
Desktop / Web  ──REST + Gateway WS──►  Newt-Server  ──► PostgreSQL
Bot            ──/bot-api/v1────────►      │
Agent (OAuth)  ──/gapi·/api─────────►      │
                                           │
                                     gRPC mTLS :9443
                                           ▲
                                      Newt-SFU（主动外连）
```

## 功能

| 域 | 能力 |
|----|------|
| **账号与安全** | 注册/登录、JWT、限流、OAuth2（设备码 / PKCE，供 Agent） |
| **服务器 (Guild)** | 创建/加入/邀请、外观资产、默认频道、节点池 |
| **频道** | 文本 / 语音 / 分类 / 舞台；排序、覆盖权限、密码房 |
| **RBAC** | 角色、权限位、频道 Overwrite、层级；无权限频道 **404** |
| **消息** | 收发、编辑、反应、附件、搜索、已读、慢速、卡片与流式（Bot） |
| **治理** | 踢/封、Restriction、审计与可撤销操作、管理员在场 |
| **语音调度** | Media Token 签发、进房/迁移/级联编排、过载策略 |
| **社交** | 好友、隐私、私信/群、通知收件箱 |
| **贴图** | 贴图包/库、服 ban |
| **开放平台** | `/bot-api/v1` + Bot Gateway；控制台管理机器人与 token |
| **平台管理** | system_admin：用户、注册开关、SFU 节点、全站审计 |
| **管理 SPA** | 生产构建打进单一 `owl-server` 二进制 |

开发环境可 **`EMBEDDED_SFU=true`** 自动拉起本机 SFU；生产默认关闭，使用独立节点。

## 仓库结构

```text
Newt-Server/
├── backend/           # Go（Gin）主服务
│   ├── cmd/server/    # 入口
│   ├── internal/      # 域模块（auth/gateway/voice/botapi/…）
│   └── Makefile
├── frontend/          # 管理后台 SPA（React Router + Bun）
├── proto/             # SFU 控制面 protobuf（权威源）
├── docs/
│   ├── 设计讨论/      # 架构与定稿（编号越大越权威）
│   └── 协议/          # Media Token、级联、热迁移等
├── deploy/signoz/     # 可观测示例
├── docker-compose.yml # 开发用 PostgreSQL
└── sdk/               # 旧镜像；权威 SDK 见 NewtBotSdk
```

## 快速开始（开发）

```bash
# 依赖：Go、Bun、Docker、PostgreSQL
cd backend
cp .env.example .env   # 配置 DATABASE_URL、JWT_SECRET 等
docker compose -f ../docker-compose.yml up -d postgres
make dev               # 后端 Air + 前端开发服；浏览器 http://localhost:8080
```

- Swagger：`http://localhost:8080/swagger/index.html`  
- 空库首次注册账号 → 系统管理员；之后关闭公开注册  

生产构建：

```bash
cd backend && make build   # 前端进二进制 → bin/newt-server
APP_ENV=production DATABASE_URL=… JWT_SECRET=… ./bin/newt-server
```

## API 平面

| 前缀 | 调用方 | 认证 |
|------|--------|------|
| `/api/v1`、管理 SPA | 平台/服管 | 用户 JWT |
| `/gapi/v1` | Desktop、Agent | 用户 JWT / OAuth access |
| `/bot-api/v1` | 机器人 | `Authorization: Bot <token>` |
| `/oauth/v1` | Agent / 第三方 | OAuth2 |
| gRPC `:9443` | SFU | mTLS（Enrollment 后） |

## 文档

| 文档 | 说明 |
|------|------|
| [backend/README.md](./backend/README.md) | 开发启动、环境变量、OTLP |
| [docs/deploy/server.md](./docs/deploy/server.md) | **生产部署** |
| [docs/设计讨论/](./docs/设计讨论/) | 架构与产品定稿 |
| [docs/协议/](./docs/协议/) | 媒体与控制协议 |
| [NewtBotSdk](https://github.com/NewtSpeak/NewtBotSdk) | Bot 开放平面官方 SDK |

## 相关仓库

| 仓库 | 关系 |
|------|------|
| [Newt-SFU](https://github.com/NewtSpeak/Newt-SFU) | 媒体节点；proto 源在本仓 |
| [Newt-Desktop](https://github.com/NewtSpeak/Newt-Desktop) | 用户端 |
| [NewtBotSdk](https://github.com/NewtSpeak/NewtBotSdk) | 官方 Bot SDK |
| [Newt-Agent](https://github.com/NewtSpeak/Newt-Agent) | OAuth CLI / MCP |

## 许可证

双重许可（个人非商用免费 + 分发修改版须开源；商用需授权）。见 [`LICENSE`](./LICENSE)、[`LICENSE-NONCOMMERCIAL.md`](./LICENSE-NONCOMMERCIAL.md)、[`LICENSE-COMMERCIAL.md`](./LICENSE-COMMERCIAL.md)。
