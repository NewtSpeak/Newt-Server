package message

import (
	"log"
	"net/http"
	"strconv"
	"sync"

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
// 一期缓解（见 bigram.go）：入库时额外维护 content_bigrams tsvector（中文连续
// 字符两两切片、英文按词），查询词同样 bigram 化后经 GIN 索引 @@ 匹配，
// 中文多字词组即可命中；ILIKE 子串匹配继续保留作精确兜底（覆盖单字查询与
// 索引异步窗口期）。

// SearchIndex 消息检索抽象。索引更新为异步（AU.6 秒级可接受）。
type SearchIndex interface {
	// IndexMessage 消息创建/编辑后重建该消息的索引条目。
	IndexMessage(id int64)
	// RemoveMessage 消息删除后将其移出索引（软删出索引）。
	RemoveMessage(id int64)
	// Search 在给定的可见频道集合内检索；ChannelIDs 即 ACL 过滤条件，必须非空。
	// 返回当前页结果与命中总数（total，供客户端展示「共 N 条」与分页，docs 06 FR-15）。
	Search(query SearchQuery) ([]model.Message, int64, error)
}

// SearchQuery 检索条件（AU.4）。
type SearchQuery struct {
	Text       string
	ChannelIDs []uuid.UUID // 调用方已按可见性计算好的频道集合（强制 ACL）
	// ViewerID 检索发起者：ephemeral 消息仅作者与可见名单内用户可命中
	//（零值 UUID 表示不过滤，仅限内部/测试路径）。
	ViewerID uuid.UUID
	AuthorID *uuid.UUID
	BeforeID *int64 // 雪花消息 ID 游标
	AfterID  *int64
	Limit    int
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
	index.startBackfill()
	return index, nil
}

// ensureSchema 追加 tsvector 列与 GIN 索引（AutoMigrate 不支持 tsvector，故用裸 DDL，幂等）。
func (p *pgSearchIndex) ensureSchema() error {
	statements := []string{
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS content_tsv tsvector`,
		`CREATE INDEX IF NOT EXISTS idx_message_content_tsv ON messages USING GIN (content_tsv)`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS content_bigrams tsvector`,
		`CREATE INDEX IF NOT EXISTS idx_message_content_bigrams ON messages USING GIN (content_bigrams)`,
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
			err = p.db.Exec(`UPDATE messages SET content_tsv = NULL, content_bigrams = NULL WHERE id = ?`, op.id).Error
		} else {
			err = p.indexOne(op.id)
		}
		if err != nil {
			log.Printf("message: 更新搜索索引失败 id=%d err=%v", op.id, err)
		}
	}
}

// indexOne 重建单条消息的两列索引：bigram 切片在应用侧计算，需先取回正文。
func (p *pgSearchIndex) indexOne(id int64) error {
	var row struct{ Content string }
	result := p.db.Raw(`SELECT content FROM messages WHERE id = ? AND deleted_at IS NULL`, id).Scan(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil // 已删或不存在：无需索引
	}
	return p.db.Exec(
		`UPDATE messages SET content_tsv = to_tsvector('simple', ?), content_bigrams = to_tsvector('simple', ?) WHERE id = ? AND deleted_at IS NULL`,
		row.Content, bigramTokens(row.Content), id,
	).Error
}

// searchBackfillOnce 存量回填进程内只启动一次（后台/用户端两个平面各构造一个
// 索引实例，靠它去重）。
var searchBackfillOnce sync.Once

// startBackfill 启动后台幂等回填 goroutine（存量消息补 content_bigrams）。
func (p *pgSearchIndex) startBackfill() {
	searchBackfillOnce.Do(func() {
		go func() {
			if err := RebuildSearchBigrams(p.db); err != nil {
				log.Printf("message: 存量搜索索引回填失败: %v", err)
			}
		}()
	})
}

// RebuildSearchBigrams 分批回填 content_bigrams 为 NULL 的存量消息（顺带补齐
// content_tsv），幂等可重复执行；供启动后台任务与测试调用。多实例并发执行时
// 更新彼此幂等，无需互斥。
func RebuildSearchBigrams(db *gorm.DB) error {
	const batchSize = 500
	for {
		var rows []struct {
			ID      int64
			Content string
		}
		err := db.Raw(
			`SELECT id, content FROM messages WHERE content_bigrams IS NULL AND deleted_at IS NULL ORDER BY id LIMIT ?`,
			batchSize,
		).Scan(&rows).Error
		if err != nil {
			return err
		}
		for _, row := range rows {
			err := db.Exec(
				`UPDATE messages SET content_tsv = to_tsvector('simple', ?), content_bigrams = to_tsvector('simple', ?) WHERE id = ? AND deleted_at IS NULL`,
				row.Content, bigramTokens(row.Content), row.ID,
			).Error
			if err != nil {
				return err
			}
		}
		if len(rows) < batchSize {
			return nil
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

// Search 执行检索：整词 tsvector 匹配 OR bigram 匹配（中文词组经 GIN 索引命中，
// 多词查询 AND 语义、无需相邻）OR ILIKE 子串兜底（单字查询与索引异步窗口期），
// 叠加 ACL 与过滤条件。先 COUNT 命中总数再取当前页（docs 06 FR-15「共 N 条结果」）。
// 排序保持 id DESC：before/after 为消息 ID 游标，要求单调序，不能混入相关度排序。
func (p *pgSearchIndex) Search(query SearchQuery) ([]model.Message, int64, error) {
	if len(query.ChannelIDs) == 0 {
		return nil, 0, nil
	}
	tx := p.db.Model(&model.Message{}).
		Where("deleted_at IS NULL").
		Where("channel_id IN ?", query.ChannelIDs).
		Where("(content_tsv @@ plainto_tsquery('simple', ?) OR content_bigrams @@ plainto_tsquery('simple', ?) OR content ILIKE ?)",
			query.Text, bigramTokens(query.Text), "%"+escapeLike(query.Text)+"%")
	if query.ViewerID != uuid.Nil {
		tx = tx.Scopes(visibleToScope(query.ViewerID))
	}
	if query.AuthorID != nil {
		tx = tx.Where("author_id = ?", *query.AuthorID)
	}
	var total int64
	if err := tx.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if query.BeforeID != nil {
		tx = tx.Where("id < ?", *query.BeforeID)
	}
	if query.AfterID != nil {
		tx = tx.Where("id > ?", *query.AfterID)
	}
	var messages []model.Message
	err := tx.Order("id DESC").Limit(query.Limit).Find(&messages).Error
	return messages, total, err
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
		// retry_after 供客户端倒计时（docs 06 FR-14）；令牌桶 1 QPS，1 秒后即有新配额。
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       gin.H{"code": "SEARCH_RATE_LIMITED", "message": "搜索过于频繁，请稍后再试"},
			"retry_after": 1,
		})
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
	query := SearchQuery{Text: text, Limit: limit, ViewerID: user.ID}
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
	messages, total, err := s.index.Search(query)
	if err != nil {
		fail(c, http.StatusInternalServerError, "SEARCH_ERROR", "搜索执行失败")
		return
	}
	views, err := s.messageViews(messages, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取附件失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": views, "total": total})
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
