package cosmetics

// 跨包积分发放入口：供 activity（每日活跃奖励）等模块复用，
// 保证余额行锁、流水与雪花 ID 生成与本包内部路径完全一致。

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GrantPointsTx 在调用方事务内调整用户积分（delta 可负；余额不足返回
// "INSUFFICIENT_POINTS" 错误）。返回调整后余额。
// COSMETIC_POINTS_UPDATE 事件由调用方在事务提交后自行发布。
func GrantPointsTx(tx *gorm.DB, userID uuid.UUID, delta int64, reason, refType, refID string) (int64, error) {
	return adjustPointsIn(tx, userID, delta, reason, refType, refID)
}
