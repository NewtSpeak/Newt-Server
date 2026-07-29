package message

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"github.com/newtspeak/newt-server/backend/internal/security"
)

// 消息交互（bot 交互按钮，设计文档 2026-07-26）：
//   - 用户端 POST /channels/{cid}/messages/{mid}/interactions {custom_id} 点击按钮；
//   - 服务端校验后落 MessageInteraction 记录，INTERACTION_CREATE 定向推给 bot（含一次性 token）；
//   - bot POST /bot-api/v1/interactions/{id}/callback {token, type, ...} 回应：
//     ack（仅确认）/ reply（发新消息，默认 ephemeral 给点击者）/ update_message（更新原消息）；
//   - 每次状态推进向点击者定向发 INTERACTION_ACK；超时未回应由 GC 置 EXPIRED。

const (
	// interactionTTL 回应令牌有效期：过期后 callback 一律 410。
	interactionTTL = 15 * time.Minute
	// interactionDedupeWindow 双击幂等窗口：窗口内同 (user, message, custom_id) 的
	// PENDING 交互直接复用，不重复落库与推送。
	interactionDedupeWindow = 3 * time.Second
	// interactionRetentionDays 交互记录审计保留天数（到期硬删）。
	interactionRetentionDays = 30
	// interactionTokenPrefix 回应令牌明文前缀（对齐 botapi 的 owlbot_ 惯例）。
	interactionTokenPrefix = "owlint_"
)

// newInteractionToken 生成一次性回应令牌：明文仅在 INTERACTION_CREATE 中下发，DB 存 SHA-256。
func newInteractionToken() (plain, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	plain = interactionTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return plain, security.HashToken(plain), nil
}

// interactionMember INTERACTION_CREATE 载荷中的点击者摘要。
type interactionMember struct {
	UserID   uuid.UUID   `json:"user_id"`
	Username string      `json:"username"`
	Roles    []uuid.UUID `json:"roles"`
}

// interactionCreatePayload INTERACTION_CREATE（定向 bot）载荷。
type interactionCreatePayload struct {
	ID        string            `json:"id"`
	Token     string            `json:"token"`
	GuildID   uuid.UUID         `json:"guild_id"`
	ChannelID uuid.UUID         `json:"channel_id"`
	MessageID string            `json:"message_id"`
	CustomID  string            `json:"custom_id"`
	Member    interactionMember `json:"member"`
	ExpiresAt time.Time         `json:"expires_at"`
	EventAt   time.Time         `json:"event_at"`
}

// interactionAckPayload INTERACTION_ACK（定向点击者）载荷。
type interactionAckPayload struct {
	InteractionID string    `json:"interaction_id"`
	MessageID     string    `json:"message_id"`
	CustomID      string    `json:"custom_id"`
	Status        string    `json:"status"`
	EventAt       time.Time `json:"event_at"`
}

// publishInteractionAck 向点击者全部端定向推送交互状态推进。
// 事件不带 GuildID/ChannelID（载荷无消息正文，无需频道过滤）。
func (s *service) publishInteractionAck(interaction model.MessageInteraction, status string, at time.Time) {
	s.bus.Publish(eventbus.Event{
		Type:    eventbus.EventInteractionAck,
		UserIDs: []uuid.UUID{interaction.UserID},
		Payload: interactionAckPayload{
			InteractionID: strconv.FormatInt(interaction.ID, 10),
			MessageID:     strconv.FormatInt(interaction.MessageID, 10),
			CustomID:      interaction.CustomID,
			Status:        status,
			EventAt:       at,
		},
	})
}

type createInteractionRequest struct {
	CustomID string `json:"custom_id" binding:"required,max=64"`
}

// createInteraction POST /channels/{cid}/messages/{mid}/interactions：用户点击交互按钮。
// 校验链：频道可达（含解锁）→ 限流 → USE_APPLICATION_COMMANDS → 消息可见（ephemeral 名单）
// → 作者为 bot 且非流式中 → 按钮存在/可交互/未禁用/对本人可见（服务端裁剪的最终防线，
// 任何一步失败一律 404，不泄露隐藏按钮存在性）。
func (s *service) createInteraction(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	_, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	user := s.currentUser(c)
	if !s.interactLimit.Allow(user.ID) {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       gin.H{"code": "INTERACTION_RATE_LIMITED", "message": "操作过于频繁，请稍后再试"},
			"retry_after": 1,
		})
		return
	}
	if !rbac.Has(bits, rbac.UseApplicationCommands) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少使用交互组件权限")
		return
	}
	var input createInteractionRequest
	if !bind(c, &input) {
		return
	}
	message, err := s.loadVisibleMessage(channel.ID, messageID, user.ID)
	if err != nil {
		notFound(c)
		return
	}
	if message.StreamStatus != "" || message.Card == nil {
		notFound(c)
		return
	}
	// 作者必须为 bot（交互回调只对 bot 消息有意义）。
	var author model.User
	if err := s.db.Select("id", "is_bot").First(&author, "id = ?", message.AuthorID).Error; err != nil || !author.IsBot {
		fail(c, http.StatusBadRequest, "NOT_INTERACTIVE", "该消息不支持交互")
		return
	}
	buttons, err := parseCardButtons(*message.Card)
	if err != nil || len(buttons) == 0 {
		notFound(c)
		return
	}
	button, found := findInteractiveButton(buttons, input.CustomID)
	if !found {
		notFound(c)
		return
	}
	if button.Disabled {
		fail(c, http.StatusBadRequest, "NOT_INTERACTIVE", "该按钮当前不可用")
		return
	}
	// 按钮可见性实时校验（防伪造 custom_id 点隐藏按钮）；失败同样 404。
	if !button.VisibleTo.empty() {
		roles := s.userRoleSet(message.GuildID, user.ID)
		if !buttonVisibleTo(button, user.ID, roles) {
			notFound(c)
			return
		}
	}
	now := time.Now().UTC()
	// 双击幂等：短窗口内同键 PENDING 交互直接返回原 interaction_id。
	var existing model.MessageInteraction
	err = s.db.Where("user_id = ? AND message_id = ? AND custom_id = ? AND status = ? AND created_at > ?",
		user.ID, message.ID, input.CustomID, model.InteractionPending, now.Add(-interactionDedupeWindow)).
		Order("id DESC").First(&existing).Error
	if err == nil {
		c.JSON(http.StatusAccepted, gin.H{
			"interaction_id": strconv.FormatInt(existing.ID, 10),
			"status":         existing.Status,
		})
		return
	}
	tokenPlain, tokenHash, err := newInteractionToken()
	if err != nil {
		fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "生成交互令牌失败")
		return
	}
	interaction := model.MessageInteraction{
		ID:        s.ids.Next(),
		GuildID:   message.GuildID,
		ChannelID: message.ChannelID,
		MessageID: message.ID,
		CustomID:  input.CustomID,
		UserID:    user.ID,
		BotUserID: message.AuthorID,
		TokenHash: tokenHash,
		Status:    model.InteractionPending,
		CreatedAt: now,
		ExpiresAt: now.Add(interactionTTL),
	}
	if err := s.db.Create(&interaction).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "记录交互失败")
		return
	}
	// 定向推给 bot（含一次性 token）。不带 GuildID/ChannelID：bot 可能未解锁上锁频道，
	// 而交互事件必须可达（token 是回应的唯一凭证）。
	memberRoles := s.userRoleList(message.GuildID, user.ID)
	s.bus.Publish(eventbus.Event{
		Type:    eventbus.EventInteractionCreate,
		UserIDs: []uuid.UUID{message.AuthorID},
		Payload: interactionCreatePayload{
			ID:        strconv.FormatInt(interaction.ID, 10),
			Token:     tokenPlain,
			GuildID:   message.GuildID,
			ChannelID: message.ChannelID,
			MessageID: strconv.FormatInt(message.ID, 10),
			CustomID:  input.CustomID,
			Member:    interactionMember{UserID: user.ID, Username: user.Username, Roles: memberRoles},
			ExpiresAt: interaction.ExpiresAt,
			EventAt:   now,
		},
	})
	c.JSON(http.StatusAccepted, gin.H{
		"interaction_id": strconv.FormatInt(interaction.ID, 10),
		"status":         interaction.Status,
	})
}

// findInteractiveButton 按 custom_id 定位交互按钮（url 型按钮不参与）。
func findInteractiveButton(buttons []cardButton, customID string) (cardButton, bool) {
	for _, button := range buttons {
		if button.CustomID != "" && button.CustomID == customID {
			return button, true
		}
	}
	return cardButton{}, false
}

// userRoleSet 用户在某服的角色集合（DM 域恒为空）。
func (s *service) userRoleSet(guildID, userID uuid.UUID) map[uuid.UUID]bool {
	roles := make(map[uuid.UUID]bool, 4)
	for _, id := range s.userRoleList(guildID, userID) {
		roles[id] = true
	}
	return roles
}

func (s *service) userRoleList(guildID, userID uuid.UUID) []uuid.UUID {
	if guildID == uuid.Nil {
		return []uuid.UUID{}
	}
	var roleIDs []uuid.UUID
	err := s.db.Raw(`SELECT member_roles.role_id FROM members
		JOIN member_roles ON member_roles.member_id = members.id
		WHERE members.guild_id = ? AND members.user_id = ?`, guildID, userID).Scan(&roleIDs).Error
	if err != nil || roleIDs == nil {
		return []uuid.UUID{}
	}
	return roleIDs
}

type interactionCallbackRequest struct {
	Token string `json:"token" binding:"required"`
	Type  string `json:"type" binding:"required"`
	// Content / Card reply 与 update_message 的载荷；Ephemeral 仅 reply 用（缺省 true）。
	Content   *string         `json:"content"`
	Card      json.RawMessage `json:"card"`
	Ephemeral *bool           `json:"ephemeral"`
}

// interactionCallback POST /bot-api/v1/interactions/{id}/callback：bot 回应交互。
// 鉴权双保险：bot 身份（requireBotAuth）+ 一次性 token；状态机
// PENDING →(ack)→ ACKED →(reply|update_message)→ RESPONDED。
func (s *service) interactionCallback(c *gin.Context) {
	interactionID, err := strconv.ParseInt(c.Param("interactionID"), 10, 64)
	if err != nil || interactionID <= 0 {
		notFound(c)
		return
	}
	var input interactionCallbackRequest
	if !bind(c, &input) {
		return
	}
	bot := s.currentUser(c)
	var interaction model.MessageInteraction
	if err := s.db.First(&interaction, "id = ?", interactionID).Error; err != nil {
		notFound(c)
		return
	}
	// 归属 + token 双校验失败一律 404（不泄露他人交互的存在性）。
	if interaction.BotUserID != bot.ID || interaction.TokenHash != security.HashToken(input.Token) {
		notFound(c)
		return
	}
	now := time.Now().UTC()
	if interaction.Status == model.InteractionExpired || now.After(interaction.ExpiresAt) {
		fail(c, http.StatusGone, "INTERACTION_EXPIRED", "交互已过期，无法回应")
		return
	}
	if interaction.Status == model.InteractionResponded {
		fail(c, http.StatusConflict, "ALREADY_RESPONDED", "该交互已回应过；后续跟进请走普通发消息 API")
		return
	}
	switch input.Type {
	case "ack":
		// 幂等：重复 ack 保持 ACKED。
		if interaction.Status == model.InteractionPending {
			if err := s.db.Model(&model.MessageInteraction{}).Where("id = ? AND status = ?", interaction.ID, model.InteractionPending).
				Update("status", model.InteractionAcked).Error; err != nil {
				fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新交互状态失败")
				return
			}
			s.publishInteractionAck(interaction, model.InteractionAcked, now)
		}
		c.JSON(http.StatusOK, gin.H{"status": model.InteractionAcked})
	case "reply":
		s.interactionReply(c, bot, interaction, input, now)
	case "update_message":
		s.interactionUpdateMessage(c, bot, interaction, input, now)
	default:
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "type 仅支持 ack/reply/update_message")
	}
}

// interactionReply bot 以新消息回应交互；ephemeral 缺省 true（仅点击者可见）。
// 不做提及解析（交互回复以私密反馈为主，@ 语义留给普通发消息 API）。
func (s *service) interactionReply(c *gin.Context, bot model.User, interaction model.MessageInteraction, input interactionCallbackRequest, now time.Time) {
	card, _, err := validateCard(input.Card)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CARD", err.Error())
		return
	}
	content := ""
	if input.Content != nil {
		content = *input.Content
	}
	if err := validateContent(content, 0, card != ""); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_MESSAGE", err.Error())
		return
	}
	var channel model.Channel
	if err := s.db.First(&channel, "id = ?", interaction.ChannelID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取频道失败")
		return
	}
	visibleTo := model.UUIDList{}
	if input.Ephemeral == nil || *input.Ephemeral {
		visibleTo = model.UUIDList{interaction.UserID}
	}
	message := model.Message{
		ID:        s.ids.Next(),
		GuildID:   interaction.GuildID,
		ChannelID: interaction.ChannelID,
		AuthorID:  bot.ID,
		Type:      model.MessageDefault,
		Content:   content,
		VisibleTo: visibleTo,
		CreatedAt: now,
	}
	if card != "" {
		message.Card = &card
	}
	if err := s.db.Create(&message).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "发送回应消息失败")
		return
	}
	if err := s.markInteractionResponded(interaction.ID, now); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新交互状态失败")
		return
	}
	view, err := s.messageViewOne(message)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	s.publishMessageEvent(eventbus.EventMessageCreate, message, view)
	s.markAuthorReadOnSend(bot.ID, channel, message.ID)
	s.index.IndexMessage(message.ID)
	s.publishInteractionAck(interaction, model.InteractionResponded, now)
	// 响应给 bot 作者的视图保留全量按钮声明。
	if cardNeedsTrim(message.Card) {
		if authorView, viewErr := s.messageViewOne(message, bot.ID); viewErr == nil {
			view = authorView
		}
	}
	c.JSON(http.StatusOK, view)
}

// interactionUpdateMessage bot 更新原消息的 card 和/或正文（典型用法：点击后置灰按钮）。
func (s *service) interactionUpdateMessage(c *gin.Context, bot model.User, interaction model.MessageInteraction, input interactionCallbackRequest, now time.Time) {
	message, err := s.loadLiveMessage(interaction.ChannelID, interaction.MessageID)
	if err != nil {
		notFound(c)
		return
	}
	if message.AuthorID != bot.ID {
		notFound(c)
		return
	}
	card, _, err := validateCard(input.Card)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CARD", err.Error())
		return
	}
	if card == "" && input.Content == nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "update_message 需提供 card 或 content")
		return
	}
	updates := make(map[string]any, 2)
	if card != "" {
		updates["card"] = card
		message.Card = &card
	}
	if input.Content != nil {
		if err := validateContent(*input.Content, 0, message.Card != nil); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_MESSAGE", err.Error())
			return
		}
		updates["content"] = *input.Content
		message.Content = *input.Content
	}
	if err := s.db.Model(&model.Message{}).Where("id = ?", message.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新消息失败")
		return
	}
	if err := s.markInteractionResponded(interaction.ID, now); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新交互状态失败")
		return
	}
	view, err := s.messageViewOne(message)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	s.publishMessageEvent(eventbus.EventMessageUpdate, message, view)
	if input.Content != nil {
		s.index.IndexMessage(message.ID)
	}
	s.publishInteractionAck(interaction, model.InteractionResponded, now)
	if cardNeedsTrim(message.Card) {
		if authorView, viewErr := s.messageViewOne(message, bot.ID); viewErr == nil {
			view = authorView
		}
	}
	c.JSON(http.StatusOK, view)
}

// markInteractionResponded 状态推进到 RESPONDED（PENDING/ACKED 均可达）。
func (s *service) markInteractionResponded(interactionID int64, now time.Time) error {
	return s.db.Model(&model.MessageInteraction{}).
		Where("id = ? AND status IN ?", interactionID, []string{model.InteractionPending, model.InteractionAcked}).
		Updates(map[string]any{"status": model.InteractionResponded, "responded_at": now}).Error
}

// gcExpiredInteractions 过期交互清理（挂 runGCOnce）：
//   - PENDING/ACKED 且已过期 → 置 EXPIRED 并向点击者推 EXPIRED ACK（客户端恢复按钮可点）；
//   - 超过审计保留期的记录硬删。
func (s *service) gcExpiredInteractions(now time.Time) {
	var expired []model.MessageInteraction
	err := s.db.Where("status IN ? AND expires_at < ?",
		[]string{model.InteractionPending, model.InteractionAcked}, now).
		Limit(gcBatchSize).Find(&expired).Error
	if err != nil {
		log.Printf("message: 扫描过期交互失败 err=%v", err)
		return
	}
	for _, interaction := range expired {
		if err := s.db.Model(&model.MessageInteraction{}).Where("id = ?", interaction.ID).
			Update("status", model.InteractionExpired).Error; err != nil {
			continue
		}
		s.publishInteractionAck(interaction, model.InteractionExpired, now)
	}
	if err := s.db.Where("created_at < ?", now.AddDate(0, 0, -interactionRetentionDays)).
		Delete(&model.MessageInteraction{}).Error; err != nil {
		log.Printf("message: 清理过期交互记录失败 err=%v", err)
	}
}
