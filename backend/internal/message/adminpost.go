package message

import (
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
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

// PostAsUser 以指定用户身份向频道发送一条普通消息，并广播 MESSAGE_CREATE。
// 供 adminpresence「系统管理员临场发言」复用：权限校验由调用方完成，
// 本函数只负责落库与事件下发（复用进程内唯一的雪花 ID 生成器，避免 ID 冲突）。
func PostAsUser(db *gorm.DB, bus *eventbus.Bus, guildID, channelID, authorID uuid.UUID, content string) (AdminPostResult, error) {
	msg := model.Message{
		ID:        sharedIDs.Next(),
		GuildID:   guildID,
		ChannelID: channelID,
		AuthorID:  authorID,
		Type:      model.MessageDefault,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.Create(&msg).Error; err != nil {
		return AdminPostResult{}, err
	}
	// 组装带作者用户名的视图广播（与用户端发消息一致，避免客户端 N+1 查作者）。
	view := struct {
		model.Message
		AuthorUsername string `json:"author_username"`
	}{Message: msg}
	_ = db.Model(&model.User{}).Select("username").Where("id = ?", authorID).Scan(&view.AuthorUsername).Error
	if bus != nil {
		bus.Publish(eventbus.Event{
			Type:      eventbus.EventMessageCreate,
			GuildID:   &guildID,
			ChannelID: &channelID,
			Payload:   view,
		})
	}
	return AdminPostResult{
		ID: strconv.FormatInt(msg.ID, 10), ChannelID: channelID, GuildID: guildID,
		AuthorID: authorID, Content: content, CreatedAt: msg.CreatedAt,
	}, nil
}
