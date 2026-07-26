package activity

// 纯函数层：业务日切分、等级计算、计分与加成。无 DB/IO，便于单测。

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/owlspeak/owl-server/backend/internal/model"
)

const maxLevel = 50

// bizDay 事件时刻对应的业务日（"2006-01-02"）：UTC 加偏移后取日期。
func bizDay(t time.Time, offsetMinutes int) string {
	return t.UTC().Add(time.Duration(offsetMinutes) * time.Minute).Format("2006-01-02")
}

// defaultThresholds 默认等级曲线：threshold(L) = 50·L² + 50·L（每级增量 100·L）。
// Lv1=100、Lv10=5500、Lv50=127500；日均 300 分约 1 周 Lv10。
func defaultThresholds() []int64 {
	out := make([]int64, maxLevel)
	for l := int64(1); l <= maxLevel; l++ {
		out[l-1] = 50*l*l + 50*l
	}
	return out
}

// parseThresholds 解析配置的门槛数组；空/非法/非严格递增回退默认曲线。
func parseThresholds(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return defaultThresholds()
	}
	var out []int64
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out) == 0 {
		return defaultThresholds()
	}
	if !thresholdsValid(out) {
		return defaultThresholds()
	}
	return out
}

// thresholdsValid 门槛数组须全部为正且严格递增。
func thresholdsValid(t []int64) bool {
	if len(t) == 0 {
		return false
	}
	prev := int64(0)
	for _, v := range t {
		if v <= prev {
			return false
		}
		prev = v
	}
	return true
}

// levelFor 累计总分对应等级：thresholds[i] = 达到 Lv(i+1) 的累计门槛。
func levelFor(total int64, thresholds []int64) int {
	level := 0
	for _, th := range thresholds {
		if total < th {
			break
		}
		level++
	}
	return level
}

// resolvedConfig 配置解析结果（Setting + 已解析门槛），tracker/settler 共用。
type resolvedConfig struct {
	model.ActivitySetting
	Thresholds []int64
}

// scoreOf 一行每日计数在给定配置下的得分（各维度按日上限截断，登录恒 cap=1）。
func scoreOf(row model.UserActivityDay, cfg *resolvedConfig) int64 {
	msg := minInt(row.MsgCount, cfg.CapMessage)
	voice := minInt(row.VoiceMinutes, cfg.CapVoiceMinutes)
	reaction := minInt(row.ReactionCount, cfg.CapReactions)
	login := minInt(row.LoginCount, 1)
	score := float64(msg)*cfg.WeightMessage +
		float64(voice)*cfg.WeightVoiceMinute +
		float64(reaction)*cfg.WeightReaction +
		float64(login)*cfg.WeightLogin
	if score < 0 {
		return 0
	}
	return int64(math.Round(score))
}

// bonusPct 等级积分加成百分比（level × 每级加成，封顶）。
func bonusPct(level int, cfg *resolvedConfig) float64 {
	pct := float64(level) * cfg.BonusPerLevelPct
	if pct > cfg.MaxBonusPct {
		pct = cfg.MaxBonusPct
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// pointsFor 得分对应发放积分：floor(score × rate × (1 + 加成%))。
func pointsFor(score int64, level int, cfg *resolvedConfig) int64 {
	if score <= 0 || cfg.PointsRate <= 0 {
		return 0
	}
	points := float64(score) * cfg.PointsRate * (1 + bonusPct(level, cfg)/100)
	return int64(math.Floor(points))
}

// defaultSetting 无配置行时的默认值（与 gorm default 标签保持一致）。
func defaultSetting() model.ActivitySetting {
	return model.ActivitySetting{
		ID: 1, Enabled: true, DayOffsetMinutes: 480,
		WeightMessage: 10, CapMessage: 60,
		WeightVoiceMinute: 2, CapVoiceMinutes: 240,
		WeightReaction: 1, CapReactions: 30,
		WeightLogin: 20,
		PointsRate:  0.1,
		LevelThresholdsJSON: "[]",
		BonusPerLevelPct:    2, MaxBonusPct: 100,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
