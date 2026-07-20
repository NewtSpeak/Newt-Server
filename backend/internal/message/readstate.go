package message

import (
	"log"
	"net/http"
	"strconv"
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
//   - MESSAGE_CREATE 时为被提及者批量递增 mention_count（见 bumpMentionCounts）；
//   - ack 后向本人全部端定向发 READ_STATE_UPDATE（跨端角标同步）。

// readStateUpdatePayload READ_STATE_UPDATE 事件载荷（docs 15 §7-1，拟名落地）。
type readStateUpdatePayload struct {
	ChannelID         uuid.UUID `json:"channel_id"`
	LastReadMessageID string    `json:"last_read_message_id"`
	MentionCount      int       `json:"mention_count"`
	EventAt           time.Time `json:"event_at"`
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
	state := model.ReadState{
		UserID:            user.ID,
		ChannelID:         channel.ID,
		GuildID:           channel.GuildID,
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
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新已读状态失败")
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:    eventbus.EventReadStateUpdate,
		UserIDs: []uuid.UUID{user.ID},
		Payload: readStateUpdatePayload{
			ChannelID:         channel.ID,
			LastReadMessageID: strconv.FormatInt(state.LastReadMessageID, 10),
			MentionCount:      0,
			EventAt:           now,
		},
	})
	c.JSON(http.StatusOK, state)
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
	c.JSON(http.StatusOK, gin.H{"read_states": states})
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
	err = s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"mention_count": gorm.Expr("read_states.mention_count + 1"),
			"updated_at":    now,
		}),
	}).Create(&rows).Error
	if err != nil {
		log.Printf("message: 批量递增 mention_count 失败（message=%d）: %v", message.ID, err)
	}
}

// mentionRecipients 计算应递增计数的用户集合（去重、排除作者、按频道可见性过滤）。
func (s *service) mentionRecipients(message model.Message) ([]uuid.UUID, error) {
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
