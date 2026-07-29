package social

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

const (
	maxFriends     = 1000
	maxPending     = 200
	maxBlocked     = 1000
	maxNicknameLen = 32
)

type relationshipView struct {
	ID           uuid.UUID   `json:"id"`
	Type         string      `json:"type"`
	Nickname     string      `json:"nickname,omitempty"`
	User         userSummary `json:"user"`
	CreatedAt    time.Time   `json:"created_at"`
}

// listRelationships GET /users/@me/relationships
func (h *api) listRelationships(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var rows []model.Relationship
	// 我发起/持有的行
	_ = h.deps.DB.Where("user_id = ?", user.ID).Order("created_at DESC").Find(&rows).Error
	// 别人发给我的 pending
	var incoming []model.Relationship
	_ = h.deps.DB.Where("target_user_id = ? AND type = ?", user.ID, model.RelationshipPendingOutgoing).
		Order("created_at DESC").Find(&incoming).Error

	out := make([]relationshipView, 0, len(rows)+len(incoming))
	for _, r := range rows {
		summary, err := h.loadUserSummary(r.TargetUserID)
		if err != nil {
			continue
		}
		out = append(out, relationshipView{
			ID: r.ID, Type: r.Type, Nickname: r.Nickname,
			User: summary, CreatedAt: r.CreatedAt,
		})
	}
	for _, r := range incoming {
		summary, err := h.loadUserSummary(r.UserID)
		if err != nil {
			continue
		}
		out = append(out, relationshipView{
			ID: r.ID, Type: model.RelationshipPendingIncoming, Nickname: "",
			User: summary, CreatedAt: r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"relationships": out})
}

type createRelRequest struct {
	Username *string    `json:"username"`
	UserID   *uuid.UUID `json:"user_id"`
}

// postRelationship POST /users/@me/relationships — 发好友请求
func (h *api) postRelationship(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	var input createRelRequest
	if !bind(c, &input) {
		return
	}
	var target model.User
	var err error
	if input.UserID != nil {
		err = h.deps.DB.First(&target, "id = ? AND disabled_at IS NULL", *input.UserID).Error
	} else if input.Username != nil && strings.TrimSpace(*input.Username) != "" {
		target, err = h.findUserByUsername(strings.TrimSpace(*input.Username))
	} else {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "需要 username 或 user_id")
		return
	}
	if err != nil {
		fail(c, http.StatusNotFound, "USER_NOT_FOUND", "找不到该用户")
		return
	}
	if target.ID == me.ID {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "不能添加自己为好友")
		return
	}

	// 屏蔽
	if h.isBlocked(me.ID, target.ID) {
		fail(c, http.StatusForbidden, "PRIVACY_DENIED", "无法发送好友请求")
		return
	}
	// 已是好友
	if h.hasFriend(me.ID, target.ID) {
		fail(c, http.StatusConflict, "RELATIONSHIP_STATE_CONFLICT", "你们已经是好友")
		return
	}
	// 我已发出 pending
	var existing model.Relationship
	if h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?",
		me.ID, target.ID, model.RelationshipPendingOutgoing).First(&existing).Error == nil {
		summary, _ := h.loadUserSummary(target.ID)
		c.JSON(http.StatusOK, relationshipView{
			ID: existing.ID, Type: model.RelationshipPendingOutgoing,
			User: summary, CreatedAt: existing.CreatedAt,
		})
		return
	}
	// 对方已向我发出 → 互发即好友
	var reverse model.Relationship
	if h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?",
		target.ID, me.ID, model.RelationshipPendingOutgoing).First(&reverse).Error == nil {
		h.promoteToFriends(c, me.ID, target.ID, &reverse)
		return
	}

	// 隐私裁决
	priv := h.loadOrDefaultPrivacy(target.ID)
	if !h.canReceiveFriendRequest(me.ID, target.ID, priv) {
		fail(c, http.StatusForbidden, "PRIVACY_DENIED", "无法发送好友请求")
		return
	}
	if h.countType(me.ID, model.RelationshipFriend) >= maxFriends {
		fail(c, http.StatusConflict, "RELATIONSHIP_LIMIT", "好友数量已达上限")
		return
	}
	if h.countPendingTotal(me.ID) >= maxPending || h.countPendingTotal(target.ID) >= maxPending {
		fail(c, http.StatusConflict, "RELATIONSHIP_LIMIT", "未决请求数量已达上限")
		return
	}

	rel := model.Relationship{
		ID: uuid.New(), UserID: me.ID, TargetUserID: target.ID,
		Type: model.RelationshipPendingOutgoing,
	}
	if err := h.deps.DB.Create(&rel).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建请求失败")
		return
	}
	summary, _ := h.loadUserSummary(target.ID)
	// 推给对方：pending_incoming 投影 + 通知
	meSummary, _ := h.loadUserSummary(me.ID)
	h.publishToUser(target.ID, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		rel.ID, target.ID, me.ID, model.RelationshipPendingIncoming, "", userJSON(meSummary),
	))
	h.createNotification(target.ID, model.NotificationFriendRequest, map[string]any{
		"from_user_id": me.ID,
		"username":     me.Username,
		"display_name": me.DisplayName,
		"avatar_url":   me.AvatarURL,
	})
	// 推给自己多端
	h.publishToUser(me.ID, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		rel.ID, me.ID, target.ID, model.RelationshipPendingOutgoing, "", userJSON(summary),
	))
	c.JSON(http.StatusCreated, relationshipView{
		ID: rel.ID, Type: model.RelationshipPendingOutgoing,
		User: summary, CreatedAt: rel.CreatedAt,
	})
}

func (h *api) canReceiveFriendRequest(sender, receiver uuid.UUID, priv model.PrivacySettings) bool {
	switch priv.FriendRequestFrom {
	case model.FriendRequestEveryone:
		return true
	case model.FriendRequestNobody:
		return false
	case model.FriendRequestMutualFriends:
		return h.mutualFriendCount(sender, receiver) > 0
	case model.FriendRequestMutualGuilds:
		return h.mutualGuildCount(sender, receiver) > 0
	default:
		return h.mutualGuildCount(sender, receiver) > 0
	}
}

func (h *api) promoteToFriends(c *gin.Context, a, b uuid.UUID, pendingFromB *model.Relationship) {
	// a 与 b 成为好友；删除 pending（含 B→A）
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(
			"(user_id = ? AND target_user_id = ?) OR (user_id = ? AND target_user_id = ?)",
			a, b, b, a,
		).Where("type = ?", model.RelationshipPendingOutgoing).
			Delete(&model.Relationship{}).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		rows := []model.Relationship{
			{ID: uuid.New(), UserID: a, TargetUserID: b, Type: model.RelationshipFriend, CreatedAt: now, UpdatedAt: now},
			{ID: uuid.New(), UserID: b, TargetUserID: a, Type: model.RelationshipFriend, CreatedAt: now, UpdatedAt: now},
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "建立好友失败")
		return
	}
	sumA, _ := h.loadUserSummary(a)
	sumB, _ := h.loadUserSummary(b)
	// 查回 a 侧 friend 行
	var rowA model.Relationship
	_ = h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?", a, b, model.RelationshipFriend).First(&rowA).Error
	var rowB model.Relationship
	_ = h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?", b, a, model.RelationshipFriend).First(&rowB).Error

	h.publishToUser(a, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		rowA.ID, a, b, model.RelationshipFriend, "", userJSON(sumB),
	))
	h.publishToUser(b, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		rowB.ID, b, a, model.RelationshipFriend, "", userJSON(sumA),
	))
	h.createNotification(b, model.NotificationFriendAccept, map[string]any{
		"user_id": a, "username": sumA.Username, "display_name": sumA.DisplayName, "avatar_url": sumA.AvatarURL,
	})
	// 若有 pending 行被删，双方 REMOVE pending
	if pendingFromB != nil {
		h.publishToUser(a, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			pendingFromB.ID, a, b, model.RelationshipPendingIncoming, "", nil,
		))
		h.publishToUser(b, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			pendingFromB.ID, b, a, model.RelationshipPendingOutgoing, "", nil,
		))
	}
	c.JSON(http.StatusOK, relationshipView{
		ID: rowA.ID, Type: model.RelationshipFriend, User: sumB, CreatedAt: rowA.CreatedAt,
	})
}

type putRelRequest struct {
	Type string `json:"type" binding:"required"` // friend | blocked
}

// putRelationship PUT /users/@me/relationships/:userID — 接受或屏蔽
func (h *api) putRelationship(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	targetID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	var input putRelRequest
	if !bind(c, &input) {
		return
	}
	switch input.Type {
	case model.RelationshipFriend:
		h.acceptFriend(c, me.ID, targetID)
	case model.RelationshipBlocked:
		h.blockUser(c, me.ID, targetID)
	default:
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "type 只能是 friend 或 blocked")
	}
}

func (h *api) acceptFriend(c *gin.Context, me, target uuid.UUID) {
	var pending model.Relationship
	if err := h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?",
		target, me, model.RelationshipPendingOutgoing).First(&pending).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "没有来自该用户的好友请求")
		return
	}
	if h.countType(me, model.RelationshipFriend) >= maxFriends {
		fail(c, http.StatusConflict, "RELATIONSHIP_LIMIT", "好友数量已达上限")
		return
	}
	h.promoteToFriends(c, me, target, &pending)
}

func (h *api) blockUser(c *gin.Context, me, target uuid.UUID) {
	if me == target {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "不能屏蔽自己")
		return
	}
	var user model.User
	if h.deps.DB.First(&user, "id = ? AND disabled_at IS NULL", target).Error != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	if h.countType(me, model.RelationshipBlocked) >= maxBlocked {
		fail(c, http.StatusConflict, "RELATIONSHIP_LIMIT", "屏蔽数量已达上限")
		return
	}

	// 记录屏蔽前双方存在的关系类型，便于多端推送精确 REMOVE
	had := struct {
		meFriend, mePendingOut, meBlocked bool
		peerFriend, peerPendingOut        bool
	}{}
	var prior []model.Relationship
	_ = h.deps.DB.Where(
		"(user_id = ? AND target_user_id = ?) OR (user_id = ? AND target_user_id = ?)",
		me, target, target, me,
	).Find(&prior).Error
	for _, r := range prior {
		switch {
		case r.UserID == me && r.Type == model.RelationshipFriend:
			had.meFriend = true
		case r.UserID == me && r.Type == model.RelationshipPendingOutgoing:
			had.mePendingOut = true
		case r.UserID == me && r.Type == model.RelationshipBlocked:
			had.meBlocked = true
		case r.UserID == target && r.Type == model.RelationshipFriend:
			had.peerFriend = true
		case r.UserID == target && r.Type == model.RelationshipPendingOutgoing:
			// 对端发给我的 pending → 我侧投影为 pending_incoming
			had.peerPendingOut = true
		}
	}

	var blocked model.Relationship
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		// 清双方 friend 与相关 pending（保留我侧已有 blocked 以便复用）
		if err := tx.Where(
			"((user_id = ? AND target_user_id = ?) OR (user_id = ? AND target_user_id = ?)) AND type IN ?",
			me, target, target, me,
			[]string{model.RelationshipFriend, model.RelationshipPendingOutgoing},
		).Delete(&model.Relationship{}).Error; err != nil {
			return err
		}
		// 已有 blocked 则复用，否则新建（唯一索引 idx_rel_pair 保证 (me,target) 仅一行）
		if err := tx.Where("user_id = ? AND target_user_id = ? AND type = ?",
			me, target, model.RelationshipBlocked).First(&blocked).Error; err == nil {
			return nil
		}
		now := time.Now().UTC()
		blocked = model.Relationship{
			ID: uuid.New(), UserID: me, TargetUserID: target,
			Type: model.RelationshipBlocked, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&blocked).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "屏蔽失败")
		return
	}

	// 本端多端：清掉旧 friend / pending，再 ADD blocked
	if had.meFriend {
		h.publishToUser(me, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			uuid.Nil, me, target, model.RelationshipFriend, "", nil,
		))
	}
	if had.mePendingOut {
		h.publishToUser(me, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			uuid.Nil, me, target, model.RelationshipPendingOutgoing, "", nil,
		))
	}
	if had.peerPendingOut {
		// 我侧投影的 pending_incoming
		h.publishToUser(me, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			uuid.Nil, me, target, model.RelationshipPendingIncoming, "", nil,
		))
	}
	summary, _ := h.loadUserSummary(target)
	h.publishToUser(me, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		blocked.ID, me, target, model.RelationshipBlocked, "", userJSON(summary),
	))

	// 对端：好友/请求消失（不告知「被屏蔽」原因，BK.2 / BP.4）
	if had.peerFriend || had.meFriend {
		h.publishToUser(target, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			uuid.Nil, target, me, model.RelationshipFriend, "", nil,
		))
	}
	if had.peerPendingOut {
		h.publishToUser(target, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			uuid.Nil, target, me, model.RelationshipPendingOutgoing, "", nil,
		))
	}
	if had.mePendingOut {
		// 对端投影的 pending_incoming（我曾向对方发请求）
		h.publishToUser(target, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			uuid.Nil, target, me, model.RelationshipPendingIncoming, "", nil,
		))
	}

	c.JSON(http.StatusOK, relationshipView{
		ID: blocked.ID, Type: model.RelationshipBlocked, User: summary, CreatedAt: blocked.CreatedAt,
	})
}

// unblockAndRestoreFriend 解除拉黑并自动恢复双向好友。
func (h *api) unblockAndRestoreFriend(c *gin.Context, me, target uuid.UUID, blockedRow model.Relationship) {
	var rowA, rowB model.Relationship
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&blockedRow).Error; err != nil {
			return err
		}
		// 清掉可能残留的 pending，再写双向 friend
		if err := tx.Where(
			"((user_id = ? AND target_user_id = ?) OR (user_id = ? AND target_user_id = ?)) AND type IN ?",
			me, target, target, me,
			[]string{model.RelationshipPendingOutgoing, model.RelationshipFriend},
		).Delete(&model.Relationship{}).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		rowA = model.Relationship{
			ID: uuid.New(), UserID: me, TargetUserID: target,
			Type: model.RelationshipFriend, CreatedAt: now, UpdatedAt: now,
		}
		rowB = model.Relationship{
			ID: uuid.New(), UserID: target, TargetUserID: me,
			Type: model.RelationshipFriend, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&[]model.Relationship{rowA, rowB}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解除拉黑失败")
		return
	}

	sumMe, _ := h.loadUserSummary(me)
	sumPeer, _ := h.loadUserSummary(target)

	// 本端：去掉 blocked，加上 friend
	h.publishToUser(me, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
		blockedRow.ID, me, target, model.RelationshipBlocked, "", nil,
	))
	h.publishToUser(me, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		rowA.ID, me, target, model.RelationshipFriend, "", userJSON(sumPeer),
	))
	// 对端：恢复好友（不暴露曾被拉黑）
	h.publishToUser(target, eventbus.EventRelationshipAdd, eventbus.NewRelationshipPayload(
		rowB.ID, target, me, model.RelationshipFriend, "", userJSON(sumMe),
	))

	c.Status(http.StatusNoContent)
}

type patchRelRequest struct {
	Nickname *string `json:"nickname"`
}

// patchRelationship PATCH /users/@me/relationships/:userID — 备注
func (h *api) patchRelationship(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	targetID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	var input patchRelRequest
	if !bind(c, &input) {
		return
	}
	var rel model.Relationship
	if h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?",
		me.ID, targetID, model.RelationshipFriend).First(&rel).Error != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "不是好友")
		return
	}
	if input.Nickname != nil {
		nick := strings.TrimSpace(*input.Nickname)
		if utf8.RuneCountInString(nick) > maxNicknameLen {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "备注最长 32 字")
			return
		}
		rel.Nickname = nick
	}
	rel.UpdatedAt = time.Now().UTC()
	if err := h.deps.DB.Save(&rel).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新失败")
		return
	}
	summary, _ := h.loadUserSummary(targetID)
	h.publishToUser(me.ID, eventbus.EventRelationshipUpdate, eventbus.NewRelationshipPayload(
		rel.ID, me.ID, targetID, model.RelationshipFriend, rel.Nickname, userJSON(summary),
	))
	c.JSON(http.StatusOK, relationshipView{
		ID: rel.ID, Type: rel.Type, Nickname: rel.Nickname, User: summary, CreatedAt: rel.CreatedAt,
	})
}

// deleteRelationship DELETE /users/@me/relationships/:userID
// 语境：删好友 / 取消发出请求 / 忽略收到请求 / 解除屏蔽
func (h *api) deleteRelationship(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	targetID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "用户不存在")
		return
	}
	// 优先处理我侧行
	var mine model.Relationship
	errMine := h.deps.DB.Where("user_id = ? AND target_user_id = ?", me.ID, targetID).First(&mine).Error
	// 对方发给我的 pending
	var incoming model.Relationship
	errIn := h.deps.DB.Where("user_id = ? AND target_user_id = ? AND type = ?",
		targetID, me.ID, model.RelationshipPendingOutgoing).First(&incoming).Error

	if errMine == nil {
		switch mine.Type {
		case model.RelationshipFriend:
			// 删双向好友
			_ = h.deps.DB.Where(
				"((user_id = ? AND target_user_id = ?) OR (user_id = ? AND target_user_id = ?)) AND type = ?",
				me.ID, targetID, targetID, me.ID, model.RelationshipFriend,
			).Delete(&model.Relationship{}).Error
			h.publishToUser(me.ID, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
				mine.ID, me.ID, targetID, model.RelationshipFriend, "", nil,
			))
			h.publishToUser(targetID, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
				uuid.Nil, targetID, me.ID, model.RelationshipFriend, "", nil,
			))
			c.Status(http.StatusNoContent)
			return
		case model.RelationshipPendingOutgoing:
			// 取消发出
			_ = h.deps.DB.Delete(&mine).Error
			h.publishToUser(me.ID, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
				mine.ID, me.ID, targetID, model.RelationshipPendingOutgoing, "", nil,
			))
			h.publishToUser(targetID, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
				mine.ID, targetID, me.ID, model.RelationshipPendingIncoming, "", nil,
			))
			c.Status(http.StatusNoContent)
			return
		case model.RelationshipBlocked:
			// 解除拉黑：删除 blocked，并自动恢复双向好友（无需再加好友）
			h.unblockAndRestoreFriend(c, me.ID, targetID, mine)
			return
		}
	}
	if errIn == nil {
		// 忽略收到的请求（静默）
		_ = h.deps.DB.Delete(&incoming).Error
		h.publishToUser(me.ID, eventbus.EventRelationshipRemove, eventbus.NewRelationshipPayload(
			incoming.ID, me.ID, targetID, model.RelationshipPendingIncoming, "", nil,
		))
		// 发起方不通知（Server-16 BK.6）
		c.Status(http.StatusNoContent)
		return
	}
	fail(c, http.StatusNotFound, "NOT_FOUND", "没有与该用户的关系")
}
