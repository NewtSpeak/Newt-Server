package restriction

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// api Restriction REST 处理器。
type api struct {
	deps appdeps.Deps
	svc  *service
}

// restrictionView 对外展示结构；reason / created_by 按观众身份裁剪（docs 12 AM）。
type restrictionView struct {
	ID           uuid.UUID  `json:"id"`
	GuildID      uuid.UUID  `json:"guild_id"`
	TargetUserID uuid.UUID  `json:"target_user_id"`
	Scope        string     `json:"scope"`
	ChannelID    *uuid.UUID `json:"channel_id,omitempty"`
	Deny         DenyFlags  `json:"deny"`
	Kind         string     `json:"kind"`
	Reason       string     `json:"reason,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at"`
	CreatedBy    *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LiftedAt     *time.Time `json:"lifted_at,omitempty"`
	LiftedBy     *uuid.UUID `json:"lifted_by,omitempty"`
	Active       bool       `json:"active"`
}

func viewOf(r model.Restriction, includeReason, includeActor bool, now time.Time) restrictionView {
	view := restrictionView{
		ID:           r.ID,
		GuildID:      r.GuildID,
		TargetUserID: r.TargetUserID,
		Scope:        r.Scope,
		ChannelID:    r.ChannelID,
		Deny:         denyOf(r),
		Kind:         r.Kind,
		ExpiresAt:    r.ExpiresAt,
		CreatedAt:    r.CreatedAt,
		LiftedAt:     r.LiftedAt,
		Active:       r.ActiveAt(now),
	}
	if includeReason {
		view.Reason = r.Reason
	}
	if includeActor {
		createdBy := r.CreatedBy
		view.CreatedBy = &createdBy
		view.LiftedBy = r.LiftedBy
	}
	return view
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func failRule(c *gin.Context, status int, err *RuleError) {
	fail(c, status, err.Code, err.Message)
}

// guildCtx 解析 guildID 并加载当前用户权限上下文；不可见统一 404。
func (h *api) guildCtx(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	return ctx, user, true
}

// isManager 是否具备限制管理权（系统管任意；本服持 MODERATE_MEMBERS，含服管/所有者短路）。
// 协管路径（docs 12 AK.2 本语音频道 speak_voice）见 comod.go，在 create/manageableRecord 中单独放行。
func isManager(ctx *perms.GuildContext) bool {
	return ctx.SystemAdmin || ctx.Has(rbac.ModerateMembers)
}

// targetInfo 加载目标成员的层级信息；目标非本服成员返回 false。
func (h *api) targetInfo(ctx *perms.GuildContext, targetUserID uuid.UUID) (Target, bool) {
	var member model.Member
	if err := h.deps.DB.First(&member, "guild_id = ? AND user_id = ?", ctx.Guild.ID, targetUserID).Error; err != nil {
		return Target{}, false
	}
	var highest int
	h.deps.DB.Raw(`SELECT COALESCE(MAX(roles.position), 0) FROM roles JOIN member_roles ON member_roles.role_id = roles.id WHERE member_roles.member_id = ?`, member.ID).Scan(&highest)
	return Target{UserID: targetUserID, IsOwner: ctx.Guild.OwnerUserID == targetUserID, HighestRole: highest}, true
}

func (h *api) actorOf(ctx *perms.GuildContext, user model.User) Actor {
	return Actor{UserID: user.ID, SystemAdmin: ctx.SystemAdmin, Owner: ctx.Owner, HighestRole: ctx.HighestRole}
}

type createRequest struct {
	TargetUserID string     `json:"target_user_id" binding:"required"`
	Scope        string     `json:"scope" binding:"required"`
	ChannelID    *string    `json:"channel_id"`
	Deny         DenyFlags  `json:"deny"`
	Kind         string     `json:"kind" binding:"required"`
	Reason       string     `json:"reason"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

// create POST /guilds/{gid}/restrictions（docs 12 §4.1）。
func (h *api) create(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	var input createRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	targetUserID, err := uuid.Parse(input.TargetUserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "target_user_id 无效")
		return
	}
	scope, kind := Scope(input.Scope), Kind(input.Kind)
	if !ValidKind(kind) {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "kind 必须是 SANCTION 或 CHANNEL_BAN")
		return
	}
	if ruleErr := ValidateScopeDeny(scope, input.Deny); ruleErr != nil {
		failRule(c, http.StatusBadRequest, ruleErr)
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	// reason 强制策略按服级配置（docs 08 AI.2/§8-9：默认强制，系统管可经
	// PATCH /guilds/{gid} 的 restriction_reason_required 关闭）。
	if input.Reason == "" && ctx.Guild.RestrictionReasonRequired {
		fail(c, http.StatusBadRequest, "REASON_REQUIRED", "必须填写限制原因")
		return
	}
	if utf8.RuneCountInString(input.Reason) > MaxReasonLength {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "reason 不能超过 512 个字符")
		return
	}
	now := time.Now().UTC()
	if ruleErr := ValidateDuration(kind, input.ExpiresAt, now, ctx.SystemAdmin); ruleErr != nil {
		failRule(c, http.StatusBadRequest, ruleErr)
		return
	}
	channelID, ok := h.resolveChannel(c, ctx, scope, input.ChannelID)
	if !ok {
		return
	}
	// 权限：管理者全量放行；否则走协管路径（docs 12 AK.2：仅本语音频道 speak_voice 快捷禁说）。
	if !isManager(ctx) && !coModCanCreate(h.deps.DB, user.ID, scope, channelID, input.Deny, kind) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有创建限制的权限")
		return
	}
	target, found := h.targetInfo(ctx, targetUserID)
	if !found {
		fail(c, http.StatusNotFound, "NOT_FOUND", "目标成员不存在")
		return
	}
	if ruleErr := CheckTarget(h.actorOf(ctx, user), target); ruleErr != nil {
		status := http.StatusForbidden
		if ruleErr.Code == "CANNOT_RESTRICT_SELF" {
			status = http.StatusBadRequest
		}
		failRule(c, status, ruleErr)
		return
	}
	deny := ApplyImplications(input.Deny)
	record := model.Restriction{
		ID:              uuid.New(),
		GuildID:         ctx.Guild.ID,
		TargetUserID:    targetUserID,
		Scope:           string(scope),
		ChannelID:       channelID,
		DenyViewText:    deny.ViewText,
		DenySendText:    deny.SendText,
		DenyListenVoice: deny.ListenVoice,
		DenySpeakVoice:  deny.SpeakVoice,
		Kind:            string(kind),
		Reason:          input.Reason,
		ExpiresAt:       input.ExpiresAt,
		CreatedBy:       user.ID,
	}
	if err := h.deps.DB.Create(&record).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建限制失败")
		return
	}
	publishChange(h.deps.DB, h.deps.Bus, eventbus.EventRestrictionCreate, record, now)
	afterSnap := map[string]any{
		"id": record.ID.String(), "guild_id": record.GuildID.String(),
		"target_user_id": record.TargetUserID.String(),
		"scope": record.Scope, "kind": record.Kind, "reason": record.Reason,
		"deny_view_text": record.DenyViewText, "deny_send_text": record.DenySendText,
		"deny_listen_voice": record.DenyListenVoice, "deny_speak_voice": record.DenySpeakVoice,
		"created_by": record.CreatedBy.String(),
	}
	if record.ChannelID != nil {
		afterSnap["channel_id"] = record.ChannelID.String()
	}
	if record.ExpiresAt != nil {
		afterSnap["expires_at"] = record.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	h.audit(ctx, user, "restriction.create", record.ID.String(), map[string]any{
		"target_user_id": targetUserID,
		"scope":          record.Scope,
		"channel_id":     record.ChannelID,
		"deny":           deny,
		"kind":           record.Kind,
		"reason":         record.Reason,
		"expires_at":     record.ExpiresAt,
		"after":          afterSnap,
	})
	c.JSON(http.StatusCreated, viewOf(record, true, true, now))
}

// resolveChannel 校验 scope 与 channel_id 的配套关系及频道类型匹配。
func (h *api) resolveChannel(c *gin.Context, ctx *perms.GuildContext, scope Scope, raw *string) (*uuid.UUID, bool) {
	channelScoped := scope == ScopeTextChannel || scope == ScopeVoiceChannel
	if !channelScoped {
		if raw != nil && *raw != "" {
			fail(c, http.StatusBadRequest, "INVALID_SCOPE_DENY", "全服作用域不能携带 channel_id")
			return nil, false
		}
		return nil, true
	}
	if raw == nil || *raw == "" {
		fail(c, http.StatusBadRequest, "INVALID_SCOPE_DENY", "单频道作用域必须携带 channel_id")
		return nil, false
	}
	channelID, err := uuid.Parse(*raw)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "channel_id 无效")
		return nil, false
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ? AND guild_id = ?", channelID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return nil, false
	}
	if (scope == ScopeTextChannel && channel.Type != model.ChannelText) ||
		(scope == ScopeVoiceChannel && channel.Type != model.ChannelVoice) {
		fail(c, http.StatusBadRequest, "INVALID_SCOPE_DENY", "作用域与频道类型不匹配")
		return nil, false
	}
	return &channelID, true
}

// list GET /guilds/{gid}/restrictions?user_id&channel_id&active&scope（管理视角）。
func (h *api) list(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	if !isManager(ctx) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "没有查看限制列表的权限")
		return
	}
	now := time.Now().UTC()
	query := h.deps.DB.Where("guild_id = ?", ctx.Guild.ID)
	if raw := c.Query("user_id"); raw != "" {
		userID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_id 无效")
			return
		}
		query = query.Where("target_user_id = ?", userID)
	}
	if raw := c.Query("channel_id"); raw != "" {
		channelID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "channel_id 无效")
			return
		}
		query = query.Where("channel_id = ?", channelID)
	}
	if raw := c.Query("scope"); raw != "" {
		if !ValidScope(Scope(raw)) {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "scope 无效")
			return
		}
		query = query.Where("scope = ?", raw)
	}
	switch c.Query("active") {
	case "":
	case "true":
		query = query.Where("lifted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", now)
	case "false":
		query = query.Where("lifted_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= ?)", now)
	default:
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "active 只能是 true 或 false")
		return
	}
	var rows []model.Restriction
	if err := query.Order("created_at DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取限制列表失败")
		return
	}
	views := make([]restrictionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewOf(row, true, true, now))
	}
	c.JSON(http.StatusOK, views)
}

// detail GET /guilds/{gid}/restrictions/{id}；路径为 @me 时返回当事人自己的生效限制。
// 无权限查看时统一 404（docs 12 §11 / 06 议题 8）。
func (h *api) detail(c *gin.Context) {
	if c.Param("restrictionID") == "@me" {
		h.mine(c)
		return
	}
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	restrictionID, err := uuid.Parse(c.Param("restrictionID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "限制记录不存在")
		return
	}
	var record model.Restriction
	if err := h.deps.DB.First(&record, "id = ? AND guild_id = ?", restrictionID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "限制记录不存在")
		return
	}
	now := time.Now().UTC()
	switch {
	case isManager(ctx):
		// 有权管理者可见完整记录（AM.3）。
		c.JSON(http.StatusOK, viewOf(record, true, true, now))
	case record.TargetUserID == user.ID:
		// 当事人可见 reason / expires_at / 维度，但不暴露操作者（AM.1）。
		c.JSON(http.StatusOK, viewOf(record, true, false, now))
	default:
		fail(c, http.StatusNotFound, "NOT_FOUND", "限制记录不存在")
	}
}

// mine GET /guilds/{gid}/restrictions/@me：当事人查看自己生效中的限制（docs 12 AJ）。
func (h *api) mine(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	now := time.Now().UTC()
	var rows []model.Restriction
	err := h.deps.DB.
		Where("guild_id = ? AND target_user_id = ? AND lifted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", ctx.Guild.ID, user.ID, now).
		Order("created_at DESC").Find(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取限制失败")
		return
	}
	views := make([]restrictionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewOf(row, true, false, now))
	}
	c.JSON(http.StatusOK, views)
}

// patch PATCH /guilds/{gid}/restrictions/{id}：仅允许改 expires_at / reason（docs 12 AJ.4）。
func (h *api) patch(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	record, ok := h.manageableRecord(c, ctx, user)
	if !ok {
		return
	}
	beforeReason := record.Reason
	beforeExpires := record.ExpiresAt
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if len(fields) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "没有可更新的字段")
		return
	}
	updates := map[string]any{}
	now := time.Now().UTC()
	for key, raw := range fields {
		switch key {
		case "reason":
			var reason string
			if err := json.Unmarshal(raw, &reason); err != nil {
				fail(c, http.StatusBadRequest, "INVALID_REQUEST", "reason 无效")
				return
			}
			reason = strings.TrimSpace(reason)
			if reason == "" {
				fail(c, http.StatusBadRequest, "REASON_REQUIRED", "必须填写限制原因")
				return
			}
			if utf8.RuneCountInString(reason) > MaxReasonLength {
				fail(c, http.StatusBadRequest, "INVALID_REQUEST", "reason 不能超过 512 个字符")
				return
			}
			record.Reason = reason
			updates["reason"] = reason
		case "expires_at":
			var expiresAt *time.Time
			if err := json.Unmarshal(raw, &expiresAt); err != nil {
				fail(c, http.StatusBadRequest, "INVALID_REQUEST", "expires_at 无效")
				return
			}
			if ruleErr := ValidateDuration(Kind(record.Kind), expiresAt, now, ctx.SystemAdmin); ruleErr != nil {
				failRule(c, http.StatusBadRequest, ruleErr)
				return
			}
			record.ExpiresAt = expiresAt
			updates["expires_at"] = expiresAt
		default:
			// scope / channel / target 等字段禁止修改（AJ.4：需解除后重建）。
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "只允许修改 expires_at 和 reason")
			return
		}
	}
	if err := h.deps.DB.Model(&model.Restriction{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新限制失败")
		return
	}
	publishChange(h.deps.DB, h.deps.Bus, eventbus.EventRestrictionUpdate, record, now)
	h.audit(ctx, user, "restriction.update", record.ID.String(), map[string]any{
		"updates": updates,
		"before": map[string]any{
			"reason": beforeReason, "expires_at": beforeExpires,
		},
	})
	c.JSON(http.StatusOK, viewOf(record, true, true, now))
}

// lift DELETE /guilds/{gid}/restrictions/{id}：提前解除（docs 12 AJ）。
func (h *api) lift(c *gin.Context) {
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	record, ok := h.manageableRecord(c, ctx, user)
	if !ok {
		return
	}
	now := time.Now().UTC()
	result := h.deps.DB.Model(&model.Restriction{}).
		Where("id = ? AND lifted_at IS NULL", record.ID).
		Updates(map[string]any{"lifted_at": now, "lifted_by": user.ID})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解除限制失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusConflict, "RESTRICTION_NOT_ACTIVE", "该限制已被解除")
		return
	}
	liftedBy := user.ID
	record.LiftedAt, record.LiftedBy = &now, &liftedBy
	publishChange(h.deps.DB, h.deps.Bus, eventbus.EventRestrictionLift, record, now)
	beforeSnap := map[string]any{
		"id": record.ID.String(), "guild_id": record.GuildID.String(),
		"target_user_id": record.TargetUserID.String(),
		"scope": record.Scope, "kind": record.Kind, "reason": record.Reason,
		"deny_view_text": record.DenyViewText, "deny_send_text": record.DenySendText,
		"deny_listen_voice": record.DenyListenVoice, "deny_speak_voice": record.DenySpeakVoice,
		"created_by": record.CreatedBy.String(),
	}
	if record.ChannelID != nil {
		beforeSnap["channel_id"] = record.ChannelID.String()
	}
	if record.ExpiresAt != nil {
		beforeSnap["expires_at"] = record.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	h.audit(ctx, user, "restriction.lift", record.ID.String(), map[string]any{
		"target_user_id": record.TargetUserID,
		"before":         beforeSnap,
	})
	c.Status(http.StatusNoContent)
}

// manageableRecord 加载记录并校验管理权：需管理权限且仍生效；不可见/不存在统一 404。
func (h *api) manageableRecord(c *gin.Context, ctx *perms.GuildContext, user model.User) (model.Restriction, bool) {
	var record model.Restriction
	restrictionID, err := uuid.Parse(c.Param("restrictionID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "限制记录不存在")
		return record, false
	}
	if err := h.deps.DB.First(&record, "id = ? AND guild_id = ?", restrictionID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "限制记录不存在")
		return record, false
	}
	if !isManager(ctx) && !coModCanManage(h.deps.DB, user.ID, record) {
		fail(c, http.StatusNotFound, "NOT_FOUND", "限制记录不存在")
		return record, false
	}
	// 层级复核（与创建一致，AK）：普通管理者不能操作层级不低于自己的目标的记录，
	// 也不能解除/修改针对自己的记录（防止被系统管限制的管理者自行解封）。
	if !ctx.SystemAdmin {
		if record.TargetUserID == user.ID {
			failRule(c, http.StatusForbidden, ruleError("CANNOT_RESTRICT_TARGET", "不能操作针对自己的限制"))
			return record, false
		}
		if target, found := h.targetInfo(ctx, record.TargetUserID); found {
			if ruleErr := CheckTarget(h.actorOf(ctx, user), target); ruleErr != nil {
				failRule(c, http.StatusForbidden, ruleErr)
				return record, false
			}
		}
	}
	if !record.ActiveAt(time.Now().UTC()) {
		fail(c, http.StatusConflict, "RESTRICTION_NOT_ACTIVE", "该限制已失效")
		return record, false
	}
	return record, true
}

func (h *api) audit(ctx *perms.GuildContext, user model.User, action, targetID string, detail map[string]any) {
	actorID := user.ID
	actorType := "user"
	if ctx.SystemAdmin {
		actorType = "system_admin"
	} else if ctx.Owner || ctx.Has(rbac.Administrator) {
		actorType = "guild_admin"
	}
	guildID := ctx.Guild.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID:    &actorID,
		ActorType:  actorType,
		GuildID:    &guildID,
		Action:     action,
		TargetType: "restriction",
		TargetID:   targetID,
		Detail:     detail,
	})
}
