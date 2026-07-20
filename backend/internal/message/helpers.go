package message

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// service 消息模块的运行时依赖集合，由 newService 装配。
// 同一套 handler 会挂到后台（/api/v1）与用户端（/gapi/v1）两个前缀：
// 两个平面各持有一个 service 实例，仅 currentUser（认证平面语义）与
// urlPrefix（响应内生成的 URL 前缀）不同，其余重资源见 newService 说明。
type service struct {
	db          *gorm.DB
	bus         *eventbus.Bus
	cfg         config.Config
	storage     Storage
	index       SearchIndex
	searchLimit *userLimiter
	ids         *snowflakeGen
	currentUser func(*gin.Context) model.User
	// urlPrefix 响应中 upload_url/download_url 的挂载前缀（如 /api/v1、/gapi/v1）。
	// 用户端响应绝不能出现 /api/v1 字样（本专项安全要求）。
	urlPrefix string
}

// ---------- 统一错误输出（对齐 httpapi 的 {"error":{"code","message"}} 格式） ----------

type errorResponse struct {
	Error apiError `json:"error"`
}
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorResponse{apiError{code, message}})
}

// notFound 统一的「不可见即 404」输出（docs 06 议题 8 防扫频）。
func notFound(c *gin.Context) {
	fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在或不可见")
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// ---------- 参数解析 ----------

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		notFound(c)
		return uuid.Nil, false
	}
	return id, true
}

func parseMessageIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("messageID"), 10, 64)
	if err != nil || id <= 0 {
		notFound(c)
		return 0, false
	}
	return id, true
}

// channelAccess 按频道 ID 定位所属服并计算调用者的频道权限。
// 频道不存在 / 不可见（含 Restriction 禁看）一律 404，不泄露存在性。
func (s *service) channelAccess(c *gin.Context, channelID uuid.UUID) (*perms.GuildContext, model.Channel, rbac.Permission, bool) {
	var channel model.Channel
	if err := s.db.First(&channel, "id = ?", channelID).Error; err != nil {
		notFound(c)
		return nil, channel, 0, false
	}
	user := s.currentUser(c)
	ctx, err := perms.LoadGuild(s.db, user, channel.GuildID)
	if err != nil {
		notFound(c)
		return nil, channel, 0, false
	}
	channel, bits, err := ctx.ChannelPerms(s.db, channel.ID)
	if err != nil {
		notFound(c)
		return nil, channel, 0, false
	}
	return ctx, channel, bits, true
}

// uploadLimitBytes 该服的单文件上限：优先服级配置，未配置回落平台默认 25MB（AT.4）。
func (s *service) uploadLimitBytes(guildID uuid.UUID) int64 {
	var cfg model.GuildMessageConfig
	if err := s.db.First(&cfg, "guild_id = ?", guildID).Error; err == nil && cfg.UploadLimitBytes > 0 {
		return cfg.UploadLimitBytes
	}
	return defaultUploadLimitBytes
}

// ---------- 响应视图 ----------

// attachmentView 消息响应中的附件元数据，download_url 为短时签名 URL（AT.7），
// preview 标注预览白名单类型（AT.5），空表示仅可下载。
type attachmentView struct {
	ID          uuid.UUID `json:"id"`
	Filename    string    `json:"filename"`
	MIME        string    `json:"mime"`
	Size        int64     `json:"size"`
	Preview     string    `json:"preview,omitempty"`
	DownloadURL string    `json:"download_url"`
}

// messageView 消息响应体：消息本体 + 作者用户名 + 附件元数据列表。
// AuthorUsername 由 messageViews 批量联查补充，避免客户端逐条查作者（N+1）；
// 后台与用户端两个前缀共用同一增强。
// Card 为卡片消息载荷（bot 专项）：原样 JSON 透传，客户端按 schema 渲染；
// AuthorIsBot 标记作者为机器人（客户端渲染 BOT 徽标）。
type messageView struct {
	model.Message
	AuthorUsername string           `json:"author_username"`
	AuthorIsBot    bool             `json:"author_is_bot,omitempty"`
	Card           json.RawMessage  `json:"card,omitempty"`
	Attachments    []attachmentView `json:"attachments"`
}

func (s *service) attachmentViews(attachments []model.Attachment, now time.Time) []attachmentView {
	views := make([]attachmentView, 0, len(attachments))
	for _, attachment := range attachments {
		views = append(views, attachmentView{
			ID:          attachment.ID,
			Filename:    attachment.Filename,
			MIME:        attachment.MIME,
			Size:        attachment.Size,
			Preview:     previewKind(attachment.MIME),
			DownloadURL: buildDownloadURL(s.urlPrefix, s.cfg.JWTSecret, attachment.ID, now),
		})
	}
	return views
}

// messageViews 批量组装消息视图（一次性查出全部附件与作者用户名，避免 N+1）。
func (s *service) messageViews(messages []model.Message) ([]messageView, error) {
	now := time.Now().UTC()
	views := make([]messageView, 0, len(messages))
	if len(messages) == 0 {
		return views, nil
	}
	ids := make([]int64, 0, len(messages))
	authorIDs := make([]uuid.UUID, 0, len(messages))
	seenAuthors := make(map[uuid.UUID]struct{}, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
		if _, ok := seenAuthors[message.AuthorID]; !ok {
			seenAuthors[message.AuthorID] = struct{}{}
			authorIDs = append(authorIDs, message.AuthorID)
		}
	}
	var attachments []model.Attachment
	if err := s.db.Where("message_id IN ?", ids).Order("created_at ASC").Find(&attachments).Error; err != nil {
		return nil, err
	}
	grouped := make(map[int64][]model.Attachment, len(messages))
	for _, attachment := range attachments {
		grouped[*attachment.MessageID] = append(grouped[*attachment.MessageID], attachment)
	}
	// 作者用户名批量联查；已注销等查不到的作者保持空字符串，不影响消息返回。
	usernames := make(map[uuid.UUID]string, len(authorIDs))
	botFlags := make(map[uuid.UUID]bool, len(authorIDs))
	var authors []model.User
	if err := s.db.Select("id", "username", "is_bot").Where("id IN ?", authorIDs).Find(&authors).Error; err != nil {
		return nil, err
	}
	for _, author := range authors {
		usernames[author.ID] = author.Username
		botFlags[author.ID] = author.IsBot
	}
	for _, message := range messages {
		var card json.RawMessage
		if message.Card != nil && *message.Card != "" {
			card = json.RawMessage(*message.Card)
		}
		views = append(views, messageView{
			Message:        message,
			AuthorUsername: usernames[message.AuthorID],
			AuthorIsBot:    botFlags[message.AuthorID],
			Card:           card,
			Attachments:    s.attachmentViews(grouped[message.ID], now),
		})
	}
	return views, nil
}

func (s *service) messageViewOne(message model.Message) (messageView, error) {
	views, err := s.messageViews([]model.Message{message})
	if err != nil {
		return messageView{}, err
	}
	return views[0], nil
}

// loadLiveMessage 取某频道内未软删的消息；不存在或已删除返回 gorm.ErrRecordNotFound。
func (s *service) loadLiveMessage(channelID uuid.UUID, messageID int64) (model.Message, error) {
	var message model.Message
	err := s.db.First(&message, "id = ? AND channel_id = ? AND deleted_at IS NULL", messageID, channelID).Error
	return message, err
}

func isRecordNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
