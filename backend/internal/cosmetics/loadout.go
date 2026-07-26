package cosmetics

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm/clause"
)

type loadoutView struct {
	Slots map[string]EquippedSlotView `json:"slots"`
}

// listLoadout GET /users/@me/cosmetics/loadout
func (h *api) listLoadout(c *gin.Context) {
	user := h.currentUser(c)
	c.JSON(http.StatusOK, h.buildLoadoutView(user.ID, false))
}

// equipSlot PUT /users/@me/cosmetics/loadout/:slot
func (h *api) equipSlot(c *gin.Context) {
	user := h.currentUser(c)
	slot := strings.TrimSpace(c.Param("slot"))
	if slot == "" {
		fail(c, http.StatusBadRequest, "INVALID_SLOT", "slot 不能为空")
		return
	}
	var input struct {
		ItemID string `json:"item_id" binding:"required"`
	}
	if !bind(c, &input) {
		return
	}
	itemID, err := parseSnowflakeString(input.ItemID)
	if err != nil || itemID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_ITEM", "item_id 非法")
		return
	}
	now := time.Now().UTC()
	if !h.userOwnsItem(user.ID, itemID, now) {
		fail(c, http.StatusForbidden, "NOT_OWNED", "未拥有该装扮或已过期")
		return
	}
	var item model.CosmeticItem
	if err := h.db().First(&item, "id = ?", itemID).Error; err != nil {
		notFound(c)
		return
	}
	var cat model.CosmeticCategory
	if err := h.db().First(&cat, "key = ?", item.CategoryKey).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "品类丢失")
		return
	}
	if cat.Slot != slot {
		fail(c, http.StatusBadRequest, "SLOT_MISMATCH", "装扮与槽位不匹配")
		return
	}
	row := model.UserCosmeticLoadout{
		UserID: user.ID, Slot: slot, ItemID: itemID, EquippedAt: now,
	}
	err = h.db().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "slot"}},
		DoUpdates: clause.AssignmentColumns([]string{"item_id", "equipped_at"}),
	}).Create(&row).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "装备失败")
		return
	}
	payload := h.buildLoadoutEventPayload(user.ID)
	h.publishLoadoutToUserGuilds(user.ID, payload)
	c.JSON(http.StatusOK, h.buildLoadoutView(user.ID, false))
}

// unequipSlot DELETE /users/@me/cosmetics/loadout/:slot
func (h *api) unequipSlot(c *gin.Context) {
	user := h.currentUser(c)
	slot := strings.TrimSpace(c.Param("slot"))
	if slot == "" {
		fail(c, http.StatusBadRequest, "INVALID_SLOT", "slot 不能为空")
		return
	}
	_ = h.db().Where("user_id = ? AND slot = ?", user.ID, slot).Delete(&model.UserCosmeticLoadout{}).Error
	payload := h.buildLoadoutEventPayload(user.ID)
	h.publishLoadoutToUserGuilds(user.ID, payload)
	c.JSON(http.StatusOK, h.buildLoadoutView(user.ID, false))
}

// getUserEquipped GET /users/:userID/cosmetics/equipped
func (h *api) getUserEquipped(c *gin.Context) {
	userID, ok := parseUUIDParam(c, "userID")
	if !ok {
		return
	}
	listMode := c.Query("full") != "1"
	c.JSON(http.StatusOK, h.buildLoadoutView(userID, listMode))
}

func (h *api) buildLoadoutView(userID uuid.UUID, listMode bool) loadoutView {
	resolved := ResolveEquippedForUsers(h.db(), []uuid.UUID{userID}, listMode)
	slots := resolved[userID]
	if slots == nil {
		slots = map[string]EquippedSlotView{}
	}
	return loadoutView{Slots: slots}
}

func jsonRaw(s string) []byte {
	if s == "" {
		return []byte("{}")
	}
	return []byte(s)
}

func (h *api) buildLoadoutEventPayload(userID uuid.UUID) gin.H {
	view := h.buildLoadoutView(userID, false)
	return gin.H{
		"user_id": userID.String(),
		"slots":   view.Slots,
	}
}
