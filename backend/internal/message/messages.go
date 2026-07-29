package message

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/activity"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"github.com/newtspeak/newt-server/backend/internal/sticker"
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
	// StickerItems 贴图消息（docs 17）：长度必须为 1；与正文/附件/卡片互斥。
	// 每项至少提供 item_id；pack_id/mark 由服务端按权威数据回填。
	StickerItems []stickerItemInput `json:"sticker_items"`
	// VisibleToUserIDs ephemeral 定向可见名单（bot 专项，设计文档 2026-07-26）：
	// 非空即 ephemeral——仅名单用户 + 作者可见；≤20 人、仅 bot 可用、禁止携带附件。
	VisibleToUserIDs []string `json:"visible_to_user_ids"`
	// VisibleRoleIDs 限定可见身份组；省略 = 跟随频道默认；显式 [] = 公开；非空 = 限定。
	// 用指针区分「未携带」与「携带空数组」。与 ephemeral 互斥语义上独立（bot ephemeral 优先）。
	VisibleRoleIDs *[]string `json:"visible_role_ids"`
}

// maxEphemeralTargets ephemeral 可见名单人数上限。
const maxEphemeralTargets = 20

// stickerItemInput 发送贴图时的客户端引用。
type stickerItemInput struct {
	ItemID string `json:"item_id"`
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
	// 私信：屏蔽 / 请求箱频控 / hidden 重开 / 回复即接受
	if !s.enforcePrivateSend(c, channel, s.currentUser(c).ID) {
		return
	}
	// 慢速模式：默认对所有成员生效，仅频道配置的豁免角色可以跳过。
	// 权限位本身不再隐式豁免，避免管理员配置无法约束普通管理角色。
	if channel.RateLimitPerUser > 0 && !s.slowmodeExempt(channel, ctx) {
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
	card, _, err := validateCard(input.Card)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CARD", err.Error())
		return
	}
	isStickerMsg := len(input.StickerItems) > 0
	if isStickerMsg {
		// M.1–M.3：贴图消息禁止正文/附件/卡片混排，恰 1 张
		if len(input.StickerItems) != 1 {
			fail(c, http.StatusBadRequest, "INVALID_STICKER", "贴图消息必须恰好包含 1 张贴图")
			return
		}
		if strings.TrimSpace(input.Content) != "" || len(input.AttachmentIDs) > 0 || card != "" {
			fail(c, http.StatusBadRequest, "INVALID_STICKER", "贴图消息禁止与正文、附件或卡片混排")
			return
		}
	} else if err := validateContent(input.Content, len(input.AttachmentIDs), card != ""); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_MESSAGE", err.Error())
		return
	}
	user := s.currentUser(c)
	now := time.Now().UTC()

	// ephemeral 定向可见校验（设计文档 2026-07-26）：仅 bot、≤20 人、禁附件、
	// 目标必须可见该频道（防越权塞消息）。
	visibleTo, ok := s.resolveVisibleTo(c, user, channel, input.VisibleToUserIDs, len(input.AttachmentIDs))
	if !ok {
		return
	}
	// 限定可见身份组：频道策略 + 客户端请求合成（仅服内 TEXT；私信忽略）。
	// ephemeral 消息不套用角色可见（名单已足够窄）。
	var visibleRoleIDs model.UUIDList
	if len(visibleTo) == 0 {
		clientSpecified := input.VisibleRoleIDs != nil
		var visErr error
		visibleRoleIDs, visErr = s.parseAndValidateVisibleRoles(c, channel, input.VisibleRoleIDs, clientSpecified)
		if visErr != nil {
			return
		}
	}

	// nonce 幂等（AR.6）：短窗口内同 channel+author+nonce 直接返回原消息，不重复落库。
	if input.Nonce != "" {
		var existing model.Message
		err := s.db.Where("channel_id = ? AND author_id = ? AND nonce = ? AND deleted_at IS NULL", channel.ID, user.ID, input.Nonce).
			Order("id DESC").First(&existing).Error
		if err == nil && nonceDuplicate(existing.CreatedAt, now) {
			view, viewErr := s.messageViewOne(existing, user.ID)
			if viewErr != nil {
				fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
				return
			}
			c.JSON(http.StatusOK, view)
			return
		}
	}

	// 单层回复（AP.6）：只校验被回复消息存在于同频道，且发送者可见；
	// ephemeral 消息禁止被回复（公开引用他人不可见消息会破坏语义，对齐 Discord）。
	var replyToID *int64
	if input.ReplyToID != "" {
		parsed, err := strconv.ParseInt(input.ReplyToID, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REPLY", "reply_to_id 非法")
			return
		}
		replied, err := s.loadLiveMessage(channel.ID, parsed)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REPLY", "被回复的消息不存在")
			return
		}
		if replied.IsEphemeral() {
			fail(c, http.StatusBadRequest, "INVALID_REPLY", "不能回复定向可见消息")
			return
		}
		viewerRoles := roleIDsFromCtx(ctx)
		ownerOrAdmin := ctx != nil && (ctx.Owner || ctx.SystemAdmin)
		if !canViewMessage(s.currentUser(c).ID, bits, viewerRoles, ownerOrAdmin, replied) {
			fail(c, http.StatusBadRequest, "INVALID_REPLY", "被回复的消息不存在")
			return
		}
		replyToID = &parsed
	}

	attachmentIDs, ok := parseAttachmentIDs(c, input.AttachmentIDs)
	if !ok {
		return
	}

	guildID := channel.GuildID
	if channel.Type.IsPrivate() {
		guildID = uuid.Nil
	}

	// 贴图消息：校验可用集合 + kind=sticker，写入 sticker_items 快照。
	var stickerItemsJSON *string
	msgType := model.MessageDefault
	if isStickerMsg {
		itemID, parseErr := strconv.ParseInt(strings.TrimSpace(input.StickerItems[0].ItemID), 10, 64)
		if parseErr != nil || itemID <= 0 {
			fail(c, http.StatusBadRequest, "INVALID_STICKER", "sticker_items[0].item_id 非法")
			return
		}
		avail, availErr := sticker.ItemAvailableForSend(s.db, user.ID, guildID, itemID)
		if availErr != nil {
			fail(c, http.StatusForbidden, "STICKER_NOT_AVAILABLE", "贴图不在可用集合内")
			return
		}
		if avail.Item.Kind != model.StickerKindSticker {
			fail(c, http.StatusBadRequest, "INVALID_STICKER", "该项为小表情，不能作为贴图消息发送")
			return
		}
		ref, refErr := sticker.ResolveItemRef(s.db, itemID)
		if refErr != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解析贴图失败")
			return
		}
		raw, _ := json.Marshal([]sticker.MessageStickerRef{ref})
		s := string(raw)
		stickerItemsJSON = &s
		msgType = model.MessageSticker
	} else {
		// 正文内嵌自定义小表情 <e:item_id:mark>：须全部 ∈ 可用集合且 kind=emote
		for _, emoteID := range sticker.ExtractCustomEmoteItemIDs(input.Content) {
			avail, availErr := sticker.ItemAvailableForSend(s.db, user.ID, guildID, emoteID)
			if availErr != nil {
				fail(c, http.StatusForbidden, "EMOTE_NOT_AVAILABLE",
					fmt.Sprintf("小表情 %d 不在可用集合内", emoteID))
				return
			}
			if avail.Item.Kind != model.StickerKindEmote {
				fail(c, http.StatusBadRequest, "INVALID_EMOTE",
					fmt.Sprintf("条目 %d 不是小表情，不能内嵌到正文", emoteID))
				return
			}
		}
	}

	// 服务端解析提及 wire format 并按 guild 校验（docs 05 FR-22、15 §7-2）；
	// DM 仅允许参与者 @（Server-16 BN.3）；@everyone 是否生效取决于 MENTION_EVERYONE。
	var mentions resolvedMentions
	if !isStickerMsg {
		if channel.Type.IsPrivate() {
			mentions, err = s.resolveMentionsDM(channel.ID, input.Content)
		} else {
			mentions, err = s.resolveMentions(ctx.Guild.ID, input.Content, bits)
		}
		if err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解析提及失败")
			return
		}
	}

	message := model.Message{
		ID:              s.ids.Next(),
		GuildID:         guildID,
		ChannelID:       channel.ID,
		AuthorID:        user.ID,
		Type:            msgType,
		Content:         input.Content,
		ReplyToID:       replyToID,
		Nonce:           input.Nonce,
		Mentions:        mentions.Users,
		MentionRoles:    mentions.Roles,
		MentionEveryone: mentions.Everyone,
		StickerItems:    stickerItemsJSON,
		VisibleTo:       visibleTo,
		VisibleRoleIDs:  visibleRoleIDs,
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
	// 作者自动已读：刷新后 READY 不再把本人消息算作未读（docs 15 FR-01/FR-02）。
	s.markAuthorReadOnSend(user.ID, channel, message.ID)
	// 为被提及者（在线与离线）批量递增其该频道 mention_count（可见性过滤，docs 15 FR-04）。
	s.bumpMentionCounts(message)
	s.index.IndexMessage(message.ID)
	// 活跃度累计（bot 与 30s 限流由 activity 内部处理）。
	activity.TrackMessage(user)
	// 含差异化按钮时，REST 响应对作者重建全量视图（广播用 view 已做安全裁剪）。
	if cardNeedsTrim(message.Card) {
		if authorView, viewErr := s.messageViewOne(message, user.ID); viewErr == nil {
			view = authorView
		}
	}
	c.JSON(http.StatusCreated, view)
}

// resolveVisibleTo 解析并校验 ephemeral 定向可见名单（空名单 = 普通公开消息）。
// 校验失败时已写入错误响应，返回 (nil, false)。
func (s *service) resolveVisibleTo(c *gin.Context, user model.User, channel model.Channel, raw []string, attachmentCount int) (model.UUIDList, bool) {
	if len(raw) == 0 {
		return model.UUIDList{}, true
	}
	if !user.IsBot {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅机器人可发送定向可见消息")
		return nil, false
	}
	if attachmentCount > 0 {
		fail(c, http.StatusBadRequest, "INVALID_VISIBLE_TO", "定向可见消息禁止携带附件")
		return nil, false
	}
	if len(raw) > maxEphemeralTargets {
		fail(c, http.StatusBadRequest, "INVALID_VISIBLE_TO",
			fmt.Sprintf("visible_to_user_ids 超过上限 %d", maxEphemeralTargets))
		return nil, false
	}
	targets := make(model.UUIDList, 0, len(raw))
	seen := make(map[uuid.UUID]struct{}, len(raw))
	for _, item := range raw {
		id, err := uuid.Parse(item)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_VISIBLE_TO", "visible_to_user_ids 含非法 ID")
			return nil, false
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
	}
	// 目标可见性：DM 要求 ∈ recipients；服频道要求成员且可见该频道
	//（防 bot 把消息「塞给」无频道权限的人造成越权可见）。
	if channel.Type.IsPrivate() {
		recipients := make(map[uuid.UUID]struct{})
		for _, id := range s.loadDMRecipientIDs(channel.ID) {
			recipients[id] = struct{}{}
		}
		for _, id := range targets {
			if _, ok := recipients[id]; !ok {
				fail(c, http.StatusBadRequest, "INVALID_VISIBLE_TO", "目标用户不在本私信频道内")
				return nil, false
			}
		}
		return targets, true
	}
	var users []model.User
	if err := s.db.Where("id IN ?", []uuid.UUID(targets)).Find(&users).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取目标用户失败")
		return nil, false
	}
	if len(users) != len(targets) {
		fail(c, http.StatusBadRequest, "INVALID_VISIBLE_TO", "目标用户不存在")
		return nil, false
	}
	for _, target := range users {
		if !perms.CanSeeChannel(s.db, target, channel.GuildID, channel.ID) {
			fail(c, http.StatusBadRequest, "INVALID_VISIBLE_TO", "目标用户不可见该频道")
			return nil, false
		}
	}
	return targets, true
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
// ephemeral 走 SQL 过滤；限定可见身份组在服务端二次过滤，不足 limit 时沿游标补足。
func (s *service) listMessages(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
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
	var beforeID, afterID *int64
	for _, cursor := range []struct {
		name   string
		target **int64
	}{{"before", &beforeID}, {"after", &afterID}} {
		if raw := c.Query(cursor.name); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				fail(c, http.StatusBadRequest, "INVALID_CURSOR", cursor.name+" 需为消息 ID")
				return
			}
			*cursor.target = &parsed
		}
	}
	user := s.currentUser(c)
	viewerRoles := roleIDsFromCtx(ctx)
	ownerOrAdmin := ctx != nil && (ctx.Owner || ctx.SystemAdmin)

	// 多批补足：ephemeral SQL 过滤后再按角色可见性过滤，可能不足 limit。
	collected := make([]model.Message, 0, limit)
	cursorBefore := beforeID
	for len(collected) < limit {
		batchSize := (limit - len(collected)) * 3
		if batchSize < 20 {
			batchSize = 20
		}
		if batchSize > maxPageLimit {
			batchSize = maxPageLimit
		}
		query := s.db.Scopes(visibleToScope(user.ID)).
			Where("channel_id = ? AND deleted_at IS NULL", channel.ID)
		if cursorBefore != nil {
			query = query.Where("id < ?", *cursorBefore)
		}
		if afterID != nil {
			query = query.Where("id > ?", *afterID)
		}
		var batch []model.Message
		if err := query.Order("id DESC").Limit(batchSize).Find(&batch).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
			return
		}
		if len(batch) == 0 {
			break
		}
		visible := filterVisibleMessages(user.ID, bits, viewerRoles, ownerOrAdmin, batch)
		for _, msg := range visible {
			collected = append(collected, msg)
			if len(collected) >= limit {
				break
			}
		}
		last := batch[len(batch)-1].ID
		cursorBefore = &last
		if len(batch) < batchSize {
			break
		}
	}
	views, err := s.messageViews(collected, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取附件失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": views})
}

// parseAndValidateVisibleRoles 解析请求中的 visible_role_ids 并套用频道策略；失败时已写响应。
func (s *service) parseAndValidateVisibleRoles(c *gin.Context, channel model.Channel, raw *[]string, clientSpecified bool) (model.UUIDList, error) {
	var ids []uuid.UUID
	if raw != nil {
		ids = make([]uuid.UUID, 0, len(*raw))
		for _, item := range *raw {
			id, err := uuid.Parse(item)
			if err != nil {
				fail(c, http.StatusBadRequest, "INVALID_VISIBLE_ROLES", "visible_role_ids 含非法 ID")
				return nil, err
			}
			ids = append(ids, id)
		}
	}
	list, err := s.resolveEffectiveVisibleRoles(channel, ids, clientSpecified)
	if err != nil {
		switch err {
		case errVisibleRolesTextOnly, errVisibleRoleInvalid, errVisibleRolesDisabled:
			fail(c, http.StatusBadRequest, "INVALID_VISIBLE_ROLES", err.Error())
		default:
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "校验可见角色失败")
		}
		return nil, err
	}
	return list, nil
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
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	if !rbac.Has(bits, rbac.ReadMessageHistory) {
		notFound(c)
		return
	}
	message, err := s.loadVisibleMessage(channel.ID, messageID, s.currentUser(c).ID)
	if err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	ownerOrAdmin := ctx != nil && (ctx.Owner || ctx.SystemAdmin)
	if !canViewMessage(user.ID, bits, roleIDsFromCtx(ctx), ownerOrAdmin, message) {
		notFound(c)
		return
	}
	view, err := s.messageViewOne(message, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

type editMessageRequest struct {
	// Content 正文；与 VisibleRoleIDs 至少提供一项（兼容仅改可见范围）。
	Content *string `json:"content"`
	// VisibleRoleIDs 修改可见范围（仅作者）；指针区分未携带 / 公开 / 限定。
	// 频道 ForceDefaultVisibility 时禁止修改。
	VisibleRoleIDs *[]string `json:"visible_role_ids"`
}

// editMessage PATCH /channels/{id}/messages/{mid}（AS.1–AS.5）。
// 仅作者可编辑正文与可见范围，无时间窗；MANAGE_MESSAGES 也不能改他人（AS.2）。
// 流程：先写 message_edits 旧正文全文快照（version 递增）→ 更新本体 → 广播 → 异步重建索引。
// 可见范围变更时：交集发 UPDATE，失去可见者发 DELETE，新增可见者发 CREATE。
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
	user := s.currentUser(c)
	message, err := s.loadVisibleMessage(channel.ID, messageID, user.ID)
	if err != nil {
		notFound(c)
		return
	}
	ownerOrAdmin := ctx != nil && (ctx.Owner || ctx.SystemAdmin)
	if !canViewMessage(user.ID, bits, roleIDsFromCtx(ctx), ownerOrAdmin, message) {
		notFound(c)
		return
	}
	if message.AuthorID != user.ID {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅作者可编辑消息")
		return
	}
	if message.IsEphemeral() {
		// ephemeral 不允许改可见名单（创建时一次定死）；正文仍可改。
	}
	var input editMessageRequest
	if !bind(c, &input) {
		return
	}
	if input.Content == nil && input.VisibleRoleIDs == nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "content 与 visible_role_ids 至少提供一项")
		return
	}

	oldAudience := s.resolveAudienceUserIDs(message) // 公开时 nil
	oldRestricted := isMessageRestricted(message)
	contentChanged := false
	visibilityChanged := false
	newContent := message.Content
	newVisible := message.VisibleRoleIDs
	var mentions resolvedMentions
	mentions.Users = message.Mentions
	mentions.Roles = message.MentionRoles
	mentions.Everyone = message.MentionEveryone

	if input.VisibleRoleIDs != nil {
		if message.IsEphemeral() {
			fail(c, http.StatusBadRequest, "INVALID_VISIBLE_ROLES", "定向可见消息不可修改角色可见范围")
			return
		}
		if channel.ForceDefaultVisibility {
			fail(c, http.StatusBadRequest, "INVALID_VISIBLE_ROLES", "本频道强制默认可见范围，不可单独修改")
			return
		}
		list, visErr := s.parseAndValidateVisibleRoles(c, channel, input.VisibleRoleIDs, true)
		if visErr != nil {
			return
		}
		if !uuidListEqual(message.VisibleRoleIDs, list) {
			visibilityChanged = true
			newVisible = list
		}
	}

	if input.Content != nil {
		newContent = *input.Content
		var attachmentCount int64
		if err := s.db.Model(&model.Attachment{}).Where("message_id = ?", message.ID).Count(&attachmentCount).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取附件失败")
			return
		}
		if err := validateContent(newContent, int(attachmentCount), message.Card != nil); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_MESSAGE", err.Error())
			return
		}
		if channel.Type.IsPrivate() {
			mentions, err = s.resolveMentionsDM(channel.ID, newContent)
		} else {
			mentions, err = s.resolveMentions(ctx.Guild.ID, newContent, bits)
		}
		if err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "解析提及失败")
			return
		}
		if newContent != message.Content {
			contentChanged = true
		}
	}

	if !contentChanged && !visibilityChanged {
		view, viewErr := s.messageViewOne(message, user.ID)
		if viewErr != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
			return
		}
		c.JSON(http.StatusOK, view)
		return
	}

	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{}
		if contentChanged {
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
			updates["content"] = newContent
			updates["edit_count"] = gorm.Expr("edit_count + 1")
			updates["edited_at"] = now
			updates["mentions"] = mentions.Users
			updates["mention_roles"] = mentions.Roles
			updates["mention_everyone"] = mentions.Everyone
		}
		if visibilityChanged {
			updates["visible_role_ids"] = newVisible
		}
		return tx.Model(&model.Message{}).Where("id = ?", message.ID).Updates(updates).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "编辑消息失败")
		return
	}
	if contentChanged {
		message.Content = newContent
		message.EditCount++
		message.EditedAt = &now
		message.Mentions = mentions.Users
		message.MentionRoles = mentions.Roles
		message.MentionEveryone = mentions.Everyone
	}
	if visibilityChanged {
		message.VisibleRoleIDs = newVisible
	}
	view, err := s.messageViewOne(message, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	if visibilityChanged {
		s.publishVisibilityChange(message, view, oldAudience, oldRestricted)
	} else {
		s.publishMessageEvent(eventbus.EventMessageUpdate, message, view)
	}
	if contentChanged {
		s.index.IndexMessage(message.ID)
	}
	if cardNeedsTrim(message.Card) {
		if authorView, viewErr := s.messageViewOne(message, user.ID); viewErr == nil {
			view = authorView
		}
	}
	c.JSON(http.StatusOK, view)
}

// uuidListEqual 比较两个角色 ID 列表（忽略顺序）。
func uuidListEqual(a, b model.UUIDList) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[uuid.UUID]struct{}, len(a))
	for _, id := range a {
		set[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

// publishVisibilityChange 可见范围变更后的差集事件：
// 失去可见 → MESSAGE_DELETE；新增可见 → MESSAGE_CREATE；仍可见 → MESSAGE_UPDATE。
func (s *service) publishVisibilityChange(message model.Message, view messageView, oldAudience []uuid.UUID, oldRestricted bool) {
	newRestricted := isMessageRestricted(message)
	newAudience := s.resolveAudienceUserIDs(message)

	if !oldRestricted && !newRestricted {
		s.publishMessageEvent(eventbus.EventMessageUpdate, message, view)
		return
	}

	oldSet := make(map[uuid.UUID]struct{})
	if oldRestricted {
		for _, id := range oldAudience {
			oldSet[id] = struct{}{}
		}
	}
	newSet := make(map[uuid.UUID]struct{})
	if newRestricted {
		for _, id := range newAudience {
			newSet[id] = struct{}{}
		}
	}

	var lost []uuid.UUID
	if oldRestricted {
		for id := range oldSet {
			if _, ok := newSet[id]; !ok {
				if newRestricted {
					lost = append(lost, id)
				}
			}
		}
	}

	if len(lost) > 0 {
		deletePayload := gin.H{
			"id":         strconv.FormatInt(message.ID, 10),
			"channel_id": message.ChannelID,
			"guild_id":   message.GuildID,
		}
		s.bus.Publish(eventbus.Event{
			Type:      eventbus.EventMessageDelete,
			GuildID:   &message.GuildID,
			ChannelID: &message.ChannelID,
			UserIDs:   lost,
			Payload:   deletePayload,
		})
	}

	if !newRestricted {
		s.publishChannelScopedEvent(eventbus.EventMessageUpdate, message.GuildID, message.ChannelID, view)
		s.publishChannelScopedEvent(eventbus.EventMessageCreate, message.GuildID, message.ChannelID, view)
		return
	}

	var keep, gained []uuid.UUID
	for id := range newSet {
		if oldRestricted {
			if _, ok := oldSet[id]; ok {
				keep = append(keep, id)
			} else {
				gained = append(gained, id)
			}
		} else {
			keep = append(keep, id)
		}
	}
	if len(keep) > 0 {
		s.bus.Publish(eventbus.Event{
			Type:      eventbus.EventMessageUpdate,
			GuildID:   &message.GuildID,
			ChannelID: &message.ChannelID,
			UserIDs:   keep,
			Payload:   view,
		})
	}
	if len(gained) > 0 {
		s.bus.Publish(eventbus.Event{
			Type:      eventbus.EventMessageCreate,
			GuildID:   &message.GuildID,
			ChannelID: &message.ChannelID,
			UserIDs:   gained,
			Payload:   view,
		})
	}
	_ = oldRestricted
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
	user := s.currentUser(c)
	message, err := s.loadVisibleMessage(channel.ID, messageID, user.ID)
	if err != nil {
		notFound(c)
		return
	}
	ownerOrAdmin := ctx != nil && (ctx.Owner || ctx.SystemAdmin)
	if !canViewMessage(user.ID, bits, roleIDsFromCtx(ctx), ownerOrAdmin, message) {
		notFound(c)
		return
	}
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
		// 管理删除他人消息属敏感操作，落审计（DM 无此路径：bits 不含 ManageMessages）。
		if ctx != nil {
			audit.Log(s.db, audit.Entry{
				ActorID: &user.ID, GuildID: &ctx.Guild.ID,
				Action: "message.delete_by_moderator", TargetType: "message", TargetID: strconv.FormatInt(message.ID, 10),
				Detail: map[string]any{"channel_id": channel.ID, "author_id": message.AuthorID},
			})
		}
	}
	deletePayload := gin.H{
		"id":         strconv.FormatInt(message.ID, 10),
		"channel_id": message.ChannelID,
		"guild_id":   message.GuildID,
	}
	if message.IsEphemeral() {
		s.publishEphemeralScopedEvent(eventbus.EventMessageDelete, message, deletePayload)
	} else if isMessageRestricted(message) {
		s.publishMessageScopedEvent(eventbus.EventMessageDelete, message, deletePayload)
	} else {
		s.publishChannelScopedEvent(eventbus.EventMessageDelete, message.GuildID, message.ChannelID, deletePayload)
	}
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
	user := s.currentUser(c)
	message, err := s.loadVisibleMessage(channel.ID, messageID, user.ID)
	if err != nil {
		notFound(c)
		return
	}
	ownerOrAdmin := ctx != nil && (ctx.Owner || ctx.SystemAdmin)
	if !canViewMessage(user.ID, bits, roleIDsFromCtx(ctx), ownerOrAdmin, message) {
		notFound(c)
		return
	}
	isSysAdmin := ctx != nil && ctx.SystemAdmin
	if message.AuthorID != user.ID && !rbac.Has(bits, rbac.ManageMessages) && !isSysAdmin {
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

// publishMessageEvent 消息事件分发四分支：
//  1. ephemeral → 仅定向可见名单 ∪ 作者；
//  2. 角色限定可见 → 按受众 UserIDs 定向；
//  3. 含差异化按钮 → 按按钮可见位图分组定向；
//  4. 其余 → guild+channel 广播 / DM recipients 定向。
func (s *service) publishMessageEvent(eventType string, message model.Message, view messageView) {
	switch {
	case message.IsEphemeral():
		s.publishEphemeralMessageEvent(eventType, message, view)
	case isMessageRestricted(message):
		s.publishMessageScopedEvent(eventType, message, view)
	case cardNeedsTrim(message.Card):
		s.publishGroupedMessageEvent(eventType, message, view)
	default:
		s.publishChannelScopedEvent(eventType, message.GuildID, message.ChannelID, view)
	}
}

// publishMessageScopedEvent 按消息可见范围选择广播或定向（角色限定 / 私信）。
func (s *service) publishMessageScopedEvent(eventType string, message model.Message, payload any) {
	if message.GuildID == uuid.Nil {
		userIDs := s.loadDMRecipientIDs(message.ChannelID)
		if len(userIDs) == 0 {
			return
		}
		s.bus.Publish(eventbus.Event{
			Type:      eventType,
			ChannelID: &message.ChannelID,
			UserIDs:   userIDs,
			Payload:   payload,
		})
		return
	}
	if isMessageRestricted(message) {
		userIDs := s.resolveAudienceUserIDs(message)
		if len(userIDs) == 0 {
			return
		}
		s.bus.Publish(eventbus.Event{
			Type:      eventType,
			GuildID:   &message.GuildID,
			ChannelID: &message.ChannelID,
			UserIDs:   userIDs,
			Payload:   payload,
		})
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventType,
		GuildID:   &message.GuildID,
		ChannelID: &message.ChannelID,
		Payload:   payload,
	})
}

// publishChannelScopedEvent 服频道走 Guild+Channel 广播；私信走 recipients 定向。
func (s *service) publishChannelScopedEvent(eventType string, guildID, channelID uuid.UUID, payload any) {
	if guildID == uuid.Nil {
		userIDs := s.loadDMRecipientIDs(channelID)
		if len(userIDs) == 0 {
			return
		}
		s.bus.Publish(eventbus.Event{
			Type:      eventType,
			ChannelID: &channelID,
			UserIDs:   userIDs,
			Payload:   payload,
		})
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventType,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload:   payload,
	})
}
