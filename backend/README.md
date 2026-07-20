# Owl-Server 后端

Gin + PostgreSQL 控制面，当前实现账号认证、Guild RBAC、角色、成员角色绑定和频道权限覆盖。

## 开发启动

```sh
cp .env.example .env
docker compose -f ../docker-compose.yml up -d postgres
make dev
```

`make dev` 同时启动 React Router（Bun）与 Air。浏览器访问 `http://localhost:8080/`，Gin 会代理前端开发端口；Swagger UI 位于 `http://localhost:8080/swagger/index.html`。

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

SigNoz 部署方式与完整示例见 `../deploy/signoz/README.md`。Owl-SFU 目前暴露 Prometheus 指标，接入 OTLP 为后续事项。
