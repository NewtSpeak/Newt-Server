# Owl-Server 后端

Gin + PostgreSQL 控制面，当前实现账号认证、Guild RBAC、角色、成员角色绑定和频道权限覆盖。

## 开发启动

```sh
cp .env.example .env
docker compose -f ../docker-compose.yml up -d postgres
make dev
```

`make dev` 同时启动 React Router（Bun）与 Air。浏览器访问 `http://localhost:8080/`，Gin 会代理前端开发端口；Swagger UI 位于 `http://localhost:8080/swagger/index.html`。

开发环境默认 **自动拉起本机 SFU**（`EMBEDDED_SFU=true`）：创建「本地内嵌 SFU」节点占位、编译/查找 `owl-sfu`、完成 enrollment，并设为平台默认调度池。无需再单独启动 Owl-SFU。日志与证书目录在 `DATA_DIR/embedded-sfu/`。生产环境默认关闭，需要时设置 `EMBEDDED_SFU=true`。

也可以在仓库根目录直接执行 `air`，此时只启动后端热重载；根目录 `.air.toml` 会将监听范围限制在 `backend`，不会扫描 `frontend/node_modules`。

## 生产构建

```sh
make build
```

构建会先产出 React SPA，再将静态资源编译进 `bin/owl-server`。运行时只需一个二进制和 PostgreSQL 连接配置，不需要单独启动前端服务。

生产运行时请设置 `APP_ENV=production`，Gin 才会提供编译进二进制的 SPA；开发环境则始终代理 Vite，避免 Air 使用旧的静态资源。

首次空数据库启动时，`/signup` 只允许创建一个初始化账号，该账号自动成为系统管理员。初始化完成后注册接口关闭，后台仅保留登录。密码登录按账号和来源 IP 双维度限流。

数据库仅支持 PostgreSQL，必须配置 `DATABASE_URL`；项目未引入 SQLite 驱动。

## 可观测（OTLP → SigNoz）

后端内置 OpenTelemetry：HTTP 请求追踪（otelgin）、`http.server.duration` 直方图（按路由维度）、GORM SQL 追踪与 slog 日志桥（OTLP 导出 + stdout 双写）。**未配置 `OTEL_EXPORTER_OTLP_ENDPOINT` 时全部为 no-op**，本地开发零成本。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 空（不启用） | OTLP 接收端地址，如 `http://127.0.0.1:4317` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc` 或 `http/protobuf` |
| `OTEL_SERVICE_NAME` | `owl-server` | 上报的服务名 |
| `OTLP_INSECURE` | `false` | `true` 时强制明文连接（本地 SigNoz 常用） |
| `AUDIT_RETENTION_DAYS` | `0`（永久） | 审计日志保留天数，>0 时每小时清理过期记录 |
| `AUDIT_INGEST_TOKEN` | 开发环境自动派生 / 生产必填才开启 | SFU 上传音频审计录音的共享密钥；经 RegisterAck 下发 |
| `PUBLIC_BASE_URL` | 开发默认 `http://127.0.0.1{APP_ADDRESS}` | 对外根地址；用于邀请链接与 SFU 审计上传 URL |
| `EMBEDDED_SFU` | development 默认 `true` / production 默认 `false` | 启动时自动创建本机 SFU 占位并拉起 `owl-sfu` 子进程 |
| `EMBEDDED_SFU_BIN` | 自动搜索 / 按需编译 monorepo `Owl-SFU` | 指定 `owl-sfu` 可执行文件路径 |
| `EMBEDDED_SFU_WSS_LISTEN` | `:8445` | 内嵌 SFU 信令监听地址 |
| `EMBEDDED_SFU_MEDIA_UDP` | `3478` | 内嵌 SFU 媒体 UDP 端口 |
| `EMBEDDED_SFU_PUBLIC_IP` | `127.0.0.1` | 上报给客户端的媒体 IP |
| `EMBEDDED_SFU_NO_TLS` | development 默认 `true` | 内嵌 SFU 是否禁用信令 TLS |
| `STICKER_MAX_FILE_BYTES` | `50m`（50 MiB） | 贴图/表情单文件大小上限（每服务器实例独立）。纯数字为字节；可写 `50m`/`50mb`/`512k`/`1g`。**`0` = 不限制**（实际无硬顶，仍受内存与反向代理限制）。格式支持 PNG/JPEG/WebP/GIF + MP4/WebM 等 |

SigNoz 部署方式与完整示例见 `../deploy/signoz/README.md`。Owl-SFU 目前暴露 Prometheus 指标，接入 OTLP 为后续事项。
