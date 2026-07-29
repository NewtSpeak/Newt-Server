package message

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
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
	// interactLimit 按钮交互限流（设计文档 2026-07-26）：每用户 2 QPS、突发 5。
	interactLimit *userLimiter
	ids           *snowflakeGen
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

// dmPermissionBits 私信域固定权限（Server-16 BN.2：不参与 RBAC）。
// 含消息读写/反应/附件/交互按钮；不含 ManageMessages / MentionEveryone（删除仅作者本人）。
const dmPermissionBits = rbac.ViewChannel | rbac.SendMessages | rbac.ReadMessageHistory |
	rbac.AddReactions | rbac.AttachFiles | rbac.EmbedLinks | rbac.UseExternalEmojis |
	rbac.UseApplicationCommands

// channelAccess 按频道 ID 定位所属服并计算调用者的频道权限。
// 频道不存在 / 不可见（含 Restriction 禁看）一律 404，不泄露存在性。
// DM/GROUP_DM：校验 channel_recipients 成员资格，返回 nil GuildContext + dmPermissionBits。
func (s *service) channelAccess(c *gin.Context, channelID uuid.UUID) (*perms.GuildContext, model.Channel, rbac.Permission, bool) {
	var channel model.Channel
	if err := s.db.First(&channel, "id = ?", channelID).Error; err != nil {
		notFound(c)
		return nil, channel, 0, false
	}
	user := s.currentUser(c)

	if channel.Type.IsPrivate() {
		var n int64
		s.db.Model(&model.ChannelRecipient{}).
			Where("channel_id = ? AND user_id = ?", channel.ID, user.ID).
			Count(&n)
		if n == 0 {
			notFound(c)
			return nil, channel, 0, false
		}
		return nil, channel, dmPermissionBits, true
	}

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
	// 上锁频道：可见但未解锁时拒绝访问消息内容（管理员/服主/系统管可绕过）。
	if !perms.IsChannelUnlocked(s.db, user.ID, channel, ctx, bits) {
		fail(c, http.StatusForbidden, "CHANNEL_LOCKED", "频道已上锁，请先输入访问密码")
		return nil, channel, 0, false
	}
	return ctx, channel, bits, true
}

// loadDMRecipientIDs 私信频道参与者 user_id 列表。
func (s *service) loadDMRecipientIDs(channelID uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	_ = s.db.Model(&model.ChannelRecipient{}).
		Where("channel_id = ?", channelID).
		Pluck("user_id", &ids).Error
	return ids
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
	ID       uuid.UUID `json:"id"`
	Filename string    `json:"filename"`
	MIME     string    `json:"mime"`
	Size     int64     `json:"size"`
	// Width/Height 图片像素尺寸（docs 07 §8-5，非图片为 0 省略）。
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Preview     string `json:"preview,omitempty"`
	DownloadURL string `json:"download_url"`
}

// reactionSummary 消息响应中的反应聚合（Newt-Desktop docs 05 FR-26）：
// 每种 emoji 一条，count 为总数；me 仅在传入 viewer 时填充（REST 列表/单条），
// Gateway 广播的 MESSAGE_CREATE/UPDATE 无 per-recipient viewer，省略 me，
// 避免客户端把广播侧的 false 当成「我未反应」而清掉本地高亮。
type reactionSummary struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Me    *bool  `json:"me,omitempty"`
}

// messageView 消息响应体：消息本体 + 作者用户名 + 附件元数据列表 + 反应聚合。
// AuthorUsername 由 messageViews 批量联查补充，避免客户端逐条查作者（N+1）；
// 后台与用户端两个前缀共用同一增强。
// Card 为卡片消息载荷（bot 专项）：原样 JSON 透传，客户端按 schema 渲染；
// AuthorIsBot 标记作者为机器人（客户端渲染 BOT 徽标）。
// StickerItems 贴图消息载荷（docs 17）：type=STICKER 时透出快照数组。
type messageView struct {
	model.Message
	AuthorUsername string           `json:"author_username"`
	AuthorIsBot    bool             `json:"author_is_bot,omitempty"`
	Card           json.RawMessage  `json:"card,omitempty"`
	StickerItems   json.RawMessage  `json:"sticker_items,omitempty"`
	Attachments    []attachmentView `json:"attachments"`
	Reactions      []reactionSummary `json:"reactions"`
}

func (s *service) attachmentViews(attachments []model.Attachment, now time.Time, restricted bool) []attachmentView {
	views := make([]attachmentView, 0, len(attachments))
	for _, attachment := range attachments {
		views = append(views, attachmentView{
			ID:          attachment.ID,
			Filename:    attachment.Filename,
			MIME:        attachment.MIME,
			Size:        attachment.Size,
			Width:       attachment.Width,
			Height:      attachment.Height,
			Preview:     previewKind(attachment.MIME),
			DownloadURL: buildDownloadURL(s.urlPrefix, s.cfg.JWTSecret, attachment.ID, now, restricted),
		})
	}
	return views
}

// messageViews 批量组装消息视图（一次性查出全部附件、作者用户名与反应聚合，
// 避免 N+1）。viewer 可选：传入时反应聚合的 me 字段按该用户计算（docs 05 FR-26）。
func (s *service) messageViews(messages []model.Message, viewer ...uuid.UUID) ([]messageView, error) {
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
	// 反应聚合：一次分组统计全部消息的 emoji 计数；viewer 提供时再查出其已反应集合。
	viewerID := uuid.Nil
	if len(viewer) > 0 {
		viewerID = viewer[0]
	}
	type reactionCountRow struct {
		MessageID int64
		Emoji     string
		Count     int
	}
	var countRows []reactionCountRow
	if err := s.db.Model(&model.MessageReaction{}).
		Select("message_id, emoji, COUNT(*) AS count").
		Where("message_id IN ?", ids).
		Group("message_id, emoji").Order("MIN(created_at) ASC").
		Scan(&countRows).Error; err != nil {
		return nil, err
	}
	mine := make(map[int64]map[string]bool)
	if viewerID != uuid.Nil && len(countRows) > 0 {
		var myRows []model.MessageReaction
		if err := s.db.Select("message_id", "emoji").
			Where("message_id IN ? AND user_id = ?", ids, viewerID).
			Find(&myRows).Error; err != nil {
			return nil, err
		}
		for _, row := range myRows {
			if mine[row.MessageID] == nil {
				mine[row.MessageID] = map[string]bool{}
			}
			mine[row.MessageID][row.Emoji] = true
		}
	}
	reactionGroups := make(map[int64][]reactionSummary)
	for _, row := range countRows {
		summary := reactionSummary{Emoji: row.Emoji, Count: row.Count}
		if viewerID != uuid.Nil {
			me := mine[row.MessageID][row.Emoji]
			summary.Me = &me
		}
		reactionGroups[row.MessageID] = append(reactionGroups[row.MessageID], summary)
	}
	// 差异化按钮裁剪（设计文档 2026-07-26）：仅当 card 含 visible_to 声明时才付出
	// 解析成本；viewer 角色集一次批查（搜索结果可跨服，按消息 guild 全集查询）。
	viewerRoles := s.viewerRolesForTrim(messages, viewerID)
	for _, message := range messages {
		var card json.RawMessage
		if message.Card != nil && *message.Card != "" {
			card = json.RawMessage(*message.Card)
			if cardNeedsTrim(message.Card) && message.AuthorID != viewerID {
				if buttons, err := parseCardButtons(*message.Card); err == nil && len(buttons) > 0 {
					bitmap := buttonVisibilityBitmap(buttons, viewerID, viewerRoles)
					card = json.RawMessage(trimCardButtons(*message.Card, buttons, bitmap))
				}
			}
		}
		var stickerItems json.RawMessage
		if message.StickerItems != nil && *message.StickerItems != "" {
			stickerItems = json.RawMessage(*message.StickerItems)
		}
		reactions := reactionGroups[message.ID]
		if reactions == nil {
			reactions = []reactionSummary{}
		}
		views = append(views, messageView{
			Message:        message,
			AuthorUsername: usernames[message.AuthorID],
			AuthorIsBot:    botFlags[message.AuthorID],
			Card:           card,
			StickerItems:   stickerItems,
			Attachments:    s.attachmentViews(grouped[message.ID], now, isMessageRestricted(message)),
			Reactions:      reactions,
		})
	}
	return views, nil
}

// viewerRolesForTrim 为按钮裁剪批量取 viewer 在相关服的角色集合：
// 仅当批次内存在需裁剪的 card 且 viewer 非空时才查询（角色 UUID 全局唯一，跨服并集安全）。
func (s *service) viewerRolesForTrim(messages []model.Message, viewerID uuid.UUID) map[uuid.UUID]bool {
	if viewerID == uuid.Nil {
		return nil
	}
	guildIDs := make([]uuid.UUID, 0, 2)
	seen := make(map[uuid.UUID]struct{}, 2)
	for _, message := range messages {
		if message.GuildID == uuid.Nil || message.AuthorID == viewerID || !cardNeedsTrim(message.Card) {
			continue
		}
		if _, ok := seen[message.GuildID]; !ok {
			seen[message.GuildID] = struct{}{}
			guildIDs = append(guildIDs, message.GuildID)
		}
	}
	if len(guildIDs) == 0 {
		return nil
	}
	var roleIDs []uuid.UUID
	err := s.db.Raw(`SELECT member_roles.role_id FROM members
		JOIN member_roles ON member_roles.member_id = members.id
		WHERE members.user_id = ? AND members.guild_id IN ?`, viewerID, guildIDs).Scan(&roleIDs).Error
	if err != nil {
		return nil
	}
	roles := make(map[uuid.UUID]bool, len(roleIDs))
	for _, id := range roleIDs {
		roles[id] = true
	}
	return roles
}

func (s *service) messageViewOne(message model.Message, viewer ...uuid.UUID) (messageView, error) {
	views, err := s.messageViews([]model.Message{message}, viewer...)
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

// loadVisibleMessage loadLiveMessage 的 viewer 感知变体：ephemeral 消息对
// 名单外用户视同不存在（不可见即 404，防泄露存在性）。
func (s *service) loadVisibleMessage(channelID uuid.UUID, messageID int64, viewer uuid.UUID) (model.Message, error) {
	message, err := s.loadLiveMessage(channelID, messageID)
	if err != nil {
		return message, err
	}
	if !ephemeralVisibleTo(message, viewer) {
		return model.Message{}, gorm.ErrRecordNotFound
	}
	return message, nil
}

// ephemeralVisibleTo ephemeral 可见性判定：公开消息恒可见；否则仅作者与名单内用户。
func ephemeralVisibleTo(message model.Message, viewer uuid.UUID) bool {
	if !message.IsEphemeral() {
		return true
	}
	if message.AuthorID == viewer {
		return true
	}
	for _, id := range message.VisibleTo {
		if id == viewer {
			return true
		}
	}
	return false
}

// visibleToScope ephemeral 历史过滤谓词（listMessages / search 共用）：
// 公开消息 OR 作者本人 OR 名单内用户（jsonb @> 包含判定）。
func visibleToScope(viewer uuid.UUID) func(*gorm.DB) *gorm.DB {
	member, _ := json.Marshal([]uuid.UUID{viewer})
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("(visible_to = '[]'::jsonb OR author_id = ? OR visible_to @> ?::jsonb)",
			viewer, string(member))
	}
}

func isRecordNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
