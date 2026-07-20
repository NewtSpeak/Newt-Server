package message

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"gorm.io/gorm"
)

// 全系统搜索（docs 13 AU）。
//
// 与文档的偏差说明：AU.1–2 要求 OpenSearch/Elasticsearch 类专用引擎；本地单机部署
// 无外部检索组件，故抽象 SearchIndex 接口并提供 PostgreSQL FTS（tsvector + GIN）实现。
// 日后接入 OpenSearch 时实现同一接口替换注入即可，API 与 ACL 逻辑不变。
//
// PG FTS 使用 'simple' 配置的已知局限：simple 仅按空格/标点分词且不做词干化，
// 英文可正常按词命中；中文整句无空格时会被当成单个词素，只有整段完全一致才能命中，
// 无法做到真正的中文分词检索（需 pg_jieba/zhparser 扩展或外部引擎，二期处理）。
// 为缓解该局限，查询侧同时叠加 ILIKE 子串匹配作为兜底，保证中文子串可命中。

// SearchIndex 消息检索抽象。索引更新为异步（AU.6 秒级可接受）。
type SearchIndex interface {
	// IndexMessage 消息创建/编辑后重建该消息的索引条目。
	IndexMessage(id int64)
	// RemoveMessage 消息删除后将其移出索引（软删出索引）。
	RemoveMessage(id int64)
	// Search 在给定的可见频道集合内检索；ChannelIDs 即 ACL 过滤条件，必须非空。
	Search(query SearchQuery) ([]model.Message, error)
}

// SearchQuery 检索条件（AU.4）。
type SearchQuery struct {
	Text       string
	ChannelIDs []uuid.UUID // 调用方已按可见性计算好的频道集合（强制 ACL）
	AuthorID   *uuid.UUID
	BeforeID   *int64 // 雪花消息 ID 游标
	AfterID    *int64
	Limit      int
}

type indexOp struct {
	id     int64
	remove bool
}

// pgSearchIndex PostgreSQL FTS 实现：messages.content_tsv（tsvector）+ GIN 索引，
// 由后台 goroutine 队列异步维护（AU.6）。
type pgSearchIndex struct {
	db    *gorm.DB
	queue chan indexOp
}

const indexQueueSize = 4096

func newPGSearchIndex(db *gorm.DB) (*pgSearchIndex, error) {
	index := &pgSearchIndex{db: db, queue: make(chan indexOp, indexQueueSize)}
	if err := index.ensureSchema(); err != nil {
		return nil, err
	}
	go index.worker()
	return index, nil
}

// ensureSchema 追加 tsvector 列与 GIN 索引（AutoMigrate 不支持 tsvector，故用裸 DDL，幂等）。
func (p *pgSearchIndex) ensureSchema() error {
	statements := []string{
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS content_tsv tsvector`,
		`CREATE INDEX IF NOT EXISTS idx_message_content_tsv ON messages USING GIN (content_tsv)`,
	}
	for _, statement := range statements {
		if err := p.db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

// worker 串行消费索引更新队列；单条失败仅记日志（检索允许秒级滞后，不阻断消息主路径）。
func (p *pgSearchIndex) worker() {
	for op := range p.queue {
		var err error
		if op.remove {
			err = p.db.Exec(`UPDATE messages SET content_tsv = NULL WHERE id = ?`, op.id).Error
		} else {
			err = p.db.Exec(
				`UPDATE messages SET content_tsv = to_tsvector('simple', coalesce(content, '')) WHERE id = ? AND deleted_at IS NULL`,
				op.id,
			).Error
		}
		if err != nil {
			log.Printf("message: 更新搜索索引失败 id=%d err=%v", op.id, err)
		}
	}
}

func (p *pgSearchIndex) enqueue(op indexOp) {
	select {
	case p.queue <- op:
	default:
		log.Printf("message: 搜索索引队列已满，丢弃 id=%d（可通过全量重建补偿）", op.id)
	}
}

func (p *pgSearchIndex) IndexMessage(id int64)  { p.enqueue(indexOp{id: id}) }
func (p *pgSearchIndex) RemoveMessage(id int64) { p.enqueue(indexOp{id: id, remove: true}) }

// Search 执行检索：tsvector 匹配 OR ILIKE 子串兜底（中文局限缓解），叠加 ACL 与过滤条件。
func (p *pgSearchIndex) Search(query SearchQuery) ([]model.Message, error) {
	if len(query.ChannelIDs) == 0 {
		return nil, nil
	}
	tx := p.db.Model(&model.Message{}).
		Where("deleted_at IS NULL").
		Where("channel_id IN ?", query.ChannelIDs).
		Where("(content_tsv @@ plainto_tsquery('simple', ?) OR content ILIKE ?)",
			query.Text, "%"+escapeLike(query.Text)+"%")
	if query.AuthorID != nil {
		tx = tx.Where("author_id = ?", *query.AuthorID)
	}
	if query.BeforeID != nil {
		tx = tx.Where("id < ?", *query.BeforeID)
	}
	if query.AfterID != nil {
		tx = tx.Where("id > ?", *query.AfterID)
	}
	var messages []model.Message
	err := tx.Order("id DESC").Limit(query.Limit).Find(&messages).Error
	return messages, err
}

// escapeLike 转义 LIKE 元字符，避免用户输入被当成通配符。
func escapeLike(input string) string {
	replaced := make([]rune, 0, len(input))
	for _, r := range input {
		if r == '%' || r == '_' || r == '\\' {
			replaced = append(replaced, '\\')
		}
		replaced = append(replaced, r)
	}
	return string(replaced)
}

// searchMessages GET /api/v1/search/messages?q=&guild_id=&channel_id=&author_id=&before=&after=&limit=。
// ACL 强制（AU.3）：先计算调用者全部可见频道集合，再作为 SQL 过滤条件注入；
// 不可见频道（含 Restriction 禁看）不会出现在结果中，也不暴露其存在性。
// 按用户限流（AU.8）：令牌桶 1 QPS / 突发 5。
func (s *service) searchMessages(c *gin.Context) {
	user := s.currentUser(c)
	if !s.searchLimit.Allow(user.ID) {
		fail(c, http.StatusTooManyRequests, "SEARCH_RATE_LIMITED", "搜索过于频繁，请稍后再试")
		return
	}
	text := c.Query("q")
	if text == "" {
		fail(c, http.StatusBadRequest, "INVALID_QUERY", "q 不能为空")
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
	query := SearchQuery{Text: text, Limit: limit}
	if raw := c.Query("author_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_QUERY", "author_id 非法")
			return
		}
		query.AuthorID = &id
	}
	for _, cursor := range []struct {
		name   string
		target **int64
	}{{"before", &query.BeforeID}, {"after", &query.AfterID}} {
		if raw := c.Query(cursor.name); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				fail(c, http.StatusBadRequest, "INVALID_QUERY", cursor.name+" 需为消息 ID")
				return
			}
			*cursor.target = &parsed
		}
	}

	channelIDs, ok := s.visibleChannelIDs(c, user)
	if !ok {
		return
	}
	query.ChannelIDs = channelIDs
	messages, err := s.index.Search(query)
	if err != nil {
		fail(c, http.StatusInternalServerError, "SEARCH_ERROR", "搜索执行失败")
		return
	}
	views, err := s.messageViews(messages)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取附件失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": views})
}

// visibleChannelIDs 计算搜索范围内调用者可见的频道 ID 集合。
//   - 指定 guild_id：仅该服（不可见 404）；
//   - 未指定：遍历用户加入的全部服务器（系统管理员遍历全部服务器）；
//   - 指定 channel_id：结果与可见集合求交，不在集合内则 404（不暴露存在性）。
func (s *service) visibleChannelIDs(c *gin.Context, user model.User) ([]uuid.UUID, bool) {
	var guildIDs []uuid.UUID
	if raw := c.Query("guild_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			notFound(c)
			return nil, false
		}
		guildIDs = []uuid.UUID{id}
	} else if user.SystemAdmin {
		if err := s.db.Model(&model.Guild{}).Pluck("id", &guildIDs).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取服务器列表失败")
			return nil, false
		}
	} else {
		if err := s.db.Model(&model.Member{}).Where("user_id = ?", user.ID).Pluck("guild_id", &guildIDs).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取服务器列表失败")
			return nil, false
		}
	}

	var channelFilter *uuid.UUID
	if raw := c.Query("channel_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			notFound(c)
			return nil, false
		}
		channelFilter = &id
	}

	channelIDs := make([]uuid.UUID, 0, 64)
	explicitGuild := c.Query("guild_id") != ""
	for _, guildID := range guildIDs {
		ctx, err := perms.LoadGuild(s.db, user, guildID)
		if err != nil {
			if explicitGuild {
				// 显式指定了不可见的服：404。
				notFound(c)
				return nil, false
			}
			continue
		}
		visible, err := ctx.VisibleChannels(s.db)
		if err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "计算可见频道失败")
			return nil, false
		}
		for _, channel := range visible {
			channelIDs = append(channelIDs, channel.ID)
		}
	}

	if channelFilter != nil {
		for _, id := range channelIDs {
			if id == *channelFilter {
				return []uuid.UUID{id}, true
			}
		}
		notFound(c)
		return nil, false
	}
	return channelIDs, true
}
