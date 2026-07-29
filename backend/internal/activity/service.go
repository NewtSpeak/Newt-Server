// Package activity 平台活跃度：多维度实时累计每日活跃度、总分等级、
// 每日结算自动发放装扮积分（等级加成）。业务日按配置偏移切分（默认 UTC+8）。
package activity

import (
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

type service struct {
	db      *gorm.DB
	bus     *eventbus.Bus
	tracker *tracker
	// cfg 配置缓存（Track 热路径无锁读取）；flush/settle 周期性刷新。
	cfg atomic.Pointer[resolvedConfig]
}

// loadConfig 从 DB 读取配置（无行回默认）并刷新缓存。
func (s *service) loadConfig() *resolvedConfig {
	setting := defaultSetting()
	if s.db != nil {
		var row model.ActivitySetting
		err := s.db.First(&row, "id = 1").Error
		if err == nil {
			setting = row
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("activity: 读取配置失败，沿用默认/缓存: %v", err)
			if cached := s.cfg.Load(); cached != nil {
				return cached
			}
		}
	}
	resolved := &resolvedConfig{ActivitySetting: setting, Thresholds: parseThresholds(setting.LevelThresholdsJSON)}
	s.cfg.Store(resolved)
	return resolved
}

// config 读缓存（未初始化时同步加载一次）。
func (s *service) config() *resolvedConfig {
	if cached := s.cfg.Load(); cached != nil {
		return cached
	}
	return s.loadConfig()
}

func (s *service) publishToUser(userID uuid.UUID, eventType string, payload any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:    eventType,
		UserIDs: []uuid.UUID{userID},
		Payload: payload,
	})
}

// publishActivityUpdate 定向下发某用户的今日计数与总分/等级快照。
func (s *service) publishActivityUpdate(userID uuid.UUID, row model.UserActivityDay, cfg *resolvedConfig) {
	var stat model.UserActivityStat
	_ = s.db.First(&stat, "user_id = ?", userID).Error
	s.publishToUser(userID, eventbus.EventActivityUpdate, gin.H{
		"day":            row.Day,
		"msg_count":      row.MsgCount,
		"voice_minutes":  row.VoiceMinutes,
		"reaction_count": row.ReactionCount,
		"login_count":    row.LoginCount,
		"score_estimate": scoreOf(row, cfg),
		"total_score":    stat.TotalScore,
		"level":          stat.Level,
	})
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func (s *service) requireSystemAdmin(currentUser func(*gin.Context) model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !currentUser(c).SystemAdmin {
			fail(c, http.StatusForbidden, "FORBIDDEN", "需要系统管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

// LevelOf 只读投影：用户当前活跃度等级（无记录返回 0）。供 publicProfile 等外部展示。
func LevelOf(db *gorm.DB, userID uuid.UUID) int {
	if db == nil {
		return 0
	}
	var stat model.UserActivityStat
	if err := db.Select("level").First(&stat, "user_id = ?", userID).Error; err != nil {
		return 0
	}
	return stat.Level
}

// nowFunc 便于测试注入假时钟。
var nowFunc = func() time.Time { return time.Now().UTC() }
