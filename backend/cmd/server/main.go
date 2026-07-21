// Package main Owl-Server 控制面 API。
// @title Owl-Server API
// @version 0.1.0
// @description OwlSpeak 账号、Guild RBAC 与权限管理 API。
// @BasePath /api/v1
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/owlspeak/owl-server/backend/docs"
	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
	"github.com/owlspeak/owl-server/backend/internal/ca"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/database"
	"github.com/owlspeak/owl-server/backend/internal/embeddedsfu"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/observability"
	"github.com/owlspeak/owl-server/backend/internal/secretstore"
	"github.com/owlspeak/owl-server/backend/internal/server"
	"github.com/owlspeak/owl-server/backend/internal/sfucontrol"
	"github.com/owlspeak/owl-server/backend/internal/stage"
	"github.com/owlspeak/owl-server/backend/internal/voice"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	// OTLP 可观测（traces/metrics/logs → SigNoz）：未配置 OTEL_EXPORTER_OTLP_ENDPOINT 时为 no-op。
	otelShutdown, err := observability.Init(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(ctx); err != nil {
			slog.Error("observability 关闭失败", "error", err)
		}
	}()
	// Prometheus /metrics（docs 09 §11 迁移观测等）：METRICS_ADDRESS 非空时启动，
	// 仅应绑定内网/本机地址。
	metricsShutdown := observability.StartMetricsServer(cfg.MetricsAddress)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = metricsShutdown(ctx)
	}()
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}

	// 根 context：收到 SIGINT/SIGTERM 时取消，用于收口控制面与内嵌 SFU。
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// SFU 配套子系统：集群 CA、Media Token 签发、节点注册表与控制面 gRPC。
	secrets := secretstore.GormStore{DB: db}
	authority, err := ca.Load(secrets)
	if err != nil {
		log.Fatal(err)
	}
	mediaTokens, err := mediatoken.Load(secrets, cfg.MediaTokenTTL)
	if err != nil {
		log.Fatal(err)
	}
	registry := sfucontrol.NewRegistry()
	control := sfucontrol.NewService(db, authority, mediaTokens, registry, sfucontrol.Config{
		Address:             cfg.SFUControlAddress,
		PublicEndpoint:      cfg.SFUControlPublicEndpoint,
		TLSSANs:             cfg.SFUControlTLSSANs,
		HeartbeatIntervalMS: cfg.SFUHeartbeatIntervalMS,
		// BI.3 提前判死组合规则（docs 15 §5）：≥2 独立信号源 + ≥1 次心跳丢失。
		EarlyDeath: sfucontrol.EarlyDeathConfig{
			Enabled:            cfg.SFUEarlyDeathEnabled,
			MinSources:         cfg.SFUEarlyDeathMinSources,
			MinHeartbeatMisses: cfg.SFUEarlyDeathMinHeartbeatMisses,
			SignalTTL:          cfg.SFUEarlyDeathSignalTTL,
		},
		// 音频审计：RegisterAck 下发给 SFU，免双端手写同一密钥（adminpresence）。
		AuditIngestURL:   cfg.AuditIngestURL,
		AuditIngestToken: cfg.AuditIngestToken,
	})

	// 事件总线由控制面（节点判死）与领域模块（voice 迁移引擎等）共享：
	// 心跳 5s×3 判死 → InternalNodeDown → voice 批量迁移（docs 09、15 BI）。
	bus := eventbus.New()
	control.SetNodeDownHandler(func(nodeID uuid.UUID) {
		bus.Publish(eventbus.Event{
			Type:    eventbus.InternalNodeDown,
			Payload: map[string]any{"node_id": nodeID.String(), "reason": "HEARTBEAT_TIMEOUT"},
		})
	})
	// SFU 上报屏幕轨发布成功 → ScreenSlot RESERVED→ACTIVE（docs 14 BC.1 步骤 5–6），
	// 防止 60s 预留超时把在播共享误回收。
	control.SetScreenTrackActiveHandler(func(channelID, userID uuid.UUID) {
		stage.OnScreenTrackActive(db, bus, channelID, userID)
	})
	// 级联边断开 → InternalEdgeDown → voice 编排补边/标记降级（docs 08 §7.2、15 BI.2）。
	control.SetEdgeDownHandler(func(es *owlsfuv1.EdgeStatus) {
		bus.Publish(eventbus.Event{
			Type: eventbus.InternalEdgeDown,
			Payload: eventbus.EdgeDownPayload{
				RoomID: es.GetRoomId(), Epoch: es.GetEpoch(),
				ParentNodeID: es.GetParentNodeId(), ChildNodeID: es.GetChildNodeId(),
			},
		})
	})
	go func() {
		if err := control.Serve(rootCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Serve 在 listener 关闭时返回 error；取消 context 后 GracefulStop。
			slog.Error("SFU 控制面监听退出", "error", err)
		}
	}()
	// 错开控制面冷启动，避免内嵌 SFU enroll 打到尚未监听的 gRPC。
	time.Sleep(400 * time.Millisecond)

	// 内嵌本地 SFU：开发环境默认开启，创建占位 + 拉起 owl-sfu 子进程 + 纳入平台默认池。
	var embedded *embeddedsfu.Process
	if cfg.EmbeddedSFU {
		proc, err := embeddedsfu.Start(rootCtx, db, cfg)
		if err != nil {
			slog.Error("内嵌 SFU 启动失败（语音将不可用，可设置 EMBEDDED_SFU_BIN 或手动接入节点）", "error", err)
		} else {
			embedded = proc
		}
		defer func() {
			if embedded != nil {
				embedded.Stop()
			}
		}()
	}

	router, err := server.New(cfg, db, bus, httpapi.SFUOptions{Registry: registry, MediaTokens: mediaTokens})
	if err != nil {
		log.Fatal(err)
	}
	// voice 单例已在 server.New 内装配：SFU connected 翻转 → VOICE_STATE_UPDATE
	control.SetVoiceConnectedChangeHandler(voice.PublishConnectedChange)

	// 优雅退出：先停 HTTP，再 cancel 根 context（收口控制面与内嵌 SFU）。
	httpSrv := &http.Server{Addr: cfg.Address, Handler: router}
	go func() {
		slog.Info("HTTP 监听启动", "address", cfg.Address)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("收到退出信号，开始优雅关闭", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	rootCancel()
	if embedded != nil {
		embedded.Stop()
	}
}
