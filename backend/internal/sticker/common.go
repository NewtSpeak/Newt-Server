// Package sticker 贴图与表情包系统（docs 17）：
// 账号级/服独属包、emote/sticker 双 kind、整包引用 Install + 单条 Copy、
// 内容 hash 去重、软删 180 天恢复、服 ban 与后台治理、发送可用集合鉴权。
package sticker

import (
	"net/http"
	"strconv"
	"strings"
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

// 配额与资源上限（docs 17 §9）。
const (
	defaultMaxPacksPerUser  = 25
	defaultMaxItemsPerPack  = 100
	defaultMaxFileBytes     = int64(512 << 10) // 512 KiB
	softDeleteRestoreDays   = 180
	maxPackNameRunes        = 100
	maxPackDescRunes        = 500
	maxItemNameRunes        = 100
	maxBanReasonRunes       = 500
	markPrefix              = "e_"
	markHashLen             = 12
	publicAssetURLPrefix    = "/public-assets/stickers/"
)

// 自定义表情 wire format（正文内嵌）：<e:item_id:mark>
// 反应路径编码：item:{item_id}
const (
	CustomEmoteWirePrefix = "<e:"
	ReactionItemPrefix    = "item:"
)

type api struct {
	deps appdeps.Deps
	ids  *snowflakeGen
}

func newAPI(deps appdeps.Deps) *api {
	return &api{deps: deps, ids: newSnowflake()}
}

func (h *api) db() *gorm.DB                 { return h.deps.DB }
func (h *api) bus() *eventbus.Bus            { return h.deps.Bus }
func (h *api) cfg() config.Config            { return h.deps.Cfg }
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

func ptrInt64(v int64) *int64 { return &v }

func clampRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

// normalizeItemName 规范化表情展示名：去路径、去常见图片扩展名、限长。
// 空串表示调用方应回退到 mark 等默认值。
func normalizeItemName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// 去掉路径前缀
	if i := strings.LastIndexAny(s, `/\`); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	// 去掉图片扩展名（上传常把文件名当 name）
	lower := strings.ToLower(s)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".apng"} {
		if strings.HasSuffix(lower, ext) {
			s = s[:len(s)-len(ext)]
			lower = strings.ToLower(s)
			break
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return clampRunes(s, maxItemNameRunes)
}

// ---------- 雪花 ID（与 message 包布局一致，进程内独立生成器） ----------

const (
	snowflakeEpochMs = int64(1767225600000) // 2026-01-01 UTC
	snowflakeMachine = int64(1)             // 与消息生成器机器位区分，避免同毫秒冲突
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
