package social

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPrivateChannelViewUnreadCount(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过私信未读数据库集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("开启事务失败: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	suffix := uuid.NewString()
	viewer := model.User{
		ID: uuid.New(), Username: "dm_viewer_" + suffix[:8],
		Email: "dm_viewer_" + suffix + "@test.local", PasswordHash: "test",
	}
	friend := model.User{
		ID: uuid.New(), Username: "dm_friend_" + suffix[:8],
		Email: "dm_friend_" + suffix + "@test.local", PasswordHash: "test",
	}
	if err := tx.Create([]model.User{viewer, friend}).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	channel := model.Channel{
		ID: uuid.New(), GuildID: uuid.Nil, Name: "dm", Type: model.ChannelDM,
	}
	if err := tx.Create(&channel).Error; err != nil {
		t.Fatalf("创建私信频道失败: %v", err)
	}
	now := time.Now().UTC()
	rows := []model.ChannelRecipient{
		{ChannelID: channel.ID, UserID: viewer.ID, JoinedAt: now},
		{ChannelID: channel.ID, UserID: friend.ID, JoinedAt: now},
	}
	if err := tx.Create(&rows).Error; err != nil {
		t.Fatalf("创建私信参与者失败: %v", err)
	}

	baseID := time.Now().UnixNano()
	messages := []model.Message{
		{ID: baseID, GuildID: uuid.Nil, ChannelID: channel.ID, AuthorID: friend.ID, Type: model.MessageDefault, Content: "已读", CreatedAt: now},
		{ID: baseID + 1, GuildID: uuid.Nil, ChannelID: channel.ID, AuthorID: friend.ID, Type: model.MessageDefault, Content: "未读一", CreatedAt: now},
		{ID: baseID + 2, GuildID: uuid.Nil, ChannelID: channel.ID, AuthorID: friend.ID, Type: model.MessageDefault, Content: "未读二", CreatedAt: now},
		{ID: baseID + 3, GuildID: uuid.Nil, ChannelID: channel.ID, AuthorID: friend.ID, Type: model.MessageDefault, Content: "未读三", CreatedAt: now},
		{ID: baseID + 4, GuildID: uuid.Nil, ChannelID: channel.ID, AuthorID: friend.ID, Type: model.MessageSystem, Content: "系统灰条", CreatedAt: now},
	}
	if err := tx.Create(&messages).Error; err != nil {
		t.Fatalf("创建测试消息失败: %v", err)
	}
	state := model.ReadState{
		UserID: viewer.ID, ChannelID: channel.ID, GuildID: uuid.Nil,
		LastReadMessageID: baseID, MentionCount: 2, UpdatedAt: now,
	}
	if err := tx.Create(&state).Error; err != nil {
		t.Fatalf("创建已读状态失败: %v", err)
	}

	h := &api{deps: appdeps.Deps{DB: tx}}
	view, ok := h.buildPrivateChannelView(channel.ID, viewer.ID, &rows[0])
	if !ok {
		t.Fatal("构建私信摘要失败")
	}
	if view.LastReadMessageID != fmt.Sprint(baseID) {
		t.Errorf("last_read_message_id = %s，期待 %d", view.LastReadMessageID, baseID)
	}
	if view.UnreadCount != 3 {
		t.Errorf("unread_count = %d，期待 3", view.UnreadCount)
	}
	if view.MentionCount != 2 {
		t.Errorf("mention_count = %d，期待 2", view.MentionCount)
	}

	if err := tx.Model(&model.ReadState{}).
		Where("user_id = ? AND channel_id = ?", viewer.ID, channel.ID).
		Updates(map[string]any{"last_read_message_id": baseID + 4, "mention_count": 0}).Error; err != nil {
		t.Fatalf("推进已读状态失败: %v", err)
	}
	view, ok = h.buildPrivateChannelView(channel.ID, viewer.ID, &rows[0])
	if !ok || view.UnreadCount != 0 || view.MentionCount != 0 {
		t.Errorf("已读后私信摘要 = %+v, ok=%v，期待未读和提及均为 0", view, ok)
	}
}
