# SigNoz 可观测部署指引

Newt-Server 通过 OpenTelemetry（OTLP）导出 traces / metrics / logs，推荐用 [SigNoz](https://signoz.io/) 作为统一后端。SigNoz 依赖 ClickHouse 等多个组件、体量较大，因此**不并入主 `docker-compose.yml`**，按官方方式独立部署。

## 官方 Docker 部署

```sh
git clone -b main https://github.com/SigNoz/signoz.git
cd signoz/deploy/docker
docker compose up -d
```

启动后：

- UI：`http://localhost:8080`（首次访问创建管理员账号）
- OTLP gRPC 接收端：`localhost:4317`
- OTLP HTTP 接收端：`localhost:4318`

也可用官方一键脚本：`git clone https://github.com/SigNoz/signoz.git && cd signoz/deploy && ./install.sh`。

## Newt-Server 接入示例

在 `backend/.env`（或进程环境）里配置：

```sh
# OTLP 接收端；留空则完全不启用导出（no-op）
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
# grpc（默认）或 http/protobuf（对应 4318 端口）
OTEL_EXPORTER_OTLP_PROTOCOL=grpc
# 上报服务名，默认 owl-server
OTEL_SERVICE_NAME=owl-server
# 本地 SigNoz 无 TLS，强制明文连接
OTLP_INSECURE=true
```

重启 owl-server 后，SigNoz 中可看到：

| 信号 | 内容 |
| --- | --- |
| Traces | 每个 HTTP 请求的 otelgin span + GORM SQL 子 span |
| Metrics | `http.server.duration` 直方图（`http.route` / `http.request.method` / `http.response.status_code` 维度） |
| Logs | slog 结构化日志（OTLP 导出，同时保留 stdout 输出） |

## Newt-SFU 说明

Newt-SFU 目前暴露的是 Prometheus 指标端点，尚未接入 OTLP；如需在 SigNoz 中查看，可先用 SigNoz 自带 otel-collector 的 `prometheus` receiver 抓取。SFU 原生 OTLP 接入为后续事项。
