package moderation

import (
	"crypto/rand"
	"errors"
	"math/big"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
	"gorm.io/gorm"
)

// 邀请码字母表：去掉易混淆字符（0O1lI）。
const inviteAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
const inviteCodeLength = 10

// errInviteExhausted 并发竞争下邀请次数被用尽（对外映射为 404，不泄露信息）。
var errInviteExhausted = errors.New("邀请使用次数已用尽")

// newInviteCode 生成加密随机短码。
func newInviteCode() (string, error) {
	code := make([]byte, inviteCodeLength)
	max := big.NewInt(int64(len(inviteAlphabet)))
	for i := range code {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		code[i] = inviteAlphabet[index.Int64()]
	}
	return string(code), nil
}

type createInviteRequest struct {
	// TTLSeconds 邀请有效期（秒），可选；0 或缺省表示不过期。
	TTLSeconds int `json:"ttl_seconds" binding:"omitempty,min=60,max=2592000"`
	// MaxUses 最大使用次数，可选；0 或缺省表示不限次（docs Owl-Desktop 02 FR-15）。
	MaxUses int `json:"max_uses" binding:"omitempty,min=1,max=10000"`
}

// createInvite POST /guilds/{gid}/invites：生成邀请短码（需 CREATE_INSTANT_INVITE）。
func (h *api) createInvite(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !ctx.SystemAdmin && !ctx.Has(rbac.CreateInstantInvite) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有创建邀请的权限")
		return
	}
	var input createInviteRequest
	if err := c.ShouldBindJSON(&input); err != nil && err.Error() != "EOF" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	var expiresAt *time.Time
	if input.TTLSeconds > 0 {
		expiry := time.Now().UTC().Add(time.Duration(input.TTLSeconds) * time.Second)
		expiresAt = &expiry
	}
	var invite model.Invite
	// 短码冲突概率极低，仍重试数次兜底唯一索引冲突。
	for attempt := 0; attempt < 3; attempt++ {
		code, err := newInviteCode()
		if err != nil {
			fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "生成邀请码失败")
			return
		}
		invite = model.Invite{ID: uuid.New(), GuildID: ctx.Guild.ID, Code: code, CreatedBy: user.ID, ExpiresAt: expiresAt, MaxUses: input.MaxUses}
		if err := h.deps.DB.Create(&invite).Error; err == nil {
			h.audit(ctx, user, "moderation.invite_create", "invite", invite.Code, map[string]any{"expires_at": expiresAt, "max_uses": invite.MaxUses})
			c.JSON(http.StatusCreated, invite)
			return
		}
	}
	fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建邀请失败")
}

// ResolveActiveInvite 按短码解析邀请并校验有效性（join/预览/凭码注册共用）：
//   - 不存在：返回 (zero, StatusNotFound)（不泄露信息）；
//   - 已过期：返回 (invite, StatusNotFound)——状态码与不存在一致（防泄露），
//     但返回已加载的记录（ID 非零），需要区分两者的调用方（如 signup）可据此判断；
//   - 已用尽（max_uses>0 且 uses≥max_uses）：返回 (invite, StatusGone)；
//   - 有效：返回 (invite, 0)。
func ResolveActiveInvite(db *gorm.DB, code string) (model.Invite, int) {
	var invite model.Invite
	if err := db.First(&invite, "code = ?", code).Error; err != nil {
		return invite, http.StatusNotFound
	}
	now := time.Now().UTC()
	if invite.ExpiresAt != nil && !invite.ExpiresAt.After(now) {
		return invite, http.StatusNotFound
	}
	if invite.MaxUses > 0 && invite.Uses >= invite.MaxUses {
		return invite, http.StatusGone
	}
	return invite, 0
}

// ConsumeInviteUse 原子消耗一次邀请使用次数（仅真正新加入时调用；与成员创建同事务）。
// 并发用尽（uses 已达 max_uses）时返回 false。
func ConsumeInviteUse(tx *gorm.DB, inviteID uuid.UUID) (bool, error) {
	result := tx.Model(&model.Invite{}).
		Where("id = ? AND (max_uses = 0 OR uses < max_uses)", inviteID).
		Update("uses", gorm.Expr("uses + 1"))
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// inviteInfo GET /invites/{code}：登录用户预览邀请（服务器名称/图标占位/成员数，
// docs Owl-Desktop 02 FR-08）。失效 404、超次 410，均不泄露服务器信息。
func (h *api) inviteInfo(c *gin.Context) {
	invite, status := ResolveActiveInvite(h.deps.DB, c.Param("code"))
	if status == http.StatusNotFound {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
		return
	}
	if status == http.StatusGone {
		fail(c, http.StatusGone, "INVITE_EXHAUSTED", "邀请已失效")
		return
	}
	var guild model.Guild
	if err := h.deps.DB.First(&guild, "id = ?", invite.GuildID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
		return
	}
	var memberCount int64
	h.deps.DB.Model(&model.Member{}).Where("guild_id = ?", guild.ID).Count(&memberCount)
	c.JSON(http.StatusOK, gin.H{
		"code":       invite.Code,
		"expires_at": invite.ExpiresAt,
		"max_uses":   invite.MaxUses,
		"uses":       invite.Uses,
		"guild": gin.H{
			"id":           guild.ID,
			"name":         guild.Name,
			"icon":         nil, // 图标占位：服务器图标上传未实现（Owl-Desktop docs 02 §8-9）
			"member_count": memberCount,
		},
	})
}

// joinByInvite POST /invites/{code}/join：登录用户凭邀请码加入服务器成为成员。
// 被 Ban 用户拒绝加入（docs 12 AG.4）；过期/超次统一 404 不泄露信息。
func (h *api) joinByInvite(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	invite, status := ResolveActiveInvite(h.deps.DB, c.Param("code"))
	if status != 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
		return
	}
	var ban model.GuildBan
	if err := h.deps.DB.First(&ban, "guild_id = ? AND user_id = ?", invite.GuildID, user.ID).Error; err == nil {
		fail(c, http.StatusForbidden, "BANNED", "你已被该服务器封禁，无法加入")
		return
	}
	var existing model.Member
	if err := h.deps.DB.First(&existing, "guild_id = ? AND user_id = ?", invite.GuildID, user.ID).Error; err == nil {
		c.JSON(http.StatusOK, existing)
		return
	}
	member := model.Member{ID: uuid.New(), GuildID: invite.GuildID, UserID: user.ID}
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		consumed, err := ConsumeInviteUse(tx, invite.ID)
		if err != nil {
			return err
		}
		if !consumed {
			return errInviteExhausted
		}
		return tx.Create(&member).Error
	})
	if err != nil {
		if errors.Is(err, errInviteExhausted) {
			fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
			return
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(http.StatusOK, member)
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "加入服务器失败")
		return
	}
	actorID := user.ID
	guildID := invite.GuildID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID:    &actorID,
		ActorType:  "user",
		GuildID:    &guildID,
		Action:     "moderation.member_join",
		TargetType: "member",
		TargetID:   member.ID.String(),
		Detail:     map[string]any{"invite_code": invite.Code},
	})
	// 新成员定向发 GUILD_CREATE 全量快照，全服广播 GUILD_MEMBER_ADD（docs 14 §3.2）。
	if payload, err := snapshot.NewGuildCreatePayload(h.deps.DB, user, guildID); err == nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type: eventbus.EventGuildCreate, GuildID: &guildID,
			UserIDs: []uuid.UUID{user.ID}, Payload: payload,
		})
	}
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventbus.EventGuildMemberAdd, GuildID: &guildID,
		Payload: eventbus.NewGuildMemberAddPayload(member, user),
	})
	c.JSON(http.StatusCreated, member)
}
