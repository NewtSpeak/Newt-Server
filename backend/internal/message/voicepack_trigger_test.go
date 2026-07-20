package message

// PlayVoicePack 触发链路数据库集成测试：场景判定（FIRST_JOIN / CHANNEL_JOIN）、
// 频道开关、服务端频控、选包裁决与 RARE 失去身份组回退（docs 12）。
//
// 需要真实 PostgreSQL，默认跳过：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' \
//	  go test ./internal/message/

import (
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openTriggerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试（说明见文件头注释）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Member{}, &model.Role{}, &model.MemberRole{},
		&model.GuildVoicePackConfig{}, &model.ChannelVoicePackConfig{},
		&model.VoicePack{}, &model.VoicePackSelection{},
	); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	return db
}

// triggerFixture 一次触发测试所需的最小数据：guild + 成员 + 服级配置。
type triggerFixture struct {
	db        *gorm.DB
	bus       *eventbus.Bus
	events    chan eventbus.Event
	guildID   uuid.UUID
	channelID uuid.UUID
	userID    uuid.UUID
	memberID  uuid.UUID
}

func newTriggerFixture(t *testing.T, db *gorm.DB, trigger model.VoicePackTrigger, defaultAudio string) *triggerFixture {
	t.Helper()
	f := &triggerFixture{
		db: db, bus: eventbus.New(), events: make(chan eventbus.Event, 16),
		guildID: uuid.New(), channelID: uuid.New(), userID: uuid.New(), memberID: uuid.New(),
	}
	f.bus.Subscribe(func(event eventbus.Event) {
		if event.Type == eventbus.EventVoicePackPlay {
			f.events <- event
		}
	})
	if err := db.Create(&model.Member{ID: f.memberID, GuildID: f.guildID, UserID: f.userID}).Error; err != nil {
		t.Fatalf("插入成员失败: %v", err)
	}
	if err := db.Create(&model.GuildVoicePackConfig{
		GuildID: f.guildID, Enabled: true, AudioURL: defaultAudio,
		Scope: model.VoicePackSameChannel, Trigger: trigger,
	}).Error; err != nil {
		t.Fatalf("插入服级配置失败: %v", err)
	}
	return f
}

// play 调用 PlayVoicePack 并（若期望发布）取回事件载荷。
func (f *triggerFixture) play(t *testing.T, userID uuid.UUID, firstEver bool) (bool, VoicePackPlayPayload) {
	t.Helper()
	published := PlayVoicePack(f.db, f.bus, f.guildID, f.channelID, userID, firstEver)
	if !published {
		return false, VoicePackPlayPayload{}
	}
	select {
	case event := <-f.events:
		payload, ok := event.Payload.(VoicePackPlayPayload)
		if !ok {
			t.Fatalf("事件载荷类型异常: %T", event.Payload)
		}
		return true, payload
	case <-time.After(2 * time.Second):
		t.Fatal("已返回发布成功但未收到 VOICE_PACK_PLAY 事件")
		return false, VoicePackPlayPayload{}
	}
}

// addMemberWithRole 添加另一个成员并（可选）授予角色。
func (f *triggerFixture) addMemberWithRole(t *testing.T, roleID *uuid.UUID) uuid.UUID {
	t.Helper()
	userID, memberID := uuid.New(), uuid.New()
	if err := f.db.Create(&model.Member{ID: memberID, GuildID: f.guildID, UserID: userID}).Error; err != nil {
		t.Fatalf("插入成员失败: %v", err)
	}
	if roleID != nil {
		if err := f.db.Create(&model.MemberRole{MemberID: memberID, RoleID: *roleID}).Error; err != nil {
			t.Fatalf("授予角色失败: %v", err)
		}
	}
	return userID
}

// TestPlayVoicePackFirstJoinScene FIRST_GUILD_JOIN：仅进服首次触发，payload 规范化字段齐全。
func TestPlayVoicePackFirstJoinScene(t *testing.T) {
	db := openTriggerTestDB(t)
	f := newTriggerFixture(t, db, model.VoicePackFirstJoin, "/public-assets/voicepacks/default.ogg")

	// 非首次进服：不触发。
	if published, _ := f.play(t, f.userID, false); published {
		t.Fatal("FIRST_GUILD_JOIN 场景下非首次进服不应触发")
	}
	// 首次进服：触发，payload 字段规范化。
	published, payload := f.play(t, f.userID, true)
	if !published {
		t.Fatal("首次进服应触发")
	}
	if payload.Scene != VoicePackSceneFirstJoin {
		t.Fatalf("scene=%s，期望 FIRST_JOIN", payload.Scene)
	}
	if payload.UserID != f.userID || payload.ChannelID != f.channelID || payload.GuildID != f.guildID {
		t.Fatalf("payload 主体字段异常: %+v", payload)
	}
	if payload.PackID != nil {
		t.Fatalf("未选包时 pack_id 应为空，got %v", payload.PackID)
	}
	if payload.AudioURL != "/public-assets/voicepacks/default.ogg" {
		t.Fatalf("audio_url=%s，期望服级默认", payload.AudioURL)
	}
	if payload.EventAt.IsZero() {
		t.Fatal("event_at 不应为零值")
	}
}

// TestPlayVoicePackChannelJoinSceneAndCooldown CHANNEL_JOIN：每次进入允许播放的频道
// 都触发，但受服务端 60s 频控约束；频道开关关闭后不触发。
func TestPlayVoicePackChannelJoinSceneAndCooldown(t *testing.T) {
	db := openTriggerTestDB(t)
	f := newTriggerFixture(t, db, model.VoicePackChannelJoin, "/public-assets/voicepacks/default.ogg")

	// 非首次进服也触发（CHANNEL_JOIN 语义），scene 正确。
	published, payload := f.play(t, f.userID, false)
	if !published || payload.Scene != VoicePackSceneChannelJoin {
		t.Fatalf("CHANNEL_JOIN 场景应触发且 scene=CHANNEL_JOIN，got published=%v scene=%s", published, payload.Scene)
	}
	// 频控：同用户同 guild 60s 内第二次进入不触发。
	if published, _ := f.play(t, f.userID, false); published {
		t.Fatal("60s 冷却窗口内重复进入不应触发（服务端频控）")
	}
	// 其他用户不受该用户冷却影响。
	otherUser := f.addMemberWithRole(t, nil)
	if published, _ := f.play(t, otherUser, false); !published {
		t.Fatal("不同用户不应被他人冷却牵连")
	}

	// 频道开关关闭：任何进入均不触发（docs 12 FR-15）。
	// 先建行再显式 Update：GORM 对带 default 标签的零值布尔在 Create 时会跳过。
	thirdUser := f.addMemberWithRole(t, nil)
	if err := db.Create(&model.ChannelVoicePackConfig{
		ChannelID: f.channelID, GuildID: f.guildID, Allowed: true,
	}).Error; err != nil {
		t.Fatalf("写频道开关失败: %v", err)
	}
	if err := db.Model(&model.ChannelVoicePackConfig{}).Where("channel_id = ?", f.channelID).
		Update("allowed", false).Error; err != nil {
		t.Fatalf("关闭频道开关失败: %v", err)
	}
	if published, _ := f.play(t, thirdUser, false); published {
		t.Fatal("频道开关关闭后不应触发")
	}
}

// TestPlayVoicePackSelectionAndRareFallback 选包优先 + RARE 失去身份组自动回退：
//  1. 选中 RARE 包且持有授权身份组 → 播放选中包（payload 带 pack_id）；
//  2. 移除身份组 → 授权失效，回退服级默认 audio_url（pack_id 为空）；
//  3. 服级默认也为空 → 不发事件。
func TestPlayVoicePackSelectionAndRareFallback(t *testing.T) {
	db := openTriggerTestDB(t)
	f := newTriggerFixture(t, db, model.VoicePackChannelJoin, "/public-assets/voicepacks/default.ogg")

	role := model.Role{ID: uuid.New(), GuildID: f.guildID, Name: "传说 " + uuid.NewString()}
	if err := db.Create(&role).Error; err != nil {
		t.Fatalf("建角色失败: %v", err)
	}
	rareUser := f.addMemberWithRole(t, &role.ID)
	pack := model.VoicePack{
		ID: uuid.New(), GuildID: f.guildID, Name: "闪亮登场",
		AudioURL: "/public-assets/voicepacks/rare.ogg", Kind: model.VoicePackRare,
		AllowedRoleIDs: model.UUIDList{role.ID}, Enabled: true, CreatedBy: f.userID,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("建语音包失败: %v", err)
	}
	if err := db.Create(&model.VoicePackSelection{UserID: rareUser, GuildID: f.guildID, PackID: pack.ID}).Error; err != nil {
		t.Fatalf("写选包失败: %v", err)
	}

	// 持有身份组：播放选中的 RARE 包。
	published, payload := f.play(t, rareUser, false)
	if !published || payload.PackID == nil || *payload.PackID != pack.ID {
		t.Fatalf("应播放选中的 RARE 包，got published=%v payload=%+v", published, payload)
	}
	if payload.AudioURL != pack.AudioURL {
		t.Fatalf("audio_url=%s，期望选中包音频", payload.AudioURL)
	}

	// 移除身份组：授权失效 → 回退服级默认（冷却已过才会再次触发，直接清冷却重试）。
	if err := db.Where("role_id = ?", role.ID).Delete(&model.MemberRole{}).Error; err != nil {
		t.Fatalf("移除角色失败: %v", err)
	}
	playCooldown = &voicePackCooldown{} // 重置服务端频控，隔离本步断言
	published, payload = f.play(t, rareUser, false)
	if !published {
		t.Fatal("失去身份组后应回退服级默认音频继续触发")
	}
	if payload.PackID != nil || payload.AudioURL != "/public-assets/voicepacks/default.ogg" {
		t.Fatalf("应回退服级默认（pack_id 空），got %+v", payload)
	}

	// 服级默认也为空：不发事件。
	if err := db.Model(&model.GuildVoicePackConfig{}).Where("guild_id = ?", f.guildID).
		Update("audio_url", "").Error; err != nil {
		t.Fatalf("清空默认音频失败: %v", err)
	}
	playCooldown = &voicePackCooldown{}
	if published, _ := f.play(t, rareUser, false); published {
		t.Fatal("选包失效且无服级默认时不应发事件")
	}
}
