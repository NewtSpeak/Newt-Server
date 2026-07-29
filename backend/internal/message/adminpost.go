package message

import (
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// AdminPostResult 管理员临场发言的结果视图（供调用方返回给控制台）。
type AdminPostResult struct {
	ID        string    `json:"id"`
	ChannelID uuid.UUID `json:"channel_id"`
	GuildID   uuid.UUID `json:"guild_id"`
	AuthorID  uuid.UUID `json:"author_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// postSideEffects 后台平面 Register 装配后的 service，供 PostAsUser 复用
// messageView / bumpMentionCounts / 搜索索引（与普通 createMessage 副作用对齐）。
// 未 Register 时（单测仅测落库）回落到最小广播路径。
var postSideEffects *service

// PostAsUser 以指定用户身份向频道发送一条系统管理员临场消息，并广播 MESSAGE_CREATE。
// 供 adminpresence「系统管理员临场发言」复用：权限校验由调用方完成，
// 本函数负责落库、提及解析、事件下发与提及计数（复用进程内唯一的雪花 ID 生成器）。
// type=SYSTEM_ADMIN，客户端按金色皇冠 +「@ 系统超级管理员」徽章渲染；
// 客户端对 SYSTEM_ADMIN 不走「自己发的自动已读」，以便频道/服务器列表正确显示未读。
func PostAsUser(db *gorm.DB, bus *eventbus.Bus, guildID, channelID, authorID uuid.UUID, content string) (AdminPostResult, error) {
	// 系统管理员具备全部权限位（含 MENTION_EVERYONE），与控制台临场身份一致。
	adminBits := ^rbac.Permission(0)
	var mentions resolvedMentions
	if postSideEffects != nil {
		resolved, err := postSideEffects.resolveMentions(guildID, content, adminBits)
		if err != nil {
			return AdminPostResult{}, err
		}
		mentions = resolved
	} else {
		// 无 service 时仍解析 wire format（仅用户/角色存在性校验略过成员表过滤）
		tokens := parseMentionTokens(content)
		mentions = resolvedMentions{
			Users:    model.UUIDList(tokens.UserIDs),
			Roles:    model.UUIDList(tokens.RoleIDs),
			Everyone: everyoneEffective(tokens, adminBits),
		}
	}

	msg := model.Message{
		ID:              sharedIDs.Next(),
		GuildID:         guildID,
		ChannelID:       channelID,
		AuthorID:        authorID,
		Type:            model.MessageSystemAdmin,
		Content:         content,
		Mentions:        mentions.Users,
		MentionRoles:    mentions.Roles,
		MentionEveryone: mentions.Everyone,
		CreatedAt:       time.Now().UTC(),
	}
	if msg.Mentions == nil {
		msg.Mentions = model.UUIDList{}
	}
	if msg.MentionRoles == nil {
		msg.MentionRoles = model.UUIDList{}
	}
	if err := db.Create(&msg).Error; err != nil {
		return AdminPostResult{}, err
	}

	// 优先走与 createMessage 相同的 messageView + 提及计数 + 索引路径，
	// 保证 MESSAGE_CREATE 载荷与用户端发消息一致，客户端 noteMessageCreate 可正确推进未读。
	if postSideEffects != nil {
		view, err := postSideEffects.messageViewOne(msg)
		if err != nil {
			// 视图组装失败不回滚落库：回落到最小广播，避免控制台 500 而消息已写入
			publishAdminMessageMinimal(bus, db, msg, authorID)
		} else {
			postSideEffects.publishMessageEvent(eventbus.EventMessageCreate, msg, view)
			postSideEffects.bumpMentionCounts(msg)
			postSideEffects.index.IndexMessage(msg.ID)
		}
	} else if bus != nil {
		publishAdminMessageMinimal(bus, db, msg, authorID)
	}

	return AdminPostResult{
		ID: strconv.FormatInt(msg.ID, 10), ChannelID: channelID, GuildID: guildID,
		AuthorID: authorID, Content: content, CreatedAt: msg.CreatedAt,
	}, nil
}

// publishAdminMessageMinimal 无 service 或视图失败时的最小 MESSAGE_CREATE 广播
//（与 messageView 字段对齐：attachments/reactions 显式空数组）。
func publishAdminMessageMinimal(bus *eventbus.Bus, db *gorm.DB, msg model.Message, authorID uuid.UUID) {
	if bus == nil {
		return
	}
	view := struct {
		model.Message
		AuthorUsername string     `json:"author_username"`
		Attachments    []struct{} `json:"attachments"`
		Reactions      []struct{} `json:"reactions"`
	}{
		Message:     msg,
		Attachments: []struct{}{},
		Reactions:   []struct{}{},
	}
	if db != nil {
		_ = db.Model(&model.User{}).Select("username").Where("id = ?", authorID).Scan(&view.AuthorUsername).Error
	}
	bus.Publish(eventbus.Event{
		Type:      eventbus.EventMessageCreate,
		GuildID:   &msg.GuildID,
		ChannelID: &msg.ChannelID,
		Payload:   view,
	})
}
