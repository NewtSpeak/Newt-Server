package message_test

// 限定可见消息集成测试（需 TEST_DATABASE_URL；未设置则 Skip）。
//
//	TEST_DATABASE_URL='postgres://...' go test ./internal/message/ -run Visibility -count=1

import (
	"fmt"
	"math/rand"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// TestVisibilityRestrictedList 作者与角色成员可见，路人 list/get 不可见。
func TestVisibilityRestrictedList(t *testing.T) {
	router, db, _ := newTextRouter(t)

	// 作者建服 + 频道
	authorTok, _, channelID := setupTextFixture(t, router, db)
	// 从频道反查 guild
	var channel model.Channel
	if err := db.First(&channel, "id = ?", channelID).Error; err != nil {
		t.Fatalf("读频道失败: %v", err)
	}
	guildID := channel.GuildID

	// 创建自定义角色
	role := model.Role{
		ID: uuid.New(), GuildID: guildID, Name: "vis-" + fmt.Sprintf("%08x", rand.Uint32()),
		Permissions: 0, Position: 1,
	}
	if err := db.Create(&role).Error; err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 角色成员 + 路人：注册并加入公会
	roleTok, roleUserID := signupVisibilityUser(t, router)
	strangerTok, strangerID := signupVisibilityUser(t, router)
	addMemberWithRole(t, db, guildID, roleUserID, role.ID)
	addMember(t, db, guildID, strangerID)

	// 发送限定可见消息
	base := "/gapi/v1/channels/" + channelID.String()
	rec, msg := doJSONReq(t, router, http.MethodPost, base+"/messages", authorTok, map[string]any{
		"content":          "secret-visibility",
		"visible_role_ids": []string{role.ID.String()},
		"nonce":            uuid.New().String(),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发限定消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	msgID := msg["id"].(string)
	rolesRaw, _ := msg["visible_role_ids"].([]any)
	if len(rolesRaw) != 1 {
		t.Fatalf("响应 visible_role_ids 异常: %v", msg["visible_role_ids"])
	}

	// 作者 list 可见
	if !listHasMessage(t, router, authorTok, channelID, msgID) {
		t.Fatal("作者应能 list 到限定消息")
	}
	// 角色成员可见
	if !listHasMessage(t, router, roleTok, channelID, msgID) {
		t.Fatal("角色成员应能 list 到限定消息")
	}
	// 路人 list 不可见
	if listHasMessage(t, router, strangerTok, channelID, msgID) {
		t.Fatal("路人不应 list 到限定消息")
	}
	// 路人 get 404
	rec, _ = doJSONReq(t, router, http.MethodGet, base+"/messages/"+msgID, strangerTok, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("路人 get 期望 404，得到 %d", rec.Code)
	}
	// 角色成员 get 200
	rec, _ = doJSONReq(t, router, http.MethodGet, base+"/messages/"+msgID, roleTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("角色成员 get 期望 200，得到 %d", rec.Code)
	}

	// 作者将可见范围改为公开
	rec, _ = doJSONReq(t, router, http.MethodPatch, base+"/messages/"+msgID, authorTok, map[string]any{
		"visible_role_ids": []string{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("改可见范围返回 %d: %s", rec.Code, rec.Body.String())
	}
	// 路人此时应可见
	if !listHasMessage(t, router, strangerTok, channelID, msgID) {
		t.Fatal("改为公开后路人应能 list 到")
	}
}

// TestVisibilityChannelDenyRestricted 频道关闭限定后发送非空 visible_role_ids 应 400。
func TestVisibilityChannelDenyRestricted(t *testing.T) {
	router, db, _ := newTextRouter(t)
	token, _, channelID := setupTextFixture(t, router, db)
	if err := db.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("allow_restricted_visibility", false).Error; err != nil {
		t.Fatalf("更新频道失败: %v", err)
	}
	roleID := uuid.New()
	rec, _ := doJSONReq(t, router, http.MethodPost, "/gapi/v1/channels/"+channelID.String()+"/messages", token, map[string]any{
		"content":          "should-fail",
		"visible_role_ids": []string{roleID.String()},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestVisibilityForceDefault 强制默认空 = 公开，忽略客户端传入的角色。
func TestVisibilityForceDefaultPublic(t *testing.T) {
	router, db, _ := newTextRouter(t)
	token, _, channelID := setupTextFixture(t, router, db)
	if err := db.Model(&model.Channel{}).Where("id = ?", channelID).Updates(map[string]any{
		"force_default_visibility": true,
		"default_visible_role_ids": model.UUIDList{},
	}).Error; err != nil {
		t.Fatalf("更新频道失败: %v", err)
	}
	// 即使传入角色 ID（可能非法），强制空默认应落库公开
	// 传入非法角色会在 validate 时失败——强制路径跳过客户端角色。
	// 因此客户端传任何值都会被忽略，最终公开。
	fakeRole := uuid.New().String()
	rec, msg := doJSONReq(t, router, http.MethodPost, "/gapi/v1/channels/"+channelID.String()+"/messages", token, map[string]any{
		"content":          "forced-public",
		"visible_role_ids": []string{fakeRole},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("强制公开发送返回 %d: %s", rec.Code, rec.Body.String())
	}
	roles, _ := msg["visible_role_ids"].([]any)
	if len(roles) != 0 {
		t.Fatalf("强制空默认应公开，got %v", msg["visible_role_ids"])
	}
}

func signupVisibilityUser(t *testing.T, router *gin.Engine) (token string, userID uuid.UUID) {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	username := "visu_" + suffix
	rec, body := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": username, "email": username + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	token = body["access_token"].(string)
	user := body["user"].(map[string]any)
	id, err := uuid.Parse(user["id"].(string))
	if err != nil {
		t.Fatalf("解析 user id: %v", err)
	}
	return token, id
}

func addMember(t *testing.T, db *gorm.DB, guildID, userID uuid.UUID) model.Member {
	t.Helper()
	var member model.Member
	err := db.Where("guild_id = ? AND user_id = ?", guildID, userID).First(&member).Error
	if err == nil {
		return member
	}
	member = model.Member{ID: uuid.New(), GuildID: guildID, UserID: userID}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("创建成员失败: %v", err)
	}
	return member
}

func addMemberWithRole(t *testing.T, db *gorm.DB, guildID, userID, roleID uuid.UUID) {
	t.Helper()
	member := addMember(t, db, guildID, userID)
	link := model.MemberRole{MemberID: member.ID, RoleID: roleID}
	var n int64
	db.Model(&model.MemberRole{}).Where("member_id = ? AND role_id = ?", member.ID, roleID).Count(&n)
	if n == 0 {
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("分配角色失败: %v", err)
		}
	}
}

func listHasMessage(t *testing.T, router *gin.Engine, token string, channelID uuid.UUID, msgID string) bool {
	t.Helper()
	rec, body := doJSONReq(t, router, http.MethodGet, "/gapi/v1/channels/"+channelID.String()+"/messages", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list 返回 %d: %s", rec.Code, rec.Body.String())
	}
	arr, _ := body["messages"].([]any)
	for _, item := range arr {
		m := item.(map[string]any)
		if m["id"] == msgID {
			return true
		}
	}
	return false
}
