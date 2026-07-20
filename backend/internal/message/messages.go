package message

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// 消息收发 / 编辑 / 删除 / 编辑历史（docs 13 AR/AS）。

const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

type createMessageRequest struct {
	Content string `json:"content"`
	// Card 卡片消息载荷（bot 专项）：任意 JSON 对象，服务端只校验结构与大小，
	// 渲染 schema 由客户端约定（嵌入/字段/按钮等）。
	Card          json.RawMessage `json:"card"`
	ReplyToID     string          `json:"reply_to_id"`    // 雪花 ID 字符串
	AttachmentIDs []string        `json:"attachment_ids"` // presign 返回的附件 UUID
	Nonce         string          `json:"nonce" binding:"max=64"`
}

// createMessage POST /channels/{id}/messages（AR.1）。
// 需 SEND_MESSAGES（Restriction 禁发已在权限位中收紧）；支持 nonce 幂等与仅附件消息。
func (s *service) createMessage(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	if !rbac.Has(bits, rbac.SendMessages) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少发送消息权限")
		return
	}
	// 慢速模式（docs 03 §8-9 / 05 FR-08）：频道配置 rate_limit_per_user 秒内每用户
	// 限一条；MANAGE_MESSAGES / MANAGE_CHANNELS 豁免（对标 Discord）。429 携带
	// retry_after（秒，向上取整）供客户端倒计时。
	if channel.RateLimitPerUser > 0 && !rbac.Has(bits, rbac.ManageMessages) && !rbac.Has(bits, rbac.ManageChannels) {
		var last model.Message
		err := s.db.Select("created_at").
			Where("channel_id = ? AND author_id = ?", channel.ID, s.currentUser(c).ID).
			Order("id DESC").First(&last).Error
		if err == nil {
			elapsed := time.Since(last.CreatedAt)
			window := time.Duration(channel.RateLimitPerUser) * time.Second
			if elapsed < window {
				retryAfter := int((window - elapsed + time.Second - 1) / time.Second)
				c.Header("Retry-After", strconv.Itoa(retryAfter))
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":       gin.H{"code": "SLOWMODE_RATE_LIMITED", "message": "慢速模式生效中，请稍后再发"},
					"retry_after": retryAfter,
				})
				return
			}
		}
	}
	var input createMessageRequest
	if !bind(c, &input) {
		return
	}
	card, err := validateCard(input.Card)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CARD", err.Error())
		return
	}
	if err := validateContent(input.Content, len(input.AttachmentIDs), card != ""); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_MESSAGE", err.Error())
		return
	}
	user := s.currentUser(c)
	now := time.Now().UTC()

	// nonce 幂等（AR.6）：短窗口内同 channel+author+nonce 直接返回原消息，不重复落库。
	if input.Nonce != "" {
		var existing model.Message
		err := s.db.Where("channel_id = ? AND author_id = ? AND nonce = ? AND deleted_at IS NULL", channel.ID, user.ID, input.Nonce).
			Order("id DESC").First(&existing).Error
		if err == nil && nonceDuplicate(existing.CreatedAt, now) {
			view, viewErr := s.messageViewOne(existing)
			if viewErr != nil {
				fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
				return
			}
			c.JSON(http.StatusOK, view)
			return
		}
	}

	// 单层回复（AP.6）：只校验被回复消息存在于同频道，不限制其本身是否为回复。
	var replyToID *int64
	if input.ReplyToID != "" {
		parsed, err := strconv.ParseInt(input.ReplyToID, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REPLY", "reply_to_id 非法")
			return
		}
		if _, err := s.loadLiveMessage(channel.ID, parsed); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REPLY", "被回复的消息不存在")
			return
		}
		replyToID = &parsed
	}

	attachmentIDs, ok := parseAttachmentIDs(c, input.AttachmentIDs)
	if !ok {
		return
	}

	// 服务端解析提及 wire format 并按 guild 校验（docs 05 FR-22、15 §7-2）；
	// @everyone 是否生效取决于作者的 MENTION_EVERYONE 频道权限位。
	mentions, err := s.resolveMentions(ctx.Guild.ID, input.Content, bits)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解析提及失败")
		return
	}

	message := model.Message{
		ID:              s.ids.Next(),
		GuildID:         ctx.Guild.ID,
		ChannelID:       channel.ID,
		AuthorID:        user.ID,
		Type:            model.MessageDefault,
		Content:         input.Content,
		ReplyToID:       replyToID,
		Nonce:           input.Nonce,
		Mentions:        mentions.Users,
		MentionRoles:    mentions.Roles,
		MentionEveryone: mentions.Everyone,
		CreatedAt:       now,
	}
	if card != "" {
		message.Card = &card
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		// 绑定附件：必须是本人上传、已完成写入、尚未绑定到其他消息、且属于本频道。
		if len(attachmentIDs) > 0 {
			result := tx.Model(&model.Attachment{}).
				Where("id IN ? AND uploader_id = ? AND channel_id = ? AND uploaded = true AND message_id IS NULL", attachmentIDs, user.ID, channel.ID).
				Update("message_id", message.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(attachmentIDs)) {
				return errAttachmentBind
			}
		}
		return nil
	})
	if err != nil {
		if err == errAttachmentBind {
			fail(c, http.StatusBadRequest, "INVALID_ATTACHMENT", "附件不存在、未完成上传或已绑定其他消息")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "发送消息失败")
		return
	}

	view, err := s.messageViewOne(message)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	s.publishMessageEvent(eventbus.EventMessageCreate, message, view)
	// 为被提及者（在线与离线）批量递增其该频道 mention_count（可见性过滤，docs 15 FR-04）。
	s.bumpMentionCounts(message)
	s.index.IndexMessage(message.ID)
	c.JSON(http.StatusCreated, view)
}

var errAttachmentBind = fmt.Errorf("附件绑定校验失败")

func parseAttachmentIDs(c *gin.Context, raw []string) ([]uuid.UUID, bool) {
	ids := make([]uuid.UUID, 0, len(raw))
	seen := make(map[uuid.UUID]struct{}, len(raw))
	for _, item := range raw {
		id, err := uuid.Parse(item)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_ATTACHMENT", "attachment_ids 含非法 ID")
			return nil, false
		}
		if _, dup := seen[id]; dup {
			fail(c, http.StatusBadRequest, "INVALID_ATTACHMENT", "attachment_ids 含重复 ID")
			return nil, false
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true
}

// listMessages GET /channels/{id}/messages?before=&after=&limit=（AR.2/AR.3/AR.4）。
// 拉历史需 READ_MESSAGE_HISTORY；无该权限按文档语义返回 404（只收进频后实时推送）。
// 游标为雪花消息 ID，结果恒按 ID 降序（新→旧）。
func (s *service) listMessages(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	_, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	if !rbac.Has(bits, rbac.ReadMessageHistory) {
		notFound(c)
		return
	}
	limit := defaultPageLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			fail(c, http.StatusBadRequest, "INVALID_LIMIT", "limit 需为 1-100 的整数")
			return
		}
		if parsed > maxPageLimit {
			parsed = maxPageLimit
		}
		limit = parsed
	}
	query := s.db.Where("channel_id = ? AND deleted_at IS NULL", channel.ID)
	for _, cursor := range []struct {
		name string
		op   string
	}{{"before", "id < ?"}, {"after", "id > ?"}} {
		if raw := c.Query(cursor.name); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				fail(c, http.StatusBadRequest, "INVALID_CURSOR", cursor.name+" 需为消息 ID")
				return
			}
			query = query.Where(cursor.op, parsed)
		}
	}
	var messages []model.Message
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	views, err := s.messageViews(messages, s.currentUser(c).ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取附件失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": views})
}

// getMessage GET /channels/{id}/messages/{mid}；读取单条同样属于历史读取，需 READ_MESSAGE_HISTORY。
// 软删消息对用户侧一律 404（AQ.3 墓碑语义）。
func (s *service) getMessage(c *gin.Context) {
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
	if !rbac.Has(bits, rbac.ReadMessageHistory) {
		notFound(c)
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	view, err := s.messageViewOne(message, s.currentUser(c).ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

type editMessageRequest struct {
	Content string `json:"content"`
}

// editMessage PATCH /channels/{id}/messages/{mid}（AS.1–AS.5）。
// 仅作者可编辑正文，无时间窗；MANAGE_MESSAGES 也不能改他人（AS.2）。
// 流程：先写 message_edits 旧正文全文快照（version 递增）→ 更新本体 → 广播 → 异步重建索引。
func (s *service) editMessage(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	if message.AuthorID != user.ID {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅作者可编辑消息正文")
		return
	}
	var input editMessageRequest
	if !bind(c, &input) {
		return
	}
	var attachmentCount int64
	if err := s.db.Model(&model.Attachment{}).Where("message_id = ?", message.ID).Count(&attachmentCount).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取附件失败")
		return
	}
	if err := validateContent(input.Content, int(attachmentCount), message.Card != nil); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_MESSAGE", err.Error())
		return
	}
	// 编辑后重新解析提及并更新落库字段（docs 15 FR-05：客户端按 MESSAGE_UPDATE
	// 的最新 mentions 自行校正本地计数，服务端不追溯调整 mention_count）。
	mentions, err := s.resolveMentions(ctx.Guild.ID, input.Content, bits)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解析提及失败")
		return
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 全文快照存「编辑前」正文：version=1 即原始正文，当前正文始终在 messages.content。
		snapshot := model.MessageEdit{
			ID:        uuid.New(),
			MessageID: message.ID,
			Version:   message.EditCount + 1,
			Content:   message.Content,
			EditorID:  user.ID,
			EditedAt:  now,
		}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}
		return tx.Model(&model.Message{}).Where("id = ?", message.ID).Updates(map[string]any{
			"content":          input.Content,
			"edit_count":       gorm.Expr("edit_count + 1"),
			"edited_at":        now,
			"mentions":         mentions.Users,
			"mention_roles":    mentions.Roles,
			"mention_everyone": mentions.Everyone,
		}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "编辑消息失败")
		return
	}
	message.Content = input.Content
	message.EditCount++
	message.EditedAt = &now
	message.Mentions = mentions.Users
	message.MentionRoles = mentions.Roles
	message.MentionEveryone = mentions.Everyone
	view, err := s.messageViewOne(message)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	s.publishMessageEvent(eventbus.EventMessageUpdate, message, view)
	s.index.IndexMessage(message.ID)
	c.JSON(http.StatusOK, view)
}

// deleteMessage DELETE /channels/{id}/messages/{mid}（AS.6）：作者或 MANAGE_MESSAGES；软删。
func (s *service) deleteMessage(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	isAuthor := message.AuthorID == user.ID
	if !isAuthor && !rbac.Has(bits, rbac.ManageMessages) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少删除他人消息权限")
		return
	}
	now := time.Now().UTC()
	if err := s.db.Model(&model.Message{}).Where("id = ? AND deleted_at IS NULL", message.ID).Update("deleted_at", now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除消息失败")
		return
	}
	if !isAuthor {
		// 管理删除他人消息属敏感操作，落审计。
		audit.Log(s.db, audit.Entry{
			ActorID: &user.ID, GuildID: &ctx.Guild.ID,
			Action: "message.delete_by_moderator", TargetType: "message", TargetID: strconv.FormatInt(message.ID, 10),
			Detail: map[string]any{"channel_id": channel.ID, "author_id": message.AuthorID},
		})
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventMessageDelete,
		GuildID:   &message.GuildID,
		ChannelID: &message.ChannelID,
		Payload: gin.H{
			"id":         strconv.FormatInt(message.ID, 10),
			"channel_id": message.ChannelID,
			"guild_id":   message.GuildID,
		},
	})
	s.index.RemoveMessage(message.ID)
	c.Status(http.StatusNoContent)
}

// listEdits GET /channels/{id}/messages/{mid}/edits（AS.5）。
// 仅作者、MANAGE_MESSAGES、系统管可见完整历史；其他人 404（只可见 edit_count）。
func (s *service) listEdits(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	if message.AuthorID != user.ID && !rbac.Has(bits, rbac.ManageMessages) && !ctx.SystemAdmin {
		notFound(c)
		return
	}
	var edits []model.MessageEdit
	if err := s.db.Where("message_id = ?", message.ID).Order("version ASC").Find(&edits).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取编辑历史失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"edits": edits, "edit_count": message.EditCount})
}

// publishMessageEvent 广播消息事件；Gateway 按 GuildID+ChannelID 做频道可见性过滤后下发。
func (s *service) publishMessageEvent(eventType string, message model.Message, view messageView) {
	s.bus.Publish(eventbus.Event{
		Type:      eventType,
		GuildID:   &message.GuildID,
		ChannelID: &message.ChannelID,
		Payload:   view,
	})
}
