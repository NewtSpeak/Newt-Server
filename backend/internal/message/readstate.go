package message

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 未读与提及计数（docs 15 §3.1 / §7-1）：
//   - ack 端点推进 last_read_message_id（只前进不后退）并清零 mention_count；
//   - 本人发消息时自动推进已读至本条（markAuthorReadOnSend），避免刷新后仍显示未读；
//   - MESSAGE_CREATE 时为被提及者批量递增 mention_count（见 bumpMentionCounts）；
//   - ack 后向本人全部端定向发 READ_STATE_UPDATE（跨端角标同步）。

// readStateUpdatePayload READ_STATE_UPDATE 事件载荷（docs 15 §7-1，拟名落地）。
type readStateUpdatePayload struct {
	UserID            uuid.UUID `json:"user_id"`
	ChannelID         uuid.UUID `json:"channel_id"`
	LastReadMessageID string    `json:"last_read_message_id"`
	MentionCount      int       `json:"mention_count"`
	EventAt           time.Time `json:"event_at"`
}

// publishReadState 向当事人全部端定向发 READ_STATE_UPDATE（多端角标同步）。
func (s *service) publishReadState(state model.ReadState, at time.Time) {
	s.bus.Publish(eventbus.Event{
		Type:    eventbus.EventReadStateUpdate,
		UserIDs: []uuid.UUID{state.UserID},
		Payload: readStateUpdatePayload{
			UserID:            state.UserID,
			ChannelID:         state.ChannelID,
			LastReadMessageID: strconv.FormatInt(state.LastReadMessageID, 10),
			MentionCount:      state.MentionCount,
			EventAt:           at,
		},
	})
}

// advanceReadState ack 核心：推进 last_read（GREATEST 只前进不后退）并清零 mention_count，
// 返回落库后的最终行（RETURNING）。
func (s *service) advanceReadState(userID uuid.UUID, channel model.Channel, messageID int64, now time.Time) (model.ReadState, error) {
	// 私信频道 Channel.GuildID 可能为零 UUID，与消息落库语义一致。
	guildID := channel.GuildID
	if channel.Type.IsPrivate() {
		guildID = uuid.Nil
	}
	state := model.ReadState{
		UserID:            userID,
		ChannelID:         channel.ID,
		GuildID:           guildID,
		LastReadMessageID: messageID,
		MentionCount:      0,
		UpdatedAt:         now,
	}
	err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"last_read_message_id": gorm.Expr("GREATEST(read_states.last_read_message_id, EXCLUDED.last_read_message_id)"),
			"mention_count":        0,
			"updated_at":           now,
		}),
	}, clause.Returning{}).Create(&state).Error
	return state, err
}

// markAuthorReadOnSend 作者发出普通消息后，服务端自动将本人 last_read 推进到本条并清零 mention。
//
// 背景：客户端对「自己发的」会本地乐观已读，但若本地游标已追上最新，ack() 会短路不上报；
// 刷新后 READY 以服务端 read_states 为准，本人消息会被算进 unread_count。发送路径落库可保证
// 刷新/跨端一致（对齐 Discord：发消息即读到频道头）。
// SYSTEM_ADMIN 临场发言不调用本函数（公告语义，作者端也应显示未读，见 adminpost）。
// 失败只记日志，不阻断发消息主流程。
func (s *service) markAuthorReadOnSend(userID uuid.UUID, channel model.Channel, messageID int64) {
	now := time.Now().UTC()
	state, err := s.advanceReadState(userID, channel, messageID, now)
	if err != nil {
		log.Printf("message: 作者自动已读失败（channel=%s message=%d）: %v", channel.ID, messageID, err)
		return
	}
	s.publishReadState(state, now)
}

// ackMessage POST /channels/{id}/messages/{mid}/ack：本人已读推进。
//   - last_read_message_id 只前进不后退（雪花 ID 单调可比，GREATEST 取大者，FR-02）；
//   - ack 时 mention_count 清零；
//   - 不要求 mid 对应消息仍存在（对齐 Discord：客户端可 ack 本地已知的任意消息位置，
//     软删/GC 后的 ID 仍是合法的读位置游标）；
//   - 成功后向本人全部端定向发 READ_STATE_UPDATE（多端角标同步，docs 15 §6 多端阅读）。
func (s *service) ackMessage(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	messageID, ok := parseMessageIDParam(c)
	if !ok {
		return
	}
	_, channel, _, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	user := s.currentUser(c)
	now := time.Now().UTC()
	state, err := s.advanceReadState(user.ID, channel, messageID, now)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新已读状态失败")
		return
	}
	s.publishReadState(state, now)
	c.JSON(http.StatusOK, state)
}

// ackChannel POST /channels/{id}/ack {message_id}：本人已读推进（体内传消息 ID 的等价形态，
// docs 15 §7-1；message_id 兼容字符串与数字两种 JSON 形态）。语义与 ackMessage 一致，响应 204。
func (s *service) ackChannel(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	var input struct {
		MessageID flexInt64 `json:"message_id"`
	}
	if !bind(c, &input) {
		return
	}
	if input.MessageID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_MESSAGE_ID", "message_id 必须为正整数（字符串或数字）")
		return
	}
	_, channel, _, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	user := s.currentUser(c)
	now := time.Now().UTC()
	state, err := s.advanceReadState(user.ID, channel, int64(input.MessageID), now)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新已读状态失败")
		return
	}
	s.publishReadState(state, now)
	c.Status(http.StatusNoContent)
}

// ackGuild POST /guilds/{id}/ack：该服全部可见频道标记已读（docs 15 FR-02 Shift+Esc）。
//   - 每个可见频道推进到其当前最新消息 ID（含软删——ID 只是读位置游标）并清零 mention_count；
//   - 无消息且无存量 read state 的频道跳过（不制造空行）；
//   - 批量 upsert 一条 SQL 落库，随后对每个受影响频道向本人全部端发 READ_STATE_UPDATE；
//   - 非成员 404（不可见即不存在）。响应 204。
func (s *service) ackGuild(c *gin.Context) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	user := s.currentUser(c)
	ctx, err := perms.LoadGuild(s.db, user, guildID)
	if err != nil {
		notFound(c)
		return
	}
	channels, err := ctx.VisibleChannels(s.db)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取频道列表失败")
		return
	}
	if len(channels) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	channelIDs := make([]uuid.UUID, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	// 每频道当前最新消息 ID（单条聚合 SQL，不逐频道查询）。
	targets, err := snapshot.ChannelLastMessageIDs(s.db, channelIDs)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息位置失败")
		return
	}
	// 消息已被 GC 但仍有存量计数的频道也要清零（目标读位置取 0，GREATEST 保持原位）。
	var staleChannelIDs []uuid.UUID
	err = s.db.Model(&model.ReadState{}).Where("user_id = ? AND channel_id IN ?", user.ID, channelIDs).
		Pluck("channel_id", &staleChannelIDs).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取已读状态失败")
		return
	}
	for _, channelID := range staleChannelIDs {
		if _, ok := targets[channelID]; !ok {
			targets[channelID] = 0
		}
	}
	if len(targets) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	now := time.Now().UTC()
	rows := make([]model.ReadState, 0, len(targets))
	for channelID, lastRead := range targets {
		rows = append(rows, model.ReadState{
			UserID:            user.ID,
			ChannelID:         channelID,
			GuildID:           guildID,
			LastReadMessageID: lastRead,
			MentionCount:      0,
			UpdatedAt:         now,
		})
	}
	err = s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"last_read_message_id": gorm.Expr("GREATEST(read_states.last_read_message_id, EXCLUDED.last_read_message_id)"),
			"mention_count":        0,
			"updated_at":           now,
		}),
	}, clause.Returning{}).Create(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新已读状态失败")
		return
	}
	for _, state := range rows {
		s.publishReadState(state, now)
	}
	c.Status(http.StatusNoContent)
}

// flexInt64 兼容字符串与数字两种 JSON 形态的 int64
//（消息 ID 在响应中序列化为字符串，客户端可能原样回传）。
type flexInt64 int64

func (v *flexInt64) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		*v = 0
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*v = flexInt64(parsed)
	return nil
}

// readStateView GET /users/@me/read-states 的响应条目：read state 行 +
// 该频道当前最大消息 ID（与 READY guilds[].channels[].last_message_id 同源，
// 保持两条同步路径信息一致——客户端据此恢复「普通未读」白点）。
type readStateView struct {
	model.ReadState
	LastMessageID int64 `json:"last_message_id,string"`
}

// listMyReadStates GET /users/@me/read-states?guild_id=：REST 兜底（docs 15 §7-1），
// 断线重连不走 READY 时可用于全量校正计数。返回本人全部已落库的 read state；
// 常规同步以 READY 快照（按可见频道过滤）为准。
func (s *service) listMyReadStates(c *gin.Context) {
	user := s.currentUser(c)
	query := s.db.Where("user_id = ?", user.ID)
	if raw := c.Query("guild_id"); raw != "" {
		guildID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_GUILD", "guild_id 非法")
			return
		}
		query = query.Where("guild_id = ?", guildID)
	}
	states := []model.ReadState{}
	if err := query.Order("updated_at DESC").Find(&states).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取已读状态失败")
		return
	}
	channelIDs := make([]uuid.UUID, 0, len(states))
	for _, state := range states {
		channelIDs = append(channelIDs, state.ChannelID)
	}
	lastMessageIDs, err := snapshot.ChannelLastMessageIDs(s.db, channelIDs)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取消息位置失败")
		return
	}
	views := make([]readStateView, 0, len(states))
	for _, state := range states {
		views = append(views, readStateView{ReadState: state, LastMessageID: lastMessageIDs[state.ChannelID]})
	}
	c.JSON(http.StatusOK, gin.H{"read_states": views})
}

// bumpMentionCounts MESSAGE_CREATE 后为被提及用户（在线与离线一视同仁，计数落库）
// 递增其该频道 mention_count：
//   - 用户提及直接计；角色提及展开到持有该角色的成员；mention_everyone 展开到
//     频道可见成员；作者自己不计（docs 15 FR-04）；
//   - 全部接收者按频道可见性过滤：禁看（view_text Restriction 收紧掉 VIEW_CHANNEL）
//     或覆盖不可见的用户既收不到事件也不产生计数（docs 12 §6.2 / docs 15 US-8）；
//   - 单条 SQL 批量 UPSERT（INSERT ... ON CONFLICT mention_count+1），角色/everyone
//     大扇出不逐行写库；
//   - MESSAGE_DELETE 不回滚计数（对齐 Discord：删除已读区间内的提及不追溯扣减，
//     客户端按 FR-05 本地扣减渲染，重同步以服务端计数为准，简单一致）；
//   - 计数失败只记日志不影响消息发送主流程（事件已广播，重同步可校正）。
func (s *service) bumpMentionCounts(message model.Message) {
	if !message.MentionEveryone && len(message.Mentions) == 0 && len(message.MentionRoles) == 0 {
		return
	}
	recipients, err := s.mentionRecipients(message)
	if err != nil {
		log.Printf("message: 计算提及接收者失败（message=%d）: %v", message.ID, err)
		return
	}
	// ephemeral：仅可见名单内的被提及者计数（名单外用户收不到消息事件，
	// 若仍加角标会形成永远消不掉的幽灵计数 + 存在性泄露）。
	if message.IsEphemeral() {
		allowed := make(map[uuid.UUID]struct{}, len(message.VisibleTo))
		for _, id := range message.VisibleTo {
			allowed[id] = struct{}{}
		}
		filtered := recipients[:0]
		for _, id := range recipients {
			if _, ok := allowed[id]; ok {
				filtered = append(filtered, id)
			}
		}
		recipients = filtered
	}
	if len(recipients) == 0 {
		return
	}
	now := time.Now().UTC()
	rows := make([]model.ReadState, 0, len(recipients))
	for _, userID := range recipients {
		rows = append(rows, model.ReadState{
			UserID:       userID,
			ChannelID:    message.ChannelID,
			GuildID:      message.GuildID,
			MentionCount: 1,
			UpdatedAt:    now,
		})
	}
	// RETURNING 拿到落库后的最终行（含累计后的 mention_count 与既有 last_read），
	// 逐用户定向发 READ_STATE_UPDATE——在线端实时加角标，离线端靠 READY 快照。
	err = s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"mention_count": gorm.Expr("read_states.mention_count + 1"),
			"updated_at":    now,
		}),
	}, clause.Returning{}).Create(&rows).Error
	if err != nil {
		log.Printf("message: 批量递增 mention_count 失败（message=%d）: %v", message.ID, err)
		return
	}
	for _, state := range rows {
		s.publishReadState(state, now)
	}
}

// mentionRecipients 计算应递增计数的用户集合（去重、排除作者、按频道可见性过滤）。
func (s *service) mentionRecipients(message model.Message) ([]uuid.UUID, error) {
	// 私信：仅 recipients 中被 @ 的用户（无 everyone/role）。
	if message.GuildID == uuid.Nil {
		ids := make([]uuid.UUID, 0, len(message.Mentions))
		for _, userID := range message.Mentions {
			if userID != message.AuthorID {
				ids = append(ids, userID)
			}
		}
		return ids, nil
	}
	// mention_everyone：直接取频道可见成员（已含 Restriction 收紧），其余提及无需再并入。
	if message.MentionEveryone {
		viewers, err := snapshot.ChannelViewers(s.db, message.GuildID, message.ChannelID)
		if err != nil {
			return nil, err
		}
		return excludeUser(viewers, message.AuthorID), nil
	}
	candidates := make(map[uuid.UUID]struct{}, len(message.Mentions))
	for _, userID := range message.Mentions {
		candidates[userID] = struct{}{}
	}
	if len(message.MentionRoles) > 0 {
		// 角色提及展开到成员：一条 SQL 拿全（member_roles → members）。
		var roleUserIDs []uuid.UUID
		err := s.db.Raw(`SELECT DISTINCT members.user_id FROM members
			JOIN member_roles ON member_roles.member_id = members.id
			WHERE members.guild_id = ? AND member_roles.role_id IN ?`,
			message.GuildID, []uuid.UUID(message.MentionRoles)).Scan(&roleUserIDs).Error
		if err != nil {
			return nil, err
		}
		for _, userID := range roleUserIDs {
			candidates[userID] = struct{}{}
		}
	}
	delete(candidates, message.AuthorID)
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	// 可见性过滤：逐用户走 perms（含覆盖与 view_text Restriction 收紧）。
	// 定向提及的候选集通常很小；大扇出的 everyone 已在上方分支批量处理。
	var users []model.User
	if err := s.db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	visible := make([]uuid.UUID, 0, len(users))
	for _, user := range users {
		if perms.CanSeeChannel(s.db, user, message.GuildID, message.ChannelID) {
			visible = append(visible, user.ID)
		}
	}
	return visible, nil
}

func excludeUser(ids []uuid.UUID, exclude uuid.UUID) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id != exclude {
			result = append(result, id)
		}
	}
	return result
}
