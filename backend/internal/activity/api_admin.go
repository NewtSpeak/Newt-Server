package activity

// 管理端 API（/api/v1/admin/activity，system_admin）：
//   GET  /config             读取配置（无行返回默认值）
//   PUT  /config             全量更新配置（校验门槛严格递增、数值非负）
//   GET  /stats?day=         按业务日聚合平台概览
//   GET  /users/:userID      单用户汇总 + 逐日明细（排障/申诉）
//   POST /settle             手动触发一轮结算

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm/clause"
)

type adminHandlers struct {
	svc *service
}

// configView 配置对外投影：门槛以解析后数组下发（前端免拆 JSON 字符串）。
func configView(cfg *resolvedConfig) gin.H {
	return gin.H{
		"enabled":             cfg.Enabled,
		"day_offset_minutes":  cfg.DayOffsetMinutes,
		"weight_message":      cfg.WeightMessage,
		"cap_message":         cfg.CapMessage,
		"weight_voice_minute": cfg.WeightVoiceMinute,
		"cap_voice_minutes":   cfg.CapVoiceMinutes,
		"weight_reaction":     cfg.WeightReaction,
		"cap_reactions":       cfg.CapReactions,
		"weight_login":        cfg.WeightLogin,
		"points_rate":         cfg.PointsRate,
		"bonus_per_level_pct": cfg.BonusPerLevelPct,
		"max_bonus_pct":       cfg.MaxBonusPct,
		"level_thresholds":    cfg.Thresholds,
		"updated_at":          cfg.UpdatedAt,
	}
}

// getConfig GET /admin/activity/config
func (h *adminHandlers) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, configView(h.svc.loadConfig()))
}

type configWriteRequest struct {
	Enabled           *bool    `json:"enabled"`
	DayOffsetMinutes  *int     `json:"day_offset_minutes"`
	WeightMessage     *float64 `json:"weight_message"`
	CapMessage        *int     `json:"cap_message"`
	WeightVoiceMinute *float64 `json:"weight_voice_minute"`
	CapVoiceMinutes   *int     `json:"cap_voice_minutes"`
	WeightReaction    *float64 `json:"weight_reaction"`
	CapReactions      *int     `json:"cap_reactions"`
	WeightLogin       *float64 `json:"weight_login"`
	PointsRate        *float64 `json:"points_rate"`
	BonusPerLevelPct  *float64 `json:"bonus_per_level_pct"`
	MaxBonusPct       *float64 `json:"max_bonus_pct"`
	LevelThresholds   []int64  `json:"level_thresholds"`
}

// putConfig PUT /admin/activity/config（以当前生效值为底全量覆盖提交的字段）。
func (h *adminHandlers) putConfig(c *gin.Context) {
	var input configWriteRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	setting := h.svc.loadConfig().ActivitySetting
	setting.ID = 1
	if input.Enabled != nil {
		setting.Enabled = *input.Enabled
	}
	if input.DayOffsetMinutes != nil {
		if *input.DayOffsetMinutes < -1440 || *input.DayOffsetMinutes > 1440 {
			fail(c, http.StatusBadRequest, "INVALID_CONFIG", "day_offset_minutes 须在 ±1440 内")
			return
		}
		setting.DayOffsetMinutes = *input.DayOffsetMinutes
	}
	applyFloat := func(dst *float64, src *float64, name string) bool {
		if src == nil {
			return true
		}
		if *src < 0 {
			fail(c, http.StatusBadRequest, "INVALID_CONFIG", name+" 不能为负")
			return false
		}
		*dst = *src
		return true
	}
	applyInt := func(dst *int, src *int, name string) bool {
		if src == nil {
			return true
		}
		if *src < 0 {
			fail(c, http.StatusBadRequest, "INVALID_CONFIG", name+" 不能为负")
			return false
		}
		*dst = *src
		return true
	}
	if !applyFloat(&setting.WeightMessage, input.WeightMessage, "weight_message") ||
		!applyInt(&setting.CapMessage, input.CapMessage, "cap_message") ||
		!applyFloat(&setting.WeightVoiceMinute, input.WeightVoiceMinute, "weight_voice_minute") ||
		!applyInt(&setting.CapVoiceMinutes, input.CapVoiceMinutes, "cap_voice_minutes") ||
		!applyFloat(&setting.WeightReaction, input.WeightReaction, "weight_reaction") ||
		!applyInt(&setting.CapReactions, input.CapReactions, "cap_reactions") ||
		!applyFloat(&setting.WeightLogin, input.WeightLogin, "weight_login") ||
		!applyFloat(&setting.PointsRate, input.PointsRate, "points_rate") ||
		!applyFloat(&setting.BonusPerLevelPct, input.BonusPerLevelPct, "bonus_per_level_pct") ||
		!applyFloat(&setting.MaxBonusPct, input.MaxBonusPct, "max_bonus_pct") {
		return
	}
	if input.LevelThresholds != nil {
		if !thresholdsValid(input.LevelThresholds) {
			fail(c, http.StatusBadRequest, "INVALID_CONFIG", "level_thresholds 须为正数且严格递增")
			return
		}
		raw, err := json.Marshal(input.LevelThresholds)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_CONFIG", "level_thresholds 序列化失败")
			return
		}
		setting.LevelThresholdsJSON = string(raw)
	}
	setting.UpdatedAt = nowFunc()
	if err := h.svc.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&setting).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存配置失败")
		return
	}
	c.JSON(http.StatusOK, configView(h.svc.loadConfig()))
}

// getStats GET /admin/activity/stats?day=2006-01-02（缺省 = 今日业务日）
func (h *adminHandlers) getStats(c *gin.Context) {
	cfg := h.svc.config()
	day := c.Query("day")
	if day == "" {
		day = bizDay(nowFunc(), cfg.DayOffsetMinutes)
	} else if _, err := time.Parse("2006-01-02", day); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_DAY", "day 须为 YYYY-MM-DD")
		return
	}
	var agg struct {
		ActiveUsers   int64
		TotalScore    int64
		GrantedUsers  int64
		GrantedPoints int64
	}
	err := h.svc.db.Model(&model.UserActivityDay{}).
		Select(`COUNT(*) AS active_users,
			COALESCE(SUM(score), 0) AS total_score,
			COUNT(*) FILTER (WHERE granted) AS granted_users,
			COALESCE(SUM(granted_points), 0) AS granted_points`).
		Where("day = ?", day).Scan(&agg).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "统计查询失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"day": day, "active_users": agg.ActiveUsers, "total_score": agg.TotalScore,
		"granted_users": agg.GrantedUsers, "granted_points": agg.GrantedPoints,
	})
}

// getUserDetail GET /admin/activity/users/:userID?days=30
func (h *adminHandlers) getUserDetail(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	days := 30
	if raw := c.Query("days"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	var stat model.UserActivityStat
	_ = h.svc.db.First(&stat, "user_id = ?", userID).Error
	var rows []model.UserActivityDay
	_ = h.svc.db.Where("user_id = ?", userID).Order("day DESC").Limit(days).Find(&rows).Error
	c.JSON(http.StatusOK, gin.H{
		"user_id": userID, "total_score": stat.TotalScore, "level": stat.Level, "days": rows,
	})
}

// triggerSettle POST /admin/activity/settle（手动触发一轮结算，返回结算行数）
func (h *adminHandlers) triggerSettle(c *gin.Context) {
	settled, err := h.svc.runSettleOnce()
	if err != nil {
		fail(c, http.StatusInternalServerError, "SETTLE_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"settled": settled})
}
