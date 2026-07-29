package social

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/message"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// privateChannelView 1:1 / 群组私信摘要（Server-16 BR.2）。
type privateChannelView struct {
	ID                uuid.UUID     `json:"id"`
	Type              string        `json:"type"`
	Name              string        `json:"name,omitempty"`
	Recipients        []userSummary `json:"recipients"`
	LastMessageID     string        `json:"last_message_id,omitempty"`
	LastReadMessageID string        `json:"last_read_message_id"`
	MentionCount      int           `json:"mention_count"`
	UnreadCount       int64         `json:"unread_count"`
	// LastMessage 列表预览（Discord 式侧栏副标题）
	LastMessage    *lastMessagePreview `json:"last_message,omitempty"`
	MessageRequest bool                `json:"message_request"`
	Hidden         bool                `json:"hidden"`
	CreatedAt      time.Time           `json:"created_at"`
	// BlockState 1:1 拉黑状态："" | "blocked_by_me" | "blocked_by_peer"
	// blocked_by_me：我拉黑了对方；blocked_by_peer：对方拉黑了我（对外伪装好友验证）
	BlockState string `json:"block_state,omitempty"`
}

type lastMessagePreview struct {
	ID        string `json:"id"`
	AuthorID  string `json:"author_id"`
	Content   string `json:"content"`
	Type      string `json:"type,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// listPrivateChannelViews 组装当前用户可见私信摘要（READY / REST 共用）。
// onlyMessageRequest=true 时仅请求箱；否则返回全部非 hidden（含请求箱）。
// 1:1 DM 按对端去重：只保留有历史/更合适的那一条，避免重复入口。
func (h *api) listPrivateChannelViews(userID uuid.UUID, onlyMessageRequest bool) []privateChannelView {
	query := h.deps.DB.Model(&model.ChannelRecipient{}).Where("user_id = ?", userID)
	if onlyMessageRequest {
		query = query.Where("message_request = true AND hidden = false")
	} else {
		query = query.Where("hidden = false")
	}
	var rows []model.ChannelRecipient
	if err := query.Order("joined_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return []privateChannelView{}
	}
	out := make([]privateChannelView, 0, len(rows))
	for _, row := range rows {
		view, ok := h.buildPrivateChannelView(row.ChannelID, userID, &row)
		if !ok {
			continue
		}
		out = append(out, view)
	}
	out = dedupePrivateChannelViews(out)
	// 按最近消息雪花 ID 降序（= 时间）；无消息的按 created_at 回落
	sort.SliceStable(out, func(i, j int) bool {
		return privateChannelNewer(out[i], out[j])
	})
	return out
}

// privateChannelNewer 报告 a 是否应排在 b 前面（更新）。
func privateChannelNewer(a, b privateChannelView) bool {
	li, lj := a.LastMessageID, b.LastMessageID
	if li == "" && lj == "" {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if li == "" {
		return false
	}
	if lj == "" {
		return true
	}
	ai, _ := strconv.ParseInt(li, 10, 64)
	aj, _ := strconv.ParseInt(lj, 10, 64)
	return ai > aj
}

// preferPrivateChannel 在两条同对端 1:1 中选出应保留的会话（有历史优先）。
func preferPrivateChannel(a, b privateChannelView) privateChannelView {
	// 非请求箱优先于请求箱
	if a.MessageRequest != b.MessageRequest {
		if !a.MessageRequest {
			return a
		}
		return b
	}
	// 有最近消息的优先；都有则取更新的
	if a.LastMessageID != "" || b.LastMessageID != "" {
		if privateChannelNewer(a, b) {
			return a
		}
		return b
	}
	// 都无消息：保留更早创建的（原始会话）
	if a.CreatedAt.Before(b.CreatedAt) {
		return a
	}
	return b
}

// dedupePrivateChannelViews 1:1 按对端 user_id 去重；GROUP_DM 按 id。
func dedupePrivateChannelViews(in []privateChannelView) []privateChannelView {
	byPeer := map[string]privateChannelView{}
	rest := make([]privateChannelView, 0, len(in))
	for _, ch := range in {
		if ch.Type != string(model.ChannelDM) {
			rest = append(rest, ch)
			continue
		}
		peer := ""
		if len(ch.Recipients) > 0 {
			peer = ch.Recipients[0].ID.String()
		}
		if peer == "" {
			rest = append(rest, ch)
			continue
		}
		if prev, ok := byPeer[peer]; ok {
			byPeer[peer] = preferPrivateChannel(prev, ch)
		} else {
			byPeer[peer] = ch
		}
	}
	out := make([]privateChannelView, 0, len(rest)+len(byPeer))
	out = append(out, rest...)
	for _, ch := range byPeer {
		out = append(out, ch)
	}
	return out
}

// listPrivateChannels GET /users/@me/channels
// filter=message_request 时只返回仍在请求箱的会话。
func (h *api) listPrivateChannels(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	filterMR := c.Query("filter") == "message_request"
	out := h.listPrivateChannelViews(me.ID, filterMR)
	if out == nil {
		out = []privateChannelView{}
	}
	c.JSON(http.StatusOK, gin.H{"channels": out})
}

type createPrivateChannelRequest struct {
	RecipientID *string  `json:"recipient_id"`
	Recipients  []string `json:"recipients"` // GROUP_DM：其他成员（不含自己）
	Name        *string  `json:"name"`       // GROUP_DM 可选名称
}

// createPrivateChannel POST /users/@me/channels
// {recipient_id} → 1:1 DM get-or-create；{recipients:[]} → GROUP_DM（Server-16 BR.2）。
func (h *api) createPrivateChannel(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	var input createPrivateChannelRequest
	if !bind(c, &input) {
		return
	}

	// 群组私信
	if len(input.Recipients) > 0 {
		h.createGroupDM(c, me.ID, input.Recipients, input.Name)
		return
	}

	if input.RecipientID == nil || *input.RecipientID == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "需要 recipient_id 或 recipients")
		return
	}
	targetID, err := uuid.Parse(*input.RecipientID)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "recipient_id 非法")
		return
	}
	if targetID == me.ID {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "不能与自己开启私信")
		return
	}

	// 目标用户必须存在
	var target model.User
	if err := h.deps.DB.First(&target, "id = ? AND disabled_at IS NULL", targetID).Error; err != nil {
		fail(c, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在")
		return
	}

	// 已有会话：重开权威会话（hidden→false），历史消息保留在同一 channel_id 上。
	// 拉黑状态下仍可打开已有会话查看历史（不可新建、不可发送）。
	if existing, ok := h.findDMChannel(me.ID, targetID); ok {
		h.reopenDMChannel(me.ID, targetID, existing)
		var myRow model.ChannelRecipient
		_ = h.deps.DB.First(&myRow, "channel_id = ? AND user_id = ?", existing, me.ID).Error
		view, _ := h.buildPrivateChannelView(existing, me.ID, &myRow)
		c.JSON(http.StatusOK, view)
		return
	}

	// 无会话且任一方拉黑：禁止新建
	if h.isBlocked(me.ID, targetID) {
		fail(c, http.StatusForbidden, "PRIVACY_DENIED", "无法完成操作")
		return
	}

	// 2–7. 新会话隐私裁决
	friends := h.hasFriend(me.ID, targetID)
	if !friends {
		if denied, asMessageRequest := h.dmPrivacyCheck(me.ID, targetID); denied {
			fail(c, http.StatusForbidden, "PRIVACY_DENIED", "无法完成操作")
			return
		} else if asMessageRequest {
			// 创建为消息请求：对端 message_request=true
			view, err := h.createDMChannel(me.ID, targetID, true)
			if err != nil {
				fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建私信失败")
				return
			}
			c.JSON(http.StatusCreated, view)
			return
		}
	}

	view, err := h.createDMChannel(me.ID, targetID, false)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建私信失败")
		return
	}
	c.JSON(http.StatusCreated, view)
}

const maxGroupDMMembers = 10 // 含自己，故 recipients ≤ 9

// createGroupDM 创建群组私信：仅可邀请自己的好友；人满拒绝。
func (h *api) createGroupDM(c *gin.Context, me uuid.UUID, rawIDs []string, name *string) {
	seen := map[uuid.UUID]struct{}{me: {}}
	ids := make([]uuid.UUID, 0, len(rawIDs))
	for _, s := range rawIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "recipients 含非法 ID")
			return
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) < 1 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "群组私信至少需要 1 名其他成员")
		return
	}
	if len(ids)+1 > maxGroupDMMembers {
		fail(c, http.StatusBadRequest, "GROUP_DM_FULL", "群组私信最多 10 人")
		return
	}

	for _, id := range ids {
		var user model.User
		if err := h.deps.DB.First(&user, "id = ? AND disabled_at IS NULL", id).Error; err != nil {
			fail(c, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在")
			return
		}
		if h.isBlocked(me, id) {
			fail(c, http.StatusForbidden, "PRIVACY_DENIED", "无法完成操作")
			return
		}
		// 仅可拉自己的好友（BN.1 / §13-D3）
		if !h.hasFriend(me, id) {
			fail(c, http.StatusForbidden, "PRIVACY_DENIED", "只能邀请自己的好友")
			return
		}
	}

	groupName := "group"
	if name != nil {
		n := strings.TrimSpace(*name)
		if len([]rune(n)) > 100 {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "群名称过长")
			return
		}
		if n != "" {
			groupName = n
		}
	}

	now := time.Now().UTC()
	channelID := uuid.New()
	all := append([]uuid.UUID{me}, ids...)
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		ch := model.Channel{
			ID:      channelID,
			GuildID: uuid.Nil,
			Name:    groupName,
			Type:    model.ChannelGroupDM,
		}
		if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		rows := make([]model.ChannelRecipient, 0, len(all))
		for _, uid := range all {
			rows = append(rows, model.ChannelRecipient{
				ChannelID: channelID, UserID: uid, JoinedAt: now,
				Hidden: false, MessageRequest: false,
			})
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建群组私信失败")
		return
	}

	// 推 CHANNEL_CREATE 给全体
	if h.deps.Bus != nil {
		for _, uid := range all {
			var row model.ChannelRecipient
			_ = h.deps.DB.First(&row, "channel_id = ? AND user_id = ?", channelID, uid).Error
			view, ok := h.buildPrivateChannelView(channelID, uid, &row)
			if !ok {
				continue
			}
			h.deps.Bus.Publish(eventbus.Event{
				Type:    eventbus.EventChannelCreate,
				UserIDs: []uuid.UUID{uid},
				Payload: view,
			})
		}
	}

	// 系统灰条：创建者拉入成员
	creatorName := h.displayNameOf(me)
	for _, id := range ids {
		message.PostPrivateSystem(
			h.deps.DB, h.deps.Bus, channelID, me,
			model.MessageSystemRecipientAdd,
			creatorName+" 邀请了 "+h.displayNameOf(id),
		)
	}

	var myRow model.ChannelRecipient
	_ = h.deps.DB.First(&myRow, "channel_id = ? AND user_id = ?", channelID, me).Error
	view, _ := h.buildPrivateChannelView(channelID, me, &myRow)
	c.JSON(http.StatusCreated, view)
}

func (h *api) displayNameOf(userID uuid.UUID) string {
	sum, err := h.loadUserSummary(userID)
	if err != nil {
		return "某人"
	}
	if sum.DisplayName != "" {
		return sum.DisplayName
	}
	if sum.Username != "" {
		return sum.Username
	}
	return "某人"
}

// dmPrivacyCheck 非好友新私信裁决。
// denied=true → 403；asMessageRequest=true → 进请求箱。
func (h *api) dmPrivacyCheck(sender, receiver uuid.UUID) (denied bool, asMessageRequest bool) {
	p := h.loadOrDefaultPrivacy(receiver)
	switch p.DmFrom {
	case model.DmFromNobody, model.DmFromFriends:
		// friends：调用方已确认非好友
		return true, false
	case model.DmFromMutualGuilds:
		if !h.shareAllowedGuild(sender, receiver) {
			return true, false
		}
	case model.DmFromEveryone:
		// 无共同服 → 强制消息请求箱（BM.5 第 7 步）
		if h.mutualGuildCount(sender, receiver) == 0 {
			return false, true
		}
		// 有共同服但对方在所有共同服关闭 allow_dm → 拒绝
		if !h.shareAllowedGuild(sender, receiver) {
			return true, false
		}
	default:
		return true, false
	}
	if p.MessageRequestFilter {
		return false, true
	}
	return false, false
}

// findDMChannel 查找 a/b 之间的权威 1:1 会话。
// 优先级：有消息历史的（最近消息雪花更大）> 更早创建的空会话。
// 绝不能取「最新创建的空壳」——关闭后再开会把用户带到无历史的新 id。
func (h *api) findDMChannel(a, b uuid.UUID) (uuid.UUID, bool) {
	return findDMChannelDB(h.deps.DB, a, b)
}

func findDMChannelDB(db *gorm.DB, a, b uuid.UUID) (uuid.UUID, bool) {
	// 注意：不可 Scan 进裸 uuid.UUID —— pgx/lib/pq 常把 uuid 以 string 返回，
	// 扫进 [16]byte 会失败并被当成「无会话」，进而反复创建空壳 1:1。
	var row struct {
		ID string `gorm:"column:id"`
	}
	// last_msg 用消息表 max(id)；无消息时 NULL，NULLS LAST 后按 created_at ASC 取最早那条
	err := db.Raw(`
		SELECT c.id::text AS id
		FROM channels c
		INNER JOIN channel_recipients cr1 ON cr1.channel_id = c.id AND cr1.user_id = ?
		INNER JOIN channel_recipients cr2 ON cr2.channel_id = c.id AND cr2.user_id = ?
		LEFT JOIN LATERAL (
			SELECT m.id AS last_id
			FROM messages m
			WHERE m.channel_id = c.id AND m.deleted_at IS NULL
			ORDER BY m.id DESC
			LIMIT 1
		) lm ON TRUE
		WHERE c.type = ?
		ORDER BY lm.last_id DESC NULLS LAST, c.created_at ASC
		LIMIT 1
	`, a, b, model.ChannelDM).Scan(&row).Error
	if err != nil || row.ID == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(row.ID)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

// listDMChannelIDs 返回 a/b 之间全部 1:1 channel id（含 hidden）。
func listDMChannelIDs(db *gorm.DB, a, b uuid.UUID) []uuid.UUID {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	_ = db.Raw(`
		SELECT c.id::text AS id
		FROM channels c
		INNER JOIN channel_recipients cr1 ON cr1.channel_id = c.id AND cr1.user_id = ?
		INNER JOIN channel_recipients cr2 ON cr2.channel_id = c.id AND cr2.user_id = ?
		WHERE c.type = ?
	`, a, b, model.ChannelDM).Scan(&rows).Error
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if id, err := uuid.Parse(r.ID); err == nil && id != uuid.Nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// reopenDMChannel 打开权威会话：
//  1. 对本用户 unhide keep；
//  2. 把同对端其它 1:1 副本里的消息并入 keep（关闭后再开仍见完整历史）；
//  3. 删除已掏空的重复频道。
func (h *api) reopenDMChannel(me, peer, keep uuid.UUID) {
	_ = h.deps.DB.Model(&model.ChannelRecipient{}).
		Where("channel_id = ? AND user_id = ?", keep, me).
		Updates(map[string]any{"hidden": false}).Error

	for _, id := range listDMChannelIDs(h.deps.DB, me, peer) {
		if id == keep {
			continue
		}
		// 消息迁入权威会话（雪花 id 全局唯一，改 channel_id 不冲突）
		_ = h.deps.DB.Exec(
			`UPDATE messages SET channel_id = ? WHERE channel_id = ?`,
			keep, id,
		).Error
		// 读状态合并到 keep（列名对齐 model.ReadState）
		_ = h.deps.DB.Exec(`
			INSERT INTO read_states (user_id, channel_id, guild_id, last_read_message_id, mention_count, updated_at)
			SELECT user_id, ?::uuid, guild_id, last_read_message_id, mention_count, updated_at
			FROM read_states WHERE channel_id = ?
			ON CONFLICT (user_id, channel_id) DO UPDATE SET
				last_read_message_id = GREATEST(read_states.last_read_message_id, EXCLUDED.last_read_message_id),
				mention_count = GREATEST(read_states.mention_count, EXCLUDED.mention_count),
				updated_at = GREATEST(read_states.updated_at, EXCLUDED.updated_at)
		`, keep, id).Error
		_ = h.deps.DB.Exec(`DELETE FROM read_states WHERE channel_id = ?`, id).Error

		_ = h.deps.DB.Where("channel_id = ?", id).Delete(&model.ChannelRecipient{}).Error
		_ = h.deps.DB.Where("id = ?", id).Delete(&model.Channel{}).Error
	}
}

func (h *api) createDMChannel(a, b uuid.UUID, messageRequestForB bool) (privateChannelView, error) {
	now := time.Now().UTC()
	channelID := uuid.New()
	created := false
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		// 按用户对排序加事务锁，避免并发 POST 各建一条 1:1 DM
		u1, u2 := a, b
		if u1.String() > u2.String() {
			u1, u2 = u2, u1
		}
		_ = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?), hashtext(?))`,
			u1.String(), u2.String()).Error

		if existing, ok := findDMChannelDB(tx, a, b); ok {
			channelID = existing
			return tx.Model(&model.ChannelRecipient{}).
				Where("channel_id = ? AND user_id = ?", channelID, a).
				Updates(map[string]any{"hidden": false}).Error
		}
		ch := model.Channel{
			ID:      channelID,
			GuildID: uuid.Nil,
			Name:    "dm",
			Type:    model.ChannelDM,
		}
		if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		rows := []model.ChannelRecipient{
			{ChannelID: channelID, UserID: a, JoinedAt: now, Hidden: false, MessageRequest: false},
			{ChannelID: channelID, UserID: b, JoinedAt: now, Hidden: false, MessageRequest: messageRequestForB},
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return privateChannelView{}, err
	}

	// 收敛同对端其它副本（含事务外可见的历史脏数据）
	if !created {
		h.reopenDMChannel(a, b, channelID)
	}

	var myRow model.ChannelRecipient
	_ = h.deps.DB.First(&myRow, "channel_id = ? AND user_id = ?", channelID, a).Error
	view, _ := h.buildPrivateChannelView(channelID, a, &myRow)

	// 仅新建时推 CHANNEL_CREATE；重开已有会话不推，避免客户端误以为新频道
	if created {
		h.publishPrivateChannelCreate(channelID, a, b)
	}
	return view, nil
}

func (h *api) publishPrivateChannelCreate(channelID, a, b uuid.UUID) {
	if h.deps.Bus == nil {
		return
	}
	for _, uid := range []uuid.UUID{a, b} {
		var row model.ChannelRecipient
		_ = h.deps.DB.First(&row, "channel_id = ? AND user_id = ?", channelID, uid).Error
		view, ok := h.buildPrivateChannelView(channelID, uid, &row)
		if !ok {
			continue
		}
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventChannelCreate,
			UserIDs: []uuid.UUID{uid},
			Payload: view,
		})
	}
}

func (h *api) buildPrivateChannelView(channelID, viewerID uuid.UUID, myRow *model.ChannelRecipient) (privateChannelView, bool) {
	var ch model.Channel
	if err := h.deps.DB.First(&ch, "id = ?", channelID).Error; err != nil {
		return privateChannelView{}, false
	}
	if !ch.Type.IsPrivate() {
		return privateChannelView{}, false
	}
	var recipients []model.ChannelRecipient
	_ = h.deps.DB.Where("channel_id = ?", channelID).Find(&recipients).Error
	summaries := make([]userSummary, 0, len(recipients))
	for _, r := range recipients {
		if r.UserID == viewerID {
			continue // 列表里通常只展示对方；仍可含全部，此处排除自己以对齐 Discord
		}
		sum, err := h.loadUserSummary(r.UserID)
		if err != nil {
			continue
		}
		summaries = append(summaries, sum)
	}
	// 若只有自己（异常），也返回空 recipients
	var lastID string
	var lastPreview *lastMessagePreview
	var last model.Message
	if err := h.deps.DB.Select("id", "author_id", "content", "type", "created_at").
		Where("channel_id = ? AND deleted_at IS NULL", channelID).
		Order("id DESC").First(&last).Error; err == nil && last.ID != 0 {
		lastID = strconv.FormatInt(last.ID, 10)
		content := last.Content
		// 预览截断，避免列表过长
		runes := []rune(content)
		if len(runes) > 80 {
			content = string(runes[:80]) + "…"
		}
		lastPreview = &lastMessagePreview{
			ID:        lastID,
			AuthorID:  last.AuthorID.String(),
			Content:   content,
			Type:      string(last.Type),
			CreatedAt: last.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	var readState model.ReadState
	if err := h.deps.DB.Where("user_id = ? AND channel_id = ?", viewerID, channelID).
		Find(&readState).Error; err != nil {
		return privateChannelView{}, false
	}
	lastReadID := readState.LastReadMessageID
	var unreadCount int64
	if err := h.deps.DB.Model(&model.Message{}).
		Where("channel_id = ? AND id > ? AND deleted_at IS NULL", channelID, lastReadID).
		Where("type NOT IN ?", []model.MessageType{
			model.MessageSystem,
			model.MessageSystemRecipientAdd,
			model.MessageSystemRecipientRemove,
			model.MessageSystemChannelNameChange,
		}).
		Count(&unreadCount).Error; err != nil {
		return privateChannelView{}, false
	}
	mr := false
	hidden := false
	if myRow != nil {
		mr = myRow.MessageRequest
		hidden = myRow.Hidden
	}
	displayName := ""
	if ch.Type == model.ChannelGroupDM {
		// 占位名 group 不展示给客户端
		if ch.Name != "" && ch.Name != "group" && ch.Name != "dm" {
			displayName = ch.Name
		}
	}
	blockState := ""
	if ch.Type == model.ChannelDM && len(summaries) > 0 {
		blockState = h.dmBlockState(viewerID, summaries[0].ID)
	}
	return privateChannelView{
		ID:                ch.ID,
		Type:              string(ch.Type),
		Name:              displayName,
		Recipients:        summaries,
		LastMessageID:     lastID,
		LastReadMessageID: strconv.FormatInt(lastReadID, 10),
		MentionCount:      readState.MentionCount,
		UnreadCount:       unreadCount,
		LastMessage:       lastPreview,
		MessageRequest:    mr,
		Hidden:            hidden,
		CreatedAt:         ch.CreatedAt,
		BlockState:        blockState,
	}, true
}

// dmBlockState 返回 viewer 相对 peer 的拉黑状态。
func (h *api) dmBlockState(viewer, peer uuid.UUID) string {
	var n int64
	h.deps.DB.Model(&model.Relationship{}).
		Where("type = ? AND user_id = ? AND target_user_id = ?",
			model.RelationshipBlocked, viewer, peer).Count(&n)
	if n > 0 {
		return "blocked_by_me"
	}
	h.deps.DB.Model(&model.Relationship{}).
		Where("type = ? AND user_id = ? AND target_user_id = ?",
			model.RelationshipBlocked, peer, viewer).Count(&n)
	if n > 0 {
		return "blocked_by_peer"
	}
	return ""
}

// patchRecipientMe PATCH /channels/:channelID/recipients/@me
// {hidden} 关闭私信 | {message_request:false} 接受请求箱。
func (h *api) patchRecipientMe(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var row model.ChannelRecipient
	if err := h.deps.DB.First(&row, "channel_id = ? AND user_id = ?", channelID, me.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var input struct {
		Hidden         *bool `json:"hidden"`
		MessageRequest *bool `json:"message_request"`
	}
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{}
	if input.Hidden != nil {
		updates["hidden"] = *input.Hidden
		row.Hidden = *input.Hidden
	}
	if input.MessageRequest != nil {
		// 仅允许 false（接受）
		if *input.MessageRequest {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "message_request 仅可设为 false 以接受")
			return
		}
		updates["message_request"] = false
		row.MessageRequest = false
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "无更新字段")
		return
	}
	if err := h.deps.DB.Model(&model.ChannelRecipient{}).
		Where("channel_id = ? AND user_id = ?", channelID, me.ID).
		Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新失败")
		return
	}
	view, _ := h.buildPrivateChannelView(channelID, me.ID, &row)
	// 接受请求箱 / 关闭会话：推 CHANNEL_UPDATE 给本人多端
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventChannelUpdate,
			UserIDs: []uuid.UUID{me.ID},
			Payload: view,
		})
	}
	c.JSON(http.StatusOK, view)
}

// leaveGroupDM DELETE /channels/:channelID/recipients/@me
// 离开群组；1:1 DM 请用 PATCH hidden。全员离开则软删频道（BN.6）。
func (h *api) leaveGroupDM(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var ch model.Channel
	if err := h.deps.DB.First(&ch, "id = ?", channelID).Error; err != nil || !ch.Type.IsPrivate() {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if ch.Type == model.ChannelDM {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "1:1 私信请使用关闭（hidden），不能离开")
		return
	}
	var row model.ChannelRecipient
	if err := h.deps.DB.First(&row, "channel_id = ? AND user_id = ?", channelID, me.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if err := h.deps.DB.Delete(&row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "离开失败")
		return
	}

	// 推本人 CHANNEL_DELETE
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventChannelDelete,
			UserIDs: []uuid.UUID{me.ID},
			Payload: gin.H{"channel_id": channelID, "guild_id": uuid.Nil},
		})
	}

	// 通知剩余成员 CHANNEL_UPDATE + 系统灰条
	var remaining []model.ChannelRecipient
	_ = h.deps.DB.Where("channel_id = ?", channelID).Find(&remaining).Error
	if len(remaining) == 0 {
		// 全员离开：删除频道
		_ = h.deps.DB.Delete(&model.Channel{}, "id = ?", channelID).Error
	} else {
		message.PostPrivateSystem(
			h.deps.DB, h.deps.Bus, channelID, me.ID,
			model.MessageSystemRecipientRemove,
			h.displayNameOf(me.ID)+" 离开了群组",
		)
		if h.deps.Bus != nil {
			for _, r := range remaining {
				view, ok := h.buildPrivateChannelView(channelID, r.UserID, &r)
				if !ok {
					continue
				}
				h.deps.Bus.Publish(eventbus.Event{
					Type:    eventbus.EventChannelUpdate,
					UserIDs: []uuid.UUID{r.UserID},
					Payload: view,
				})
			}
		}
	}
	c.Status(http.StatusNoContent)
}

// patchPrivateChannel PATCH /channels/:channelID — GROUP_DM 改名（任何成员）。
func (h *api) patchPrivateChannel(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var ch model.Channel
	if err := h.deps.DB.First(&ch, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if ch.Type != model.ChannelGroupDM {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "仅群组私信可改名")
		return
	}
	var n int64
	h.deps.DB.Model(&model.ChannelRecipient{}).
		Where("channel_id = ? AND user_id = ?", channelID, me.ID).Count(&n)
	if n == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var input struct {
		Name *string `json:"name"`
	}
	if !bind(c, &input) {
		return
	}
	if input.Name == nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "需要 name")
		return
	}
	name := strings.TrimSpace(*input.Name)
	if len([]rune(name)) > 100 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "群名称过长")
		return
	}
	if name == "" {
		name = "group"
	}
	if err := h.deps.DB.Model(&ch).Update("name", name).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "改名失败")
		return
	}
	ch.Name = name

	label := name
	if name == "group" {
		label = "（未命名）"
	}
	message.PostPrivateSystem(
		h.deps.DB, h.deps.Bus, channelID, me.ID,
		model.MessageSystemChannelNameChange,
		h.displayNameOf(me.ID)+" 将群名称改为 "+label,
	)

	var recipients []model.ChannelRecipient
	_ = h.deps.DB.Where("channel_id = ?", channelID).Find(&recipients).Error
	if h.deps.Bus != nil {
		for _, r := range recipients {
			view, ok := h.buildPrivateChannelView(channelID, r.UserID, &r)
			if !ok {
				continue
			}
			h.deps.Bus.Publish(eventbus.Event{
				Type:    eventbus.EventChannelUpdate,
				UserIDs: []uuid.UUID{r.UserID},
				Payload: view,
			})
		}
	}
	var myRow model.ChannelRecipient
	_ = h.deps.DB.First(&myRow, "channel_id = ? AND user_id = ?", channelID, me.ID).Error
	view, _ := h.buildPrivateChannelView(channelID, me.ID, &myRow)
	c.JSON(http.StatusOK, view)
}

// inviteGroupDM PUT /channels/:channelID/recipients/:userID — 仅可邀自己的好友。
func (h *api) inviteGroupDM(c *gin.Context) {
	me := h.deps.CurrentUser(c)
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	targetID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "user_id 非法")
		return
	}
	var ch model.Channel
	if err := h.deps.DB.First(&ch, "id = ?", channelID).Error; err != nil || ch.Type != model.ChannelGroupDM {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var n int64
	h.deps.DB.Model(&model.ChannelRecipient{}).
		Where("channel_id = ? AND user_id = ?", channelID, me.ID).Count(&n)
	if n == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if !h.hasFriend(me.ID, targetID) {
		fail(c, http.StatusForbidden, "PRIVACY_DENIED", "只能邀请自己的好友")
		return
	}
	if h.isBlocked(me.ID, targetID) {
		fail(c, http.StatusForbidden, "PRIVACY_DENIED", "无法完成操作")
		return
	}
	var count int64
	h.deps.DB.Model(&model.ChannelRecipient{}).Where("channel_id = ?", channelID).Count(&count)
	// 已是成员？
	var existing model.ChannelRecipient
	if err := h.deps.DB.First(&existing, "channel_id = ? AND user_id = ?", channelID, targetID).Error; err == nil {
		view, _ := h.buildPrivateChannelView(channelID, me.ID, nil)
		c.JSON(http.StatusOK, view)
		return
	}
	if count >= maxGroupDMMembers {
		fail(c, http.StatusBadRequest, "GROUP_DM_FULL", "群组已满")
		return
	}
	row := model.ChannelRecipient{
		ChannelID: channelID, UserID: targetID, JoinedAt: time.Now().UTC(),
	}
	if err := h.deps.DB.Create(&row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "邀请失败")
		return
	}

	message.PostPrivateSystem(
		h.deps.DB, h.deps.Bus, channelID, me.ID,
		model.MessageSystemRecipientAdd,
		h.displayNameOf(me.ID)+" 邀请了 "+h.displayNameOf(targetID),
	)

	// 新成员 CHANNEL_CREATE；其余 CHANNEL_UPDATE
	if h.deps.Bus != nil {
		viewNew, ok := h.buildPrivateChannelView(channelID, targetID, &row)
		if ok {
			h.deps.Bus.Publish(eventbus.Event{
				Type:    eventbus.EventChannelCreate,
				UserIDs: []uuid.UUID{targetID},
				Payload: viewNew,
			})
		}
		var all []model.ChannelRecipient
		_ = h.deps.DB.Where("channel_id = ?", channelID).Find(&all).Error
		for _, r := range all {
			if r.UserID == targetID {
				continue
			}
			view, ok := h.buildPrivateChannelView(channelID, r.UserID, &r)
			if !ok {
				continue
			}
			h.deps.Bus.Publish(eventbus.Event{
				Type:    eventbus.EventChannelUpdate,
				UserIDs: []uuid.UUID{r.UserID},
				Payload: view,
			})
		}
	}

	var myRow model.ChannelRecipient
	_ = h.deps.DB.First(&myRow, "channel_id = ? AND user_id = ?", channelID, me.ID).Error
	view, _ := h.buildPrivateChannelView(channelID, me.ID, &myRow)
	c.JSON(http.StatusOK, view)
}
