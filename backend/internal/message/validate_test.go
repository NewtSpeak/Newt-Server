package message

import (
	"strings"
	"testing"
	"time"
)

// TestValidateContent 正文长度与「正文/附件皆空」校验（AP.3/AP.4/AT.3）。
func TestValidateContent(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		attachments int
		hasCard     bool
		wantErr     error
	}{
		{"普通正文", "你好，世界", 0, false, nil},
		{"恰好 4000 字符", strings.Repeat("中", 4000), 0, false, nil},
		{"超过 4000 字符", strings.Repeat("中", 4001), 0, false, errContentTooLong},
		{"4001 个 ASCII", strings.Repeat("a", 4001), 0, false, errContentTooLong},
		{"正文与附件皆空", "", 0, false, errContentEmpty},
		{"纯空白正文无附件", "  \n\t ", 0, false, errContentEmpty},
		{"仅附件", "", 1, false, nil},
		{"恰好 10 个附件", "", 10, false, nil},
		{"11 个附件", "x", 11, false, errTooManyFiles},
		{"纯卡片消息", "", 0, true, nil},
	}
	for _, tc := range cases {
		if got := validateContent(tc.content, tc.attachments, tc.hasCard); got != tc.wantErr {
			t.Errorf("%s: 期望 %v，实际 %v", tc.name, tc.wantErr, got)
		}
	}
}

// TestValidateCard 卡片载荷校验（bot 专项）：JSON 对象、大小受限。
func TestValidateCard(t *testing.T) {
	if card, err := validateCard(nil); err != nil || card != "" {
		t.Errorf("空卡片应放行并返回空串，got (%q, %v)", card, err)
	}
	if card, err := validateCard([]byte(`{"title":"hi"}`)); err != nil || card != `{"title":"hi"}` {
		t.Errorf("合法卡片被拒绝：(%q, %v)", card, err)
	}
	invalid := [][]byte{
		[]byte(`"just a string"`),
		[]byte(`[1,2,3]`),
		[]byte(`{broken`),
		[]byte(`{"big":"` + strings.Repeat("x", maxCardBytes) + `"}`),
	}
	for _, raw := range invalid {
		if _, err := validateCard(raw); err == nil {
			t.Errorf("非法卡片 %.40q 未被拒绝", raw)
		}
	}
}

// TestNonceDuplicate 窗口内视为重复，窗口外放行（AR.6）。
func TestNonceDuplicate(t *testing.T) {
	now := time.Now()
	if !nonceDuplicate(now.Add(-time.Minute), now) {
		t.Error("1 分钟前的同 nonce 应判定为重复")
	}
	if !nonceDuplicate(now, now) {
		t.Error("同时刻应判定为重复")
	}
	if nonceDuplicate(now.Add(-nonceWindow), now) {
		t.Error("恰好达到窗口边界应放行")
	}
	if nonceDuplicate(now.Add(-time.Hour), now) {
		t.Error("窗口外应放行")
	}
}

// TestPreviewKind 预览白名单：图片/音频/视频/PDF 之外一律不可预览（AT.5）。
func TestPreviewKind(t *testing.T) {
	cases := map[string]string{
		"image/png":                    "image",
		"IMAGE/JPEG":                   "image",
		"image/svg+xml; charset=utf-8": "image",
		"audio/mpeg":                   "audio",
		"video/mp4":                    "video",
		"application/pdf":              "pdf",
		"Application/PDF":              "pdf",
		"application/zip":              "",
		"text/html":                    "",
		"application/octet-stream":     "",
		"":                             "",
	}
	for mime, want := range cases {
		if got := previewKind(mime); got != want {
			t.Errorf("previewKind(%q) = %q，期望 %q", mime, got, want)
		}
	}
}

// TestValidateEmoji 反应 emoji 校验（AV）。
func TestValidateEmoji(t *testing.T) {
	valid := []string{"👍", "🦉", "👨‍👩‍👧‍👦", "❤️", "🇨🇳"}
	for _, emoji := range valid {
		if err := validateEmoji(emoji); err != nil {
			t.Errorf("合法 emoji %q 被拒绝：%v", emoji, err)
		}
	}
	invalid := []string{
		"",
		" ",
		"a b",
		"\n",
		"\x01",
		string([]byte{0xff, 0xfe}),           // 非法 UTF-8
		strings.Repeat("👍", 20),              // 超过 rune 上限
		strings.Repeat("x", maxEmojiBytes+1), // 超过字节上限
	}
	for _, emoji := range invalid {
		if err := validateEmoji(emoji); err == nil {
			t.Errorf("非法 emoji %q 未被拒绝", emoji)
		}
	}
}

// TestEscapeLike LIKE 元字符转义。
func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`100%_\abc`); got != `100\%\_\\abc` {
		t.Errorf("escapeLike 结果不符：%q", got)
	}
}
