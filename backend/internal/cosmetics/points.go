package cosmetics

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// getMyPoints GET /users/@me/cosmetics/points
func (h *api) getMyPoints(c *gin.Context) {
	user := h.currentUser(c)
	var row model.UserPoints
	if err := h.db().First(&row, "user_id = ?", user.ID).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"balance": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"balance": row.Balance, "updated_at": row.UpdatedAt})
}

// exchangeCurrencyStub POST /users/@me/cosmetics/points/exchange
// 服务器货币→积分预留入口，本期不实现。
func (h *api) exchangeCurrencyStub(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": gin.H{
			"code":    "NOT_IMPLEMENTED",
			"message": "服务器货币兑换积分尚未开放",
		},
		"enabled": false,
	})
}

// adminGrantPoints POST /admin/cosmetics/points/grant
func (h *api) adminGrantPoints(c *gin.Context) {
	var input struct {
		UserID string `json:"user_id" binding:"required"`
		Amount int64  `json:"amount" binding:"required"`
		Reason string `json:"reason"`
	}
	if !bind(c, &input) {
		return
	}
	if input.Amount == 0 {
		fail(c, http.StatusBadRequest, "INVALID_AMOUNT", "amount 不能为 0")
		return
	}
	userID, err := uuid.Parse(input.UserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_USER", "user_id 非法")
		return
	}
	reason := input.Reason
	if reason == "" {
		reason = "admin_grant"
	}
	var balance int64
	err = h.db().Transaction(func(tx *gorm.DB) error {
		var e error
		balance, e = h.adjustPoints(tx, userID, input.Amount, reason, "admin", "")
		return e
	})
	if err != nil {
		if err.Error() == "INSUFFICIENT_POINTS" {
			fail(c, http.StatusBadRequest, "INSUFFICIENT_POINTS", "扣减后余额不能为负")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "调整积分失败")
		return
	}
	h.publishToUser(userID, eventbus.EventCosmeticPointsUpdate, gin.H{"balance": balance})
	c.JSON(http.StatusOK, gin.H{"balance": balance, "user_id": userID.String()})
}

// listMyLedger GET /users/@me/cosmetics/points/ledger （可选，便于调试）
func (h *api) listMyLedger(c *gin.Context) {
	user := h.currentUser(c)
	var rows []model.UserPointsLedger
	_ = h.db().Where("user_id = ?", user.ID).Order("created_at DESC").Limit(50).Find(&rows).Error
	c.JSON(http.StatusOK, gin.H{"ledger": rows})
}

// ensureUserPointsOnRead 可选懒创建
func (h *api) ensureUserPointsOnRead(userID uuid.UUID) model.UserPoints {
	var row model.UserPoints
	if err := h.db().First(&row, "user_id = ?", userID).Error; err == nil {
		return row
	}
	row = model.UserPoints{UserID: userID, Balance: 0, UpdatedAt: time.Now().UTC()}
	_ = h.db().Create(&row).Error
	return row
}
