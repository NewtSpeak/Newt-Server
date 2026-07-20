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
	"log"
	"log/slog"
	"time"

	"github.com/google/uuid"
	_ "github.com/owlspeak/owl-server/backend/docs"
	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
	"github.com/owlspeak/owl-server/backend/internal/ca"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/database"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/mediatoken"
	"github.com/owlspeak/owl-server/backend/internal/observability"
	"github.com/owlspeak/owl-server/backend/internal/secretstore"
	"github.com/owlspeak/owl-server/backend/internal/server"
	"github.com/owlspeak/owl-server/backend/internal/sfucontrol"
	"github.com/owlspeak/owl-server/backend/internal/stage"
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
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}

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
		if err := control.Serve(context.Background()); err != nil {
			slog.Error("SFU 控制面监听退出", "error", err)
		}
	}()

	router, err := server.New(cfg, db, bus, httpapi.SFUOptions{Registry: registry, MediaTokens: mediaTokens})
	if err != nil {
		log.Fatal(err)
	}
	if err := router.Run(cfg.Address); err != nil {
		log.Fatal(err)
	}
}
