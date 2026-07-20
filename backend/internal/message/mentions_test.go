package message

import (
	"testing"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// TestParseMentionTokens wire format 纯解析：<@user_id> / <@&role_id> / @everyone / @here。
func TestParseMentionTokens(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	role := uuid.New()

	cases := []struct {
		name         string
		content      string
		wantUsers    []uuid.UUID
		wantRoles    []uuid.UUID
		wantEveryone bool
	}{
		{
			name:      "单个用户提及",
			content:   "你好 <@" + userA.String() + ">",
			wantUsers: []uuid.UUID{userA},
		},
		{
			name:      "多个用户提及去重且保序",
			content:   "<@" + userA.String() + "> <@" + userB.String() + "> 再次 <@" + userA.String() + ">",
			wantUsers: []uuid.UUID{userA, userB},
		},
		{
			name:      "角色提及",
			content:   "通知 <@&" + role.String() + "> 集合",
			wantRoles: []uuid.UUID{role},
		},
		{
			name:         "everyone 字面量",
			content:      "@everyone 大家好",
			wantEveryone: true,
		},
		{
			name:         "here 字面量",
			content:      "在线的看过来 @here",
			wantEveryone: true,
		},
		{
			name:         "混合提及",
			content:      "<@" + userA.String() + "> 和 <@&" + role.String() + "> @everyone",
			wantUsers:    []uuid.UUID{userA},
			wantRoles:    []uuid.UUID{role},
			wantEveryone: true,
		},
		{
			name:    "非法 UUID 忽略",
			content: "<@not-a-uuid> <@&12345>",
		},
		{
			name:    "普通 @ 文本不误判",
			content: "邮件发到 admin@example.com，@张三 看一下",
		},
		{
			name:    "缺少闭合括号不匹配",
			content: "<@" + userA.String(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMentionTokens(tc.content)
			if !uuidSliceEqual(got.UserIDs, tc.wantUsers) {
				t.Errorf("UserIDs = %v，期待 %v", got.UserIDs, tc.wantUsers)
			}
			if !uuidSliceEqual(got.RoleIDs, tc.wantRoles) {
				t.Errorf("RoleIDs = %v，期待 %v", got.RoleIDs, tc.wantRoles)
			}
			if got.EveryoneLiteral != tc.wantEveryone {
				t.Errorf("EveryoneLiteral = %v，期待 %v", got.EveryoneLiteral, tc.wantEveryone)
			}
		})
	}
}

// TestEveryoneEffective @everyone 生效需 MENTION_EVERYONE 权限；无权限时字面量不生效。
func TestEveryoneEffective(t *testing.T) {
	literal := mentionTokens{EveryoneLiteral: true}
	noLiteral := mentionTokens{}

	if !everyoneEffective(literal, rbac.MentionEveryone|rbac.SendMessages) {
		t.Error("有 MENTION_EVERYONE 权限时 @everyone 应生效")
	}
	if everyoneEffective(literal, rbac.SendMessages|rbac.ViewChannel) {
		t.Error("无 MENTION_EVERYONE 权限时 @everyone 不应生效（正文保留但 mention_everyone=false）")
	}
	if everyoneEffective(noLiteral, rbac.AllDefined) {
		t.Error("正文无字面量时即使有权限也不应生效")
	}
}

// TestKeepOrder 校验结果按正文出现顺序保留、未通过校验的 ID 剔除。
func TestKeepOrder(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	got := keepOrder([]uuid.UUID{a, b, c}, []uuid.UUID{c, a})
	if len(got) != 2 || got[0] != a || got[1] != c {
		t.Fatalf("keepOrder = %v，期待 [%s %s]", got, a, c)
	}
}

func uuidSliceEqual(got []uuid.UUID, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestUUIDListJSON UUIDList 的 jsonb Value/Scan 与 JSON 输出：nil 恒为 []。
func TestUUIDListJSON(t *testing.T) {
	var nilList model.UUIDList
	raw, err := nilList.MarshalJSON()
	if err != nil || string(raw) != "[]" {
		t.Fatalf("nil UUIDList JSON = %s (err=%v)，期待 []", raw, err)
	}
	value, err := nilList.Value()
	if err != nil || string(value.([]byte)) != "[]" {
		t.Fatalf("nil UUIDList Value = %v (err=%v)，期待 []", value, err)
	}
	id := uuid.New()
	var scanned model.UUIDList
	if err := scanned.Scan([]byte(`["` + id.String() + `"]`)); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	if len(scanned) != 1 || scanned[0] != id {
		t.Fatalf("Scan 结果 = %v，期待 [%s]", scanned, id)
	}
	var fromNull model.UUIDList
	if err := fromNull.Scan(nil); err != nil || len(fromNull) != 0 {
		t.Fatalf("Scan(nil) = %v (err=%v)，期待空数组", fromNull, err)
	}
}
