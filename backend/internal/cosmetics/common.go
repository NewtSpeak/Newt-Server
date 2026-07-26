// Package cosmetics 平台装扮商店：可扩展品类、单品/捆绑、标签、库存装备、积分兑换。
package cosmetics

import (
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

const (
	publicAssetURLPrefix = "/public-assets/cosmetics/"
	maxNameRunes         = 100
	maxDescRunes         = 2000
	maxTagKeyRunes       = 64
	defaultMaxAssetBytes = int64(12 << 20) // 12 MiB 默认单槽上限
	maxAudioBytes        = int64(2 << 20)  // 2 MiB 音效
)

type api struct {
	deps appdeps.Deps
	ids  *snowflakeGen
}

func newAPI(deps appdeps.Deps) *api {
	return &api{deps: deps, ids: newSnowflake()}
}

func (h *api) db() *gorm.DB          { return h.deps.DB }
func (h *api) bus() *eventbus.Bus     { return h.deps.Bus }
func (h *api) cfg() config.Config     { return h.deps.Cfg }
func (h *api) currentUser(c *gin.Context) model.User {
	return h.deps.CurrentUser(c)
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func notFound(c *gin.Context) {
	fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在或不可见")
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

func parseSnowflakeParam(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		notFound(c)
		return 0, false
	}
	return id, true
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		notFound(c)
		return uuid.Nil, false
	}
	return id, true
}

func parseSnowflakeString(raw string) (int64, error) {
	return strconv.ParseInt(raw, 10, 64)
}

func strID(id int64) string { return strconv.FormatInt(id, 10) }

func clampRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

func (h *api) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := h.currentUser(c)
		if !user.SystemAdmin {
			fail(c, http.StatusForbidden, "FORBIDDEN", "需要系统管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (h *api) publishToUser(userID uuid.UUID, eventType string, payload any) {
	if h.bus() == nil {
		return
	}
	h.bus().Publish(eventbus.Event{
		Type:    eventType,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

// publishLoadoutToUserGuilds 装备变更：本人全部端 + 所在各服在线成员。
func (h *api) publishLoadoutToUserGuilds(userID uuid.UUID, payload any) {
	if h.bus() == nil {
		return
	}
	var guildIDs []uuid.UUID
	_ = h.db().Model(&model.Member{}).Where("user_id = ?", userID).Pluck("guild_id", &guildIDs).Error
	for i := range guildIDs {
		gid := guildIDs[i]
		h.bus().Publish(eventbus.Event{
			Type:    eventbus.EventCosmeticLoadoutUpdate,
			GuildID: &gid,
			Payload: payload,
		})
	}
	h.bus().Publish(eventbus.Event{
		Type:    eventbus.EventCosmeticLoadoutUpdate,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

func (h *api) publishCatalogUpdate(payload any) {
	if h.bus() == nil {
		return
	}
	// 全站广播：无 GuildID / UserIDs 时 gateway 按实现推送（若仅支持定向，客户端依赖轮询 version）
	h.bus().Publish(eventbus.Event{
		Type:    eventbus.EventCosmeticCatalogUpdate,
		Payload: payload,
	})
}

// ---------- 雪花 ID（独立 machine 位，避免与 message/sticker 冲突） ----------

const (
	snowflakeEpochMs = int64(1767225600000) // 2026-01-01 UTC
	snowflakeMachine = int64(2)
	machineBits      = 10
	sequenceBits     = 12
	sequenceMask     = int64(1<<sequenceBits - 1)
	timestampShift   = machineBits + sequenceBits
	machineShift     = sequenceBits
)

type snowflakeGen struct {
	mu       sync.Mutex
	lastMs   int64
	sequence int64
	now      func() int64
}

func newSnowflake() *snowflakeGen {
	return &snowflakeGen{now: func() int64 { return time.Now().UnixMilli() }}
}

func (g *snowflakeGen) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	nowMs := g.now()
	if nowMs < g.lastMs {
		nowMs = g.lastMs
	}
	if nowMs == g.lastMs {
		g.sequence = (g.sequence + 1) & sequenceMask
		if g.sequence == 0 {
			for nowMs <= g.lastMs {
				nowMs = g.now()
				if nowMs < g.lastMs {
					nowMs = g.lastMs + 1
				}
			}
		}
	} else {
		g.sequence = 0
	}
	g.lastMs = nowMs
	return (nowMs-snowflakeEpochMs)<<timestampShift | snowflakeMachine<<machineShift | g.sequence
}
