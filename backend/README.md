# Owl-Server 后端

Gin + PostgreSQL 控制面，当前实现账号认证、Guild RBAC、角色、成员角色绑定和频道权限覆盖。

## 开发启动

```sh
cp .env.example .env
docker compose -f ../docker-compose.yml up -d postgres
make dev
```

`make dev` 同时启动 React Router（Bun）与 Air。浏览器访问 `http://localhost:8080/`，Gin 会代理前端开发端口；Swagger UI 位于 `http://localhost:8080/swagger/index.html`。

## 生产构建

```sh
make build
```

构建会先产出 React SPA，再将静态资源编译进 `bin/owl-server`。运行时只需一个二进制和 PostgreSQL 连接配置，不需要单独启动前端服务。

数据库仅支持 PostgreSQL，必须配置 `DATABASE_URL`；项目未引入 SQLite 驱动。
