package cosmetics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

type acquireRequest struct {
	TargetType string `json:"target_type" binding:"required"` // item | bundle
	TargetID   string `json:"target_id" binding:"required"`
}

// claimFree POST /users/@me/cosmetics/claim — 仅 price_points=0
func (h *api) claimFree(c *gin.Context) {
	h.acquire(c, true)
}

// purchaseWithPoints POST /users/@me/cosmetics/purchase
func (h *api) purchaseWithPoints(c *gin.Context) {
	h.acquire(c, false)
}

func (h *api) acquire(c *gin.Context, freeOnly bool) {
	user := h.currentUser(c)
	var input acquireRequest
	if !bind(c, &input) {
		return
	}
	targetType := model.CosmeticOrderTarget(input.TargetType)
	if targetType != model.CosmeticTargetItem && targetType != model.CosmeticTargetBundle {
		fail(c, http.StatusBadRequest, "INVALID_TARGET", "target_type 须为 item 或 bundle")
		return
	}
	targetID, err := parseSnowflakeString(input.TargetID)
	if err != nil || targetID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_TARGET", "target_id 非法")
		return
	}
	now := time.Now().UTC()

	var granted []int64
	var price int
	var balanceAfter int64 = -1

	err = h.db().Transaction(func(tx *gorm.DB) error {
		if targetType == model.CosmeticTargetItem {
			var item model.CosmeticItem
			if err := tx.First(&item, "id = ?", targetID).Error; err != nil {
				return fmt.Errorf("NOT_FOUND")
			}
			if !itemAvailableNow(item, now) {
				return fmt.Errorf("NOT_AVAILABLE")
			}
			price = item.PricePoints
			if freeOnly && price > 0 {
				return fmt.Errorf("NOT_FREE")
			}
			if !freeOnly && price == 0 {
				// 免费商品请走 claim
				return fmt.Errorf("USE_CLAIM")
			}
			// 已拥有
			var existing model.UserCosmeticInventory
			if err := tx.Where("user_id = ? AND item_id = ?", user.ID, item.ID).First(&existing).Error; err == nil {
				if inventoryActive(existing, now) {
					return fmt.Errorf("ALREADY_OWNED")
				}
			}
			if price > 0 {
				bal, err := h.adjustPoints(tx, user.ID, -int64(price), "purchase_item", "item", strID(item.ID))
				if err != nil {
					return err
				}
				balanceAfter = bal
			}
			src := model.CosmeticSourceClaim
			if price > 0 {
				src = model.CosmeticSourcePoints
			}
			order := model.CosmeticOrder{
				ID: h.ids.Next(), UserID: user.ID, TargetType: model.CosmeticTargetItem,
				TargetID: item.ID, PricePoints: price, Status: model.CosmeticOrderCompleted, CreatedAt: now,
			}
			if err := tx.Create(&order).Error; err != nil {
				return err
			}
			if _, err := h.grantItem(tx, user.ID, item.ID, src, strID(order.ID), nil); err != nil {
				return err
			}
			granted = []int64{item.ID}
			return nil
		}

		// bundle
		var bundle model.CosmeticBundle
		if err := tx.First(&bundle, "id = ?", targetID).Error; err != nil {
			return fmt.Errorf("NOT_FOUND")
		}
		if !bundleAvailableNow(bundle, now) {
			return fmt.Errorf("NOT_AVAILABLE")
		}
		price = bundle.PricePoints
		if freeOnly && price > 0 {
			return fmt.Errorf("NOT_FREE")
		}
		if !freeOnly && price == 0 {
			return fmt.Errorf("USE_CLAIM")
		}
		var links []model.CosmeticBundleItem
		if err := tx.Where("bundle_id = ?", bundle.ID).Find(&links).Error; err != nil {
			return err
		}
		if len(links) == 0 {
			return fmt.Errorf("EMPTY_BUNDLE")
		}
		// 是否全部已拥有
		allOwned := true
		for _, l := range links {
			var inv model.UserCosmeticInventory
			err := tx.Where("user_id = ? AND item_id = ?", user.ID, l.ItemID).First(&inv).Error
			if err != nil || !inventoryActive(inv, now) {
				allOwned = false
				break
			}
		}
		if allOwned {
			return fmt.Errorf("ALREADY_OWNED")
		}
		if price > 0 {
			bal, err := h.adjustPoints(tx, user.ID, -int64(price), "purchase_bundle", "bundle", strID(bundle.ID))
			if err != nil {
				return err
			}
			balanceAfter = bal
		}
		order := model.CosmeticOrder{
			ID: h.ids.Next(), UserID: user.ID, TargetType: model.CosmeticTargetBundle,
			TargetID: bundle.ID, PricePoints: price, Status: model.CosmeticOrderCompleted, CreatedAt: now,
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		src := model.CosmeticSourceBundle
		if price == 0 {
			src = model.CosmeticSourceClaim
		}
		for _, l := range links {
			if _, err := h.grantItem(tx, user.ID, l.ItemID, src, strID(order.ID), nil); err != nil {
				return err
			}
			granted = append(granted, l.ItemID)
		}
		return nil
	})

	if err != nil {
		switch err.Error() {
		case "NOT_FOUND":
			notFound(c)
		case "NOT_AVAILABLE":
			fail(c, http.StatusBadRequest, "NOT_AVAILABLE", "商品未上架或已过期")
		case "NOT_FREE":
			fail(c, http.StatusBadRequest, "NOT_FREE", "该商品需积分兑换")
		case "USE_CLAIM":
			fail(c, http.StatusBadRequest, "USE_CLAIM", "免费商品请使用 claim 接口")
		case "ALREADY_OWNED":
			fail(c, http.StatusConflict, "ALREADY_OWNED", "已拥有")
		case "EMPTY_BUNDLE":
			fail(c, http.StatusBadRequest, "EMPTY_BUNDLE", "捆绑包为空")
		case "INSUFFICIENT_POINTS":
			fail(c, http.StatusPaymentRequired, "INSUFFICIENT_POINTS", "积分不足")
		default:
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "兑换失败")
		}
		return
	}

	ids := make([]string, 0, len(granted))
	for _, id := range granted {
		ids = append(ids, strID(id))
	}
	h.publishToUser(user.ID, eventbus.EventCosmeticInventoryUpdate, gin.H{
		"item_ids": ids, "source": map[bool]string{true: "claim", false: "points"}[freeOnly],
	})
	if balanceAfter >= 0 {
		h.publishToUser(user.ID, eventbus.EventCosmeticPointsUpdate, gin.H{"balance": balanceAfter})
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "granted_item_ids": ids, "price_points": price,
		"balance": balanceAfter,
	})
}
