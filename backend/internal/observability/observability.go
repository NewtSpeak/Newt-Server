// Package observability OTLP 可观测接入（traces / metrics / logs → SigNoz 等 OTLP 后端）。
// 未配置 OTEL_EXPORTER_OTLP_ENDPOINT 时全部为 no-op，不影响本地开发。
//
// 环境变量：
//   - OTEL_EXPORTER_OTLP_ENDPOINT：OTLP 接收端地址（如 http://127.0.0.1:4317）；为空则不启用导出
//   - OTEL_EXPORTER_OTLP_PROTOCOL：grpc（默认）或 http/protobuf
//   - OTEL_SERVICE_NAME：服务名，默认 owl-server
//   - OTLP_INSECURE：true 时强制明文连接（本地 SigNoz 常用）
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// serviceName Init 后生效的服务名；GinMiddleware 与 slog 桥共用。
var serviceName = "owl-server"

// Init 初始化 OTLP 导出器与全局 provider（TracerProvider + MeterProvider + LoggerProvider），
// 返回优雅关闭函数。未配置 OTEL_EXPORTER_OTLP_ENDPOINT 时保持 no-op。
func Init(ctx context.Context, cfg config.Config) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		slog.Info("observability: 未配置 OTEL_EXPORTER_OTLP_ENDPOINT，OTLP 导出保持关闭（no-op）")
		return noop, nil
	}
	if name := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); name != "" {
		serviceName = name
	}
	insecure, _ := strconv.ParseBool(os.Getenv("OTLP_INSECURE"))
	protocol := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
	useGRPC := protocol == "" || protocol == "grpc"

	// NewSchemaless 避免与 resource.Default() 的 semconv schema 版本冲突。
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName),
		attribute.String("deployment.environment", cfg.Environment),
	))
	if err != nil {
		return noop, fmt.Errorf("构建 OTel resource: %w", err)
	}

	// endpoint / TLS 均交由各 exporter 按标准环境变量解析（OTEL_EXPORTER_OTLP_ENDPOINT 的
	// scheme 决定是否 TLS）；OTLP_INSECURE=true 时显式覆盖为明文。
	var shutdowns []func(context.Context) error
	shutdownAll := func(ctx context.Context) error {
		var errs []error
		// 逆序关闭：先停 provider 刷缓冲，再断 exporter。
		for i := len(shutdowns) - 1; i >= 0; i-- {
			errs = append(errs, shutdowns[i](ctx))
		}
		return errors.Join(errs...)
	}

	// Traces
	var traceExporter sdktrace.SpanExporter
	if useGRPC {
		opts := []otlptracegrpc.Option{}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		traceExporter, err = otlptracegrpc.New(ctx, opts...)
	} else {
		opts := []otlptracehttp.Option{}
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		traceExporter, err = otlptracehttp.New(ctx, opts...)
	}
	if err != nil {
		return noop, fmt.Errorf("创建 OTLP trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	shutdowns = append(shutdowns, tracerProvider.Shutdown)

	// Metrics
	var metricExporter sdkmetric.Exporter
	if useGRPC {
		opts := []otlpmetricgrpc.Option{}
		if insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		metricExporter, err = otlpmetricgrpc.New(ctx, opts...)
	} else {
		opts := []otlpmetrichttp.Option{}
		if insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		metricExporter, err = otlpmetrichttp.New(ctx, opts...)
	}
	if err != nil {
		_ = shutdownAll(ctx)
		return noop, fmt.Errorf("创建 OTLP metric exporter: %w", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)
	shutdowns = append(shutdowns, meterProvider.Shutdown)

	// Logs：OTLP 导出 + 保留 stdout（slog 双写）。
	var logExporter sdklog.Exporter
	if useGRPC {
		opts := []otlploggrpc.Option{}
		if insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		logExporter, err = otlploggrpc.New(ctx, opts...)
	} else {
		opts := []otlploghttp.Option{}
		if insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		logExporter, err = otlploghttp.New(ctx, opts...)
	}
	if err != nil {
		_ = shutdownAll(ctx)
		return noop, fmt.Errorf("创建 OTLP log exporter: %w", err)
	}
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)
	logglobal.SetLoggerProvider(loggerProvider)
	shutdowns = append(shutdowns, loggerProvider.Shutdown)

	slog.SetDefault(slog.New(fanoutHandler{
		slog.NewTextHandler(os.Stdout, nil),
		otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(loggerProvider)),
	}))

	slog.Info("observability: OTLP 导出已启用",
		"endpoint", endpoint, "protocol", map[bool]string{true: "grpc", false: "http/protobuf"}[useGRPC],
		"service", serviceName, "insecure", insecure)
	return shutdownAll, nil
}

// StartMetricsServer 在独立监听地址暴露 Prometheus /metrics（默认注册表，含
// voice 迁移指标等，docs 09 §11）。addr 为空时不启动；部署时应仅绑定内网/本机
//（METRICS_ADDRESS，如 "127.0.0.1:9091"），不做鉴权。
// 返回关闭函数；监听失败只记录错误（观测面不拖垮业务面）。
func StartMetricsServer(addr string) func(context.Context) error {
	if strings.TrimSpace(addr) == "" {
		return func(context.Context) error { return nil }
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("observability: Prometheus /metrics 监听启动（应仅内网可达）", "address", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("observability: /metrics 监听退出", "error", err)
		}
	}()
	return server.Shutdown
}

// GinMiddleware 返回 HTTP 请求可观测中间件：otelgin 追踪 + http.server.duration 直方图
//（按 method / route / status_code 维度）。未 Init 时全局 provider 为 no-op，开销可忽略。
func GinMiddleware() []gin.HandlerFunc {
	meter := otel.Meter("github.com/owlspeak/owl-server/backend/internal/observability")
	duration, err := meter.Float64Histogram(
		"http.server.duration",
		metric.WithUnit("s"),
		metric.WithDescription("HTTP 请求处理耗时（按路由维度）"),
	)
	if err != nil {
		slog.Warn("observability: 创建 http.server.duration 直方图失败", "error", err)
	}
	return []gin.HandlerFunc{
		otelgin.Middleware(serviceName),
		func(c *gin.Context) {
			start := time.Now()
			c.Next()
			if duration == nil {
				return
			}
			route := c.FullPath()
			if route == "" {
				// 未命中路由（404 等）统一归并，避免高基数标签。
				route = "unmatched"
			}
			duration.Record(c.Request.Context(), time.Since(start).Seconds(), metric.WithAttributes(
				attribute.String("http.request.method", c.Request.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", c.Writer.Status()),
			))
		},
	}
}
