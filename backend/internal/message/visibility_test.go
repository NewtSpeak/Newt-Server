package message

import (
	"testing"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

func TestCanViewMessagePublic(t *testing.T) {
	msg := model.Message{AuthorID: uuid.New(), VisibleRoleIDs: model.UUIDList{}}
	viewer := uuid.New()
	if !canViewMessage(viewer, 0, nil, false, msg) {
		t.Fatal("公开消息应人人可见")
	}
}

func TestCanViewMessageAuthor(t *testing.T) {
	author := uuid.New()
	role := uuid.New()
	msg := model.Message{AuthorID: author, VisibleRoleIDs: model.UUIDList{role}}
	if !canViewMessage(author, 0, nil, false, msg) {
		t.Fatal("作者应可见自己的限定消息")
	}
}

func TestCanViewMessageRoleHolder(t *testing.T) {
	author := uuid.New()
	roleA := uuid.New()
	roleB := uuid.New()
	msg := model.Message{AuthorID: author, VisibleRoleIDs: model.UUIDList{roleA}}
	viewer := uuid.New()
	if !canViewMessage(viewer, 0, []uuid.UUID{roleA, roleB}, false, msg) {
		t.Fatal("持有指定角色应可见")
	}
	if canViewMessage(viewer, 0, []uuid.UUID{roleB}, false, msg) {
		t.Fatal("未持有指定角色不应可见")
	}
}

func TestCanViewMessageManageMessages(t *testing.T) {
	author := uuid.New()
	role := uuid.New()
	msg := model.Message{AuthorID: author, VisibleRoleIDs: model.UUIDList{role}}
	viewer := uuid.New()
	if !canViewMessage(viewer, rbac.ManageMessages, nil, false, msg) {
		t.Fatal("MANAGE_MESSAGES 应可穿透查看")
	}
}

func TestCanViewMessageOwner(t *testing.T) {
	author := uuid.New()
	role := uuid.New()
	msg := model.Message{AuthorID: author, VisibleRoleIDs: model.UUIDList{role}}
	viewer := uuid.New()
	if !canViewMessage(viewer, 0, nil, true, msg) {
		t.Fatal("服主/系统管应可穿透查看")
	}
}

func TestFilterVisibleMessages(t *testing.T) {
	author := uuid.New()
	role := uuid.New()
	viewer := uuid.New()
	public := model.Message{ID: 1, AuthorID: author, VisibleRoleIDs: model.UUIDList{}}
	restricted := model.Message{ID: 2, AuthorID: author, VisibleRoleIDs: model.UUIDList{role}}
	own := model.Message{ID: 3, AuthorID: viewer, VisibleRoleIDs: model.UUIDList{role}}

	got := filterVisibleMessages(viewer, 0, nil, false, []model.Message{public, restricted, own})
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("期望公开+本人限定，得到 %#v", got)
	}
}

func TestIsMessageRestricted(t *testing.T) {
	if isMessageRestricted(model.Message{}) {
		t.Fatal("空 visible_role_ids 应视为公开")
	}
	if !isMessageRestricted(model.Message{VisibleRoleIDs: model.UUIDList{uuid.New()}}) {
		t.Fatal("非空应视为限定")
	}
}

func TestUUIDListEqual(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	if !uuidListEqual(model.UUIDList{a, b}, model.UUIDList{b, a}) {
		t.Fatal("应忽略顺序")
	}
	if uuidListEqual(model.UUIDList{a}, model.UUIDList{b}) {
		t.Fatal("不同集合不应相等")
	}
}

func TestResolveEffectiveVisibleRolesPolicy(t *testing.T) {
	// 不依赖 DB 的策略短路：私信 / 非 TEXT / 关闭限定 / 强制默认空
	s := &service{}
	dm := model.Channel{Type: model.ChannelDM}
	list, err := s.resolveEffectiveVisibleRoles(dm, []uuid.UUID{uuid.New()}, true)
	if err != errVisibleRolesTextOnly {
		t.Fatalf("私信应拒绝非空限定: %v", err)
	}
	_ = list

	text := model.Channel{
		Type:                      model.ChannelText,
		AllowRestrictedVisibility: false,
		ForceDefaultVisibility:    false,
	}
	_, err = s.resolveEffectiveVisibleRoles(text, []uuid.UUID{uuid.New()}, true)
	if err != errVisibleRolesDisabled {
		t.Fatalf("关闭限定应拒绝: %v", err)
	}

	// 强制默认且默认空 → 公开（不查库）
	forced := model.Channel{
		Type:                   model.ChannelText,
		ForceDefaultVisibility: true,
		DefaultVisibleRoleIDs:  model.UUIDList{},
	}
	got, err := s.resolveEffectiveVisibleRoles(forced, []uuid.UUID{uuid.New()}, true)
	if err != nil {
		t.Fatalf("强制空默认应成功: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("强制空默认应为公开, got %v", got)
	}
}
