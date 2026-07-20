package message

import "testing"

// TestBigramTokens 中文 bigram 切片纯函数：CJK 连续段两两重叠、字母数字按词、
// 标点分隔、孤立单字保留。
func TestBigramTokens(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"连续中文", "你好世界", "你好 好世 世界"},
		{"标点切段", "你好，美丽的世界", "你好 美丽 丽的 的世 世界"},
		{"中英混排", "hello世界ok", "hello 世界 ok"},
		{"英文按词", "hello world 42", "hello world 42"},
		{"孤立单字", "好", "好"},
		{"单字加词", "好 code", "好 code"},
		{"空串", "", ""},
		{"纯标点", "。！？", ""},
		{"假名切片", "こんにちは", "こん んに にち ちは"},
	}
	for _, tc := range cases {
		if got := bigramTokens(tc.content); got != tc.want {
			t.Errorf("%s: bigramTokens(%q) = %q，期待 %q", tc.name, tc.content, got, tc.want)
		}
	}
}
