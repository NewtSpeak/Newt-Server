package message

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// card.buttons 解析与裁剪的纯逻辑单测（设计文档 2026-07-26），无数据库依赖。

func TestValidateCardWithoutButtons(t *testing.T) {
	// 无 buttons 键：行为与历史版本一致，原样透传。
	card, buttons, err := validateCard([]byte(`{"title":"部署完成","fields":[{"name":"a","value":"b"}]}`))
	if err != nil {
		t.Fatalf("无 buttons 的 card 不应报错: %v", err)
	}
	if buttons != nil {
		t.Fatalf("无 buttons 键应返回 nil buttons，得到 %v", buttons)
	}
	if !strings.Contains(card, "部署完成") {
		t.Fatalf("card 应原样保留: %s", card)
	}
}

func TestValidateCardLegacyURLButtons(t *testing.T) {
	// 旧格式 {label, url}：天然满足新规则。
	_, buttons, err := validateCard([]byte(`{"buttons":[{"label":"查看日志","url":"https://ci.example.com/log/42"}]}`))
	if err != nil {
		t.Fatalf("旧格式按钮应兼容: %v", err)
	}
	if len(buttons) != 1 || buttons[0].URL == "" || buttons[0].CustomID != "" {
		t.Fatalf("旧格式解析结果异常: %+v", buttons)
	}
}

func TestValidateCardButtonMatrix(t *testing.T) {
	cases := []struct {
		name    string
		button  string
		wantErr bool
	}{
		{"合法交互按钮", `{"label":"批准","custom_id":"deploy:approve:42","style":"success","size":"md"}`, false},
		{"label 缺失", `{"custom_id":"a"}`, true},
		{"label 超长", `{"label":"` + strings.Repeat("很", 41) + `","custom_id":"a"}`, true},
		{"label 恰 40 字", `{"label":"` + strings.Repeat("很", 40) + `","custom_id":"a"}`, false},
		{"url 与 custom_id 双有", `{"label":"x","url":"https://a.com","custom_id":"a"}`, true},
		{"url 与 custom_id 双无", `{"label":"x"}`, true},
		{"危险 URL", `{"label":"x","url":"javascript:alert(1)"}`, true},
		{"custom_id 非法字符", `{"label":"x","custom_id":"a b"}`, true},
		{"custom_id 合法字符集", `{"label":"x","custom_id":"deploy:ok_1-2.3"}`, false},
		{"style 非法", `{"label":"x","custom_id":"a","style":"link"}`, true},
		{"size 非法", `{"label":"x","custom_id":"a","size":"xl"}`, true},
		{"row 越界", `{"label":"x","custom_id":"a","row":5}`, true},
		{"row 合法", `{"label":"x","custom_id":"a","row":4}`, false},
		{"visible_to users 超限", `{"label":"x","custom_id":"a","visible_to":{"users":[` + repeatUUIDs(21) + `]}}`, true},
		{"visible_to roles 超限", `{"label":"x","custom_id":"a","visible_to":{"roles":[` + repeatUUIDs(11) + `]}}`, true},
		{"visible_to 合法", `{"label":"x","custom_id":"a","visible_to":{"roles":["` + uuid.NewString() + `"]}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := validateCard([]byte(`{"buttons":[` + tc.button + `]}`))
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func repeatUUIDs(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = `"` + uuid.NewString() + `"`
	}
	return strings.Join(items, ",")
}

func TestValidateCardDuplicateCustomID(t *testing.T) {
	_, _, err := validateCard([]byte(`{"buttons":[
		{"label":"a","custom_id":"dup"},
		{"label":"b","custom_id":"dup"}]}`))
	if err == nil {
		t.Fatal("重复 custom_id 应报错")
	}
}

func TestValidateCardButtonCountLimit(t *testing.T) {
	items := make([]string, maxCardButtons+1)
	for i := range items {
		items[i] = `{"label":"x","custom_id":"b` + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + strconv26(i) + `"}`
	}
	_, _, err := validateCard([]byte(`{"buttons":[` + strings.Join(items, ",") + `]}`))
	if err == nil {
		t.Fatalf("超过 %d 个按钮应报错", maxCardButtons)
	}
}

func strconv26(i int) string { return string(rune('a'+i/26)) + string(rune('a'+i%26)) }

func TestTrimCardButtonsKeepsOtherKeys(t *testing.T) {
	roleID := uuid.New()
	userA := uuid.New()
	card := `{"title":"审批","color":"#22c55e","buttons":[
		{"label":"公开","custom_id":"pub"},
		{"label":"管理专属","custom_id":"admin","visible_to":{"roles":["` + roleID.String() + `"]}}]}`
	buttons, err := parseCardButtons(card)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 无角色的 viewer：只见公开按钮，且 visible_to 被剥除、其余键原样保留。
	bitmap := buttonVisibilityBitmap(buttons, userA, nil)
	trimmed := trimCardButtons(card, buttons, bitmap)
	var parsed struct {
		Title   string          `json:"title"`
		Color   string          `json:"color"`
		Buttons []cardButton    `json:"buttons"`
		Raw     json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		t.Fatalf("裁剪结果非法 JSON: %v", err)
	}
	if parsed.Title != "审批" || parsed.Color != "#22c55e" {
		t.Fatalf("非 buttons 键应原样保留: %s", trimmed)
	}
	if len(parsed.Buttons) != 1 || parsed.Buttons[0].CustomID != "pub" {
		t.Fatalf("应只保留公开按钮: %s", trimmed)
	}
	if strings.Contains(trimmed, "visible_to") {
		t.Fatalf("visible_to 应从下发 payload 剥除: %s", trimmed)
	}

	// 持有角色的 viewer：两个按钮都可见。
	bitmap = buttonVisibilityBitmap(buttons, userA, map[uuid.UUID]bool{roleID: true})
	trimmed = trimCardButtons(card, buttons, bitmap)
	if !strings.Contains(trimmed, `"admin"`) {
		t.Fatalf("持角色 viewer 应可见管理按钮: %s", trimmed)
	}
	if strings.Contains(trimmed, "visible_to") {
		t.Fatalf("全量位图也应剥除 visible_to: %s", trimmed)
	}
}

func TestTrimCardButtonsAllHidden(t *testing.T) {
	card := `{"title":"t","buttons":[{"label":"x","custom_id":"a","visible_to":{"users":["` + uuid.NewString() + `"]}}]}`
	buttons, err := parseCardButtons(card)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	trimmed := trimCardButtons(card, buttons, 0)
	if strings.Contains(trimmed, "buttons") {
		t.Fatalf("全部按钮不可见时应删除 buttons 键: %s", trimmed)
	}
}

func TestCardNeedsTrim(t *testing.T) {
	plain := `{"buttons":[{"label":"x","custom_id":"a"}]}`
	restricted := `{"buttons":[{"label":"x","custom_id":"a","visible_to":{"users":[]}}]}`
	if cardNeedsTrim(&plain) {
		t.Fatal("无 visible_to 不应触发裁剪")
	}
	if !cardNeedsTrim(&restricted) {
		t.Fatal("含 visible_to 应触发裁剪")
	}
	if cardNeedsTrim(nil) {
		t.Fatal("nil card 不应触发裁剪")
	}
}

func TestButtonVisibleToUsers(t *testing.T) {
	target := uuid.New()
	other := uuid.New()
	button := cardButton{Label: "x", CustomID: "a", VisibleTo: &buttonVisibility{Users: []uuid.UUID{target}}}
	if !buttonVisibleTo(button, target, nil) {
		t.Fatal("名单内用户应可见")
	}
	if buttonVisibleTo(button, other, nil) {
		t.Fatal("名单外用户不应可见")
	}
}
