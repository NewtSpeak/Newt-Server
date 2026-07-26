package activity

// 每日结算：把已过业务日界、尚未发放的活跃日按当时配置计分并发放装扮积分。
// 触发：启动补跑一轮 + 每小时一轮（仿 message/gc.go）；单实例部署，
// 幂等由 granted=false 行级 CAS + ledger refID=业务日双重保障。

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/cosmetics"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	settleInterval = time.Hour
	settleBatch    = 500
	// settleGrace 过界缓冲：让 tracker 末轮 flush（30s 周期）落库完毕再结算。
	settleGrace = 10 * time.Minute
)

func (s *service) settleLoop() {
	if _, err := s.runSettleOnce(); err != nil {
		log.Printf("activity: 启动结算补跑失败: %v", err)
	}
	ticker := time.NewTicker(settleInterval)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := s.runSettleOnce(); err != nil {
			log.Printf("activity: 定时结算失败: %v", err)
		}
	}
}

// runSettleOnce 结算所有 day < 当前业务日（含缓冲）且未发放的行，返回结算行数。
func (s *service) runSettleOnce() (int, error) {
	cfg := s.loadConfig()
	if !cfg.Enabled {
		return 0, nil
	}
	cutoff := bizDay(nowFunc().Add(-settleGrace), cfg.DayOffsetMinutes)
	settled := 0
	for {
		var rows []model.UserActivityDay
		err := s.db.Where("granted = false AND day < ?", cutoff).
			Order("day ASC, user_id ASC").Limit(settleBatch).Find(&rows).Error
		if err != nil {
			return settled, err
		}
		if len(rows) == 0 {
			return settled, nil
		}
		for _, row := range rows {
			if err := s.settleRow(row, cfg); err != nil {
				// 单行失败只记日志不中断（下一轮补跑），与 message GC 惯例一致。
				log.Printf("activity: 结算 user=%s day=%s 失败: %v", row.UserID, row.Day, err)
				continue
			}
			settled++
		}
		if len(rows) < settleBatch {
			return settled, nil
		}
	}
}

// settleRow 结算单个用户单日：计分 → 按结算前等级算加成发积分 → 更新汇总与等级。
func (s *service) settleRow(row model.UserActivityDay, cfg *resolvedConfig) error {
	score := scoreOf(row, cfg)
	now := nowFunc()

	var (
		points      int64
		balance     int64 = -1
		levelBefore int
		levelAfter  int
		totalAfter  int64
		skipped     bool
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 汇总行：确保存在后 FOR UPDATE 锁定（等级加成按结算前等级快照）。
		stat := model.UserActivityStat{UserID: row.UserID, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&stat).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&stat, "user_id = ?", row.UserID).Error; err != nil {
			return err
		}
		levelBefore = stat.Level
		points = pointsFor(score, levelBefore, cfg)

		// 幂等闸门：行级 CAS，重复结算/并发结算只有一边生效。
		res := tx.Model(&model.UserActivityDay{}).
			Where("user_id = ? AND day = ? AND granted = false", row.UserID, row.Day).
			Updates(map[string]any{
				"granted": true, "score": score,
				"granted_points": points, "granted_at": now, "updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			skipped = true
			return nil
		}

		if points > 0 {
			bal, err := cosmetics.GrantPointsTx(tx, row.UserID, points, "activity_daily", "activity", row.Day)
			if err != nil {
				return err
			}
			balance = bal
		}

		totalAfter = stat.TotalScore + score
		levelAfter = levelFor(totalAfter, cfg.Thresholds)
		return tx.Model(&model.UserActivityStat{}).Where("user_id = ?", row.UserID).
			Updates(map[string]any{"total_score": totalAfter, "level": levelAfter, "updated_at": now}).Error
	})
	if err != nil || skipped {
		return err
	}

	// 事务提交后发事件（丢失无害，客户端下次拉取补齐）。
	if points > 0 && balance >= 0 {
		s.publishToUser(row.UserID, eventbus.EventCosmeticPointsUpdate, gin.H{
			"balance": balance, "delta": points, "reason": "activity_daily",
		})
	}
	s.publishToUser(row.UserID, eventbus.EventActivityUpdate, gin.H{
		"day":            row.Day,
		"msg_count":      row.MsgCount,
		"voice_minutes":  row.VoiceMinutes,
		"reaction_count": row.ReactionCount,
		"login_count":    row.LoginCount,
		"score_estimate": score,
		"total_score":    totalAfter,
		"level":          levelAfter,
	})
	if levelAfter > levelBefore {
		s.publishToUser(row.UserID, eventbus.EventActivityLevelUp, gin.H{
			"level": levelAfter, "previous": levelBefore, "total_score": totalAfter,
		})
	}
	return nil
}

// settleUserDay 供集成测试直调的单行结算入口。
func (s *service) settleUserDay(userID uuid.UUID, day string) error {
	var row model.UserActivityDay
	if err := s.db.First(&row, "user_id = ? AND day = ?", userID, day).Error; err != nil {
		return err
	}
	return s.settleRow(row, s.loadConfig())
}
