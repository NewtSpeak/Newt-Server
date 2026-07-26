package message

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// 流式消息（bot 专项）：为 AI 生成场景提供「占位消息 → 增量分片 → 终态」三段协议。
//
// 事件时序（Gateway 按频道可见性过滤下发，同普通消息）：
//  1. MESSAGE_STREAM_START  d = messageView（stream_status=STREAMING 的占位消息）
//  2. MESSAGE_STREAM_DELTA  d = {id, channel_id, guild_id, delta, seq}（seq 从 1 递增，客户端按序拼接）
//  3. MESSAGE_STREAM_END    d = messageView（终态，stream_status 清空，可携带 card）
//     同时补发 MESSAGE_UPDATE，保证不理解流式协议的客户端也能拿到最终正文。
//
// 正文持久化为「随分片追加写」：任一时刻拉取历史都能看到已生成部分；
// 分片序号 seq 只在进程内存维护（streamStatus=STREAMING 的孤儿消息由 GC 兜底终结）。

// streamStatusStreaming Message.StreamStatus 的进行中取值。
const streamStatusStreaming = "STREAMING"

// streamStaleAfter 流式会话闲置超时：超过后 GC 自动终结（防 bot 崩溃留下永久 STREAMING）。
const streamStaleAfter = 10 * time.Minute

// streamTracker 进程内流式分片序号表（消息 ID → 已发分片数）。
type streamTracker struct {
	mu   sync.Mutex
	seqs map[int64]int
}

var streams = &streamTracker{seqs: make(map[int64]int)}

func (t *streamTracker) next(id int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seqs[id]++
	return t.seqs[id]
}

func (t *streamTracker) drop(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seqs, id)
}

// mountStream 挂载流式消息端点（bot 开放平面）。
func (s *service) mountStream(authed *gin.RouterGroup) {
	authed.POST("/channels/:channelID/messages/stream", s.startStream)
	authed.POST("/channels/:channelID/messages/:messageID/stream", s.appendStream)
	authed.POST("/channels/:channelID/messages/:messageID/stream/end", s.endStream)
}

type startStreamRequest struct {
	Content   string `json:"content"` // 可选的首段正文
	ReplyToID string `json:"reply_to_id"`
	Nonce     string `json:"nonce" binding:"max=64"`
}

// startStream POST /channels/{id}/messages/stream：创建流式占位消息。
func (s *service) startStream(c *gin.Context) {
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
	var input startStreamRequest
	if !bind(c, &input) {
		return
	}
	if utf8.RuneCountInString(input.Content) > maxContentRunes {
		fail(c, http.StatusBadRequest, "INVALID_MESSAGE", errContentTooLong.Error())
		return
	}
	user := s.currentUser(c)
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
	message := model.Message{
		ID:           s.ids.Next(),
		GuildID:      ctx.Guild.ID,
		ChannelID:    channel.ID,
		AuthorID:     user.ID,
		Type:         model.MessageDefault,
		Content:      input.Content,
		ReplyToID:    replyToID,
		Nonce:        input.Nonce,
		StreamStatus: streamStatusStreaming,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.db.Create(&message).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建流式消息失败")
		return
	}
	view, err := s.messageViewOne(message)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	s.publishMessageEvent(eventbus.EventMessageStreamStart, message, view)
	// 流式占位创建即推进作者已读（与普通 createMessage 一致）。
	s.markAuthorReadOnSend(user.ID, channel, message.ID)
	c.JSON(http.StatusCreated, view)
}

type appendStreamRequest struct {
	Delta string `json:"delta" binding:"required"`
}

// appendStream POST /channels/{id}/messages/{mid}/stream：追加增量分片（仅作者）。
func (s *service) appendStream(c *gin.Context) {
	message, ok := s.streamingMessageAccess(c)
	if !ok {
		return
	}
	var input appendStreamRequest
	if !bind(c, &input) {
		return
	}
	if utf8.RuneCountInString(message.Content)+utf8.RuneCountInString(input.Delta) > maxContentRunes {
		fail(c, http.StatusBadRequest, "STREAM_TOO_LONG", "流式正文超过 4000 字符上限，请先 end 收束")
		return
	}
	if err := s.db.Model(&model.Message{}).Where("id = ?", message.ID).
		Update("content", gorm.Expr("content || ?", input.Delta)).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "写入分片失败")
		return
	}
	seq := streams.next(message.ID)
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventMessageStreamDelta,
		GuildID:   &message.GuildID,
		ChannelID: &message.ChannelID,
		Payload: gin.H{
			"id":         strconv.FormatInt(message.ID, 10),
			"channel_id": message.ChannelID,
			"guild_id":   message.GuildID,
			"delta":      input.Delta,
			"seq":        seq,
		},
	})
	c.JSON(http.StatusOK, gin.H{
		"seq":            seq,
		"content_length": utf8.RuneCountInString(message.Content) + utf8.RuneCountInString(input.Delta),
	})
}

type endStreamRequest struct {
	// Content 非 nil 时以其整体覆盖已追加的正文（bot 端做最终修订的场景）。
	Content *string `json:"content"`
	// Card 终态可附带卡片载荷。
	Card json.RawMessage `json:"card"`
}

// endStream POST /channels/{id}/messages/{mid}/stream/end：终结流式消息（仅作者）。
func (s *service) endStream(c *gin.Context) {
	message, ok := s.streamingMessageAccess(c)
	if !ok {
		return
	}
	var input endStreamRequest
	if !bind(c, &input) {
		return
	}
	card, _, err := validateCard(input.Card)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CARD", err.Error())
		return
	}
	updates := map[string]any{"stream_status": ""}
	if input.Content != nil {
		if utf8.RuneCountInString(*input.Content) > maxContentRunes {
			fail(c, http.StatusBadRequest, "INVALID_MESSAGE", errContentTooLong.Error())
			return
		}
		updates["content"] = *input.Content
		message.Content = *input.Content
	}
	if card != "" {
		updates["card"] = card
		message.Card = &card
	}
	if err := s.db.Model(&model.Message{}).Where("id = ?", message.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "终结流式消息失败")
		return
	}
	streams.drop(message.ID)
	message.StreamStatus = ""
	if input.Content == nil {
		// 覆盖式收束之外：重读 DB 拿到分片累计后的完整正文。
		if reloaded, err := s.loadLiveMessage(message.ChannelID, message.ID); err == nil {
			message = reloaded
		}
	}
	view, err := s.messageViewOne(message)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息失败")
		return
	}
	s.publishMessageEvent(eventbus.EventMessageStreamEnd, message, view)
	// 兼容不理解流式协议的客户端：终态同时按普通编辑事件下发。
	s.publishMessageEvent(eventbus.EventMessageUpdate, message, view)
	s.index.IndexMessage(message.ID)
	c.JSON(http.StatusOK, view)
}

// streamingMessageAccess 定位流式消息并校验：频道可见、消息存在、状态为 STREAMING、调用者为作者。
func (s *service) streamingMessageAccess(c *gin.Context) (model.Message, bool) {
	var zero model.Message
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return zero, false
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return zero, false
	}
	_, channel, _, ok := s.channelAccess(c, channelID)
	if !ok {
		return zero, false
	}
	message, err := s.loadLiveMessage(channel.ID, messageID)
	if err != nil {
		notFound(c)
		return zero, false
	}
	if message.StreamStatus != streamStatusStreaming {
		fail(c, http.StatusConflict, "NOT_STREAMING", "该消息不是进行中的流式消息")
		return zero, false
	}
	if message.AuthorID != s.currentUser(c).ID {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅流式消息的作者可操作")
		return zero, false
	}
	return message, true
}

// gcStaleStreams 终结闲置超时的流式消息（bot 崩溃/断连兜底），由后台 GC 周期调用。
func (s *service) gcStaleStreams(now time.Time) {
	var stale []model.Message
	err := s.db.Where("stream_status = ? AND created_at < ? AND deleted_at IS NULL",
		streamStatusStreaming, now.Add(-streamStaleAfter)).Limit(gcBatchSize).Find(&stale).Error
	if err != nil {
		return
	}
	for _, message := range stale {
		if err := s.db.Model(&model.Message{}).Where("id = ?", message.ID).
			Update("stream_status", "").Error; err != nil {
			continue
		}
		streams.drop(message.ID)
		message.StreamStatus = ""
		if view, err := s.messageViewOne(message); err == nil {
			s.publishMessageEvent(eventbus.EventMessageStreamEnd, message, view)
			s.publishMessageEvent(eventbus.EventMessageUpdate, message, view)
		}
		s.index.IndexMessage(message.ID)
	}
}
