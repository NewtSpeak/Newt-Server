package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 平台活跃度（Activity）=================
// 多维度实时累计每日活跃度（平台全局非 per-guild）；总活跃度决定等级；
// 每日结算按活跃度自动发放装扮积分（等级提供加成）。
// 业务日按平台配置的 UTC 偏移切分（默认 +480 分钟 = 北京时间）。

// UserActivityDay 用户每日活跃度累计。各维度存原始计数不截断（上限在结算时应用，
// 配置改动可回溯）；Score/Granted* 由结算器写入，落库为当时配置下的快照。
type UserActivityDay struct {
	UserID uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	// Day 业务日（"2006-01-02"，按 ActivitySetting.DayOffsetMinutes 切分）。
	Day           string `gorm:"size:10;primaryKey;index:idx_activity_day_date" json:"day"`
	MsgCount      int    `gorm:"not null;default:0" json:"msg_count"`
	VoiceMinutes  int    `gorm:"not null;default:0" json:"voice_minutes"`
	ReactionCount int    `gorm:"not null;default:0" json:"reaction_count"`
	LoginCount    int    `gorm:"not null;default:0" json:"login_count"`
	// Score 结算时按当时配置计算的当日总分（结算前为 0，展示端实时口径由 API 现算）。
	Score int64 `gorm:"not null;default:0" json:"score"`
	// Granted 是否已结算发放；partial index 让结算扫描只看未结算行。
	Granted       bool       `gorm:"not null;default:false;index:idx_activity_day_pending,where:granted = false" json:"granted"`
	GrantedPoints int64      `gorm:"not null;default:0" json:"granted_points"`
	GrantedAt     *time.Time `json:"granted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// UserActivityStat 用户活跃度汇总（仅含已结算日；今日部分由 API 实时叠加展示）。
type UserActivityStat struct {
	UserID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	TotalScore int64     `gorm:"not null;default:0" json:"total_score"`
	Level      int       `gorm:"not null;default:0;index:idx_activity_stat_level" json:"level"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ActivitySetting 平台活跃度配置（单行，ID 固定 1，范式同 ScreenPlatformSetting）。
type ActivitySetting struct {
	ID      int  `gorm:"primaryKey" json:"id"`
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// DayOffsetMinutes 业务日相对 UTC 的偏移分钟数（480 = UTC+8 北京时间）。
	DayOffsetMinutes  int     `gorm:"not null;default:480" json:"day_offset_minutes"`
	WeightMessage     float64 `gorm:"not null;default:10" json:"weight_message"`
	CapMessage        int     `gorm:"not null;default:60" json:"cap_message"` // 计分条数/日
	WeightVoiceMinute float64 `gorm:"not null;default:2" json:"weight_voice_minute"`
	CapVoiceMinutes   int     `gorm:"not null;default:240" json:"cap_voice_minutes"`
	WeightReaction    float64 `gorm:"not null;default:1" json:"weight_reaction"`
	CapReactions      int     `gorm:"not null;default:30" json:"cap_reactions"`
	WeightLogin       float64 `gorm:"not null;default:20" json:"weight_login"` // 登录日上限恒为 1
	// PointsRate 分数→积分兑换率：points = floor(score × rate × (1 + 加成%)）。
	PointsRate float64 `gorm:"not null;default:0.1" json:"points_rate"`
	// LevelThresholdsJSON 各级累计总分门槛数组（jsonb，[100,300,...]，
	// 下标 i = 达到 Lv(i+1) 门槛，须严格递增）；空/非法时回退代码内默认曲线。
	LevelThresholdsJSON string `gorm:"type:jsonb;not null;default:'[]'" json:"level_thresholds_json"`
	// BonusPerLevelPct 每级积分加成百分比（总加成 = level × pct，封顶 MaxBonusPct）。
	BonusPerLevelPct float64   `gorm:"not null;default:2" json:"bonus_per_level_pct"`
	MaxBonusPct      float64   `gorm:"not null;default:100" json:"max_bonus_pct"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func init() {
	Register(
		&UserActivityDay{},
		&UserActivityStat{},
		&ActivitySetting{},
	)
}
