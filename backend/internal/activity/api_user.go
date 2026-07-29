package activity

// 用户端 API：
//   GET /users/@me/activity?days=N  今日明细 + 总分/等级/下一级进度 + 已结算历史

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

type userHandlers struct {
	svc         *service
	currentUser func(*gin.Context) model.User
}

type todayView struct {
	Day           string `json:"day"`
	MsgCount      int    `json:"msg_count"`
	VoiceMinutes  int    `json:"voice_minutes"`
	ReactionCount int    `json:"reaction_count"`
	LoginCount    int    `json:"login_count"`
	ScoreEstimate int64  `json:"score_estimate"`
}

type historyDayView struct {
	Day           string `json:"day"`
	MsgCount      int    `json:"msg_count"`
	VoiceMinutes  int    `json:"voice_minutes"`
	ReactionCount int    `json:"reaction_count"`
	LoginCount    int    `json:"login_count"`
	Score         int64  `json:"score"`
	GrantedPoints int64  `json:"granted_points"`
	Granted       bool   `json:"granted"`
}

type nextLevelView struct {
	Level       int     `json:"level"`
	Threshold   int64   `json:"threshold"`
	ProgressPct float64 `json:"progress_pct"`
}

// myActivity GET /users/@me/activity?days=14
func (h *userHandlers) myActivity(c *gin.Context) {
	user := h.currentUser(c)
	cfg := h.svc.config()
	now := nowFunc()
	today := bizDay(now, cfg.DayOffsetMinutes)

	days := 14
	if raw := c.Query("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			days = n
		}
	}
	if days > 90 {
		days = 90
	}

	var todayRow model.UserActivityDay
	if err := h.svc.db.First(&todayRow, "user_id = ? AND day = ?", user.ID, today).Error; err != nil {
		todayRow = model.UserActivityDay{UserID: user.ID, Day: today}
	}

	var stat model.UserActivityStat
	_ = h.svc.db.First(&stat, "user_id = ?", user.ID).Error

	var history []model.UserActivityDay
	_ = h.svc.db.Where("user_id = ? AND day < ?", user.ID, today).
		Order("day DESC").Limit(days).Find(&history).Error
	historyViews := make([]historyDayView, 0, len(history))
	for _, r := range history {
		historyViews = append(historyViews, historyDayView{
			Day: r.Day, MsgCount: r.MsgCount, VoiceMinutes: r.VoiceMinutes,
			ReactionCount: r.ReactionCount, LoginCount: r.LoginCount,
			Score: r.Score, GrantedPoints: r.GrantedPoints, Granted: r.Granted,
		})
	}

	var nextLevel *nextLevelView
	if stat.Level < len(cfg.Thresholds) {
		threshold := cfg.Thresholds[stat.Level]
		var prev int64
		if stat.Level > 0 {
			prev = cfg.Thresholds[stat.Level-1]
		}
		pct := 0.0
		if threshold > prev {
			pct = float64(stat.TotalScore-prev) / float64(threshold-prev) * 100
		}
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		nextLevel = &nextLevelView{Level: stat.Level + 1, Threshold: threshold, ProgressPct: pct}
	}

	c.JSON(http.StatusOK, gin.H{
		"today": todayView{
			Day: todayRow.Day, MsgCount: todayRow.MsgCount, VoiceMinutes: todayRow.VoiceMinutes,
			ReactionCount: todayRow.ReactionCount, LoginCount: todayRow.LoginCount,
			ScoreEstimate: scoreOf(todayRow, cfg),
		},
		"caps": gin.H{
			"message": cfg.CapMessage, "voice_minutes": cfg.CapVoiceMinutes,
			"reactions": cfg.CapReactions, "login": 1,
		},
		"weights": gin.H{
			"message": cfg.WeightMessage, "voice_minute": cfg.WeightVoiceMinute,
			"reaction": cfg.WeightReaction, "login": cfg.WeightLogin,
		},
		"points_rate":     cfg.PointsRate,
		"level_bonus_pct": bonusPct(stat.Level, cfg),
		"total_score":     stat.TotalScore,
		"level":           stat.Level,
		"next_level":      nextLevel,
		"history":         historyViews,
	})
}
