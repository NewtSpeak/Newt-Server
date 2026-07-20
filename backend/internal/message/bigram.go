package message

import (
	"strings"
	"unicode"
)

// 中文检索 bigram 化（docs 13 AU 中文局限的一期缓解方案）：
// PG FTS 的 'simple' 配置无法切分无空格的中文整句，此处在入库/查询两侧做同样的
// 预切分——连续 CJK 字符两两重叠切片（"你好世界" → 你好 好世 世界），字母/数字
// 按词保留（小写化交给 to_tsvector）。切片结果以空格连接后交给
// to_tsvector('simple', ...) / plainto_tsquery('simple', ...)，即得到可走 GIN
// 索引的中文「词组」匹配：查询 "世界" 命中 "你好世界"，多词查询（"你好 世界"）
// 为 AND 语义、无需相邻。孤立单个 CJK 字符保留为单字 token；查询中的单字仍主要
// 依赖 ILIKE 子串兜底（单字不构成 bigram，无法命中长句中的中间字）。

// cjkTables bigram 切片适用的书写系统：汉字为主，兼顾日文假名与谚文
//（同样无空格分词的场景）。
var cjkTables = []*unicode.RangeTable{unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul}

func isCJK(r rune) bool {
	for _, table := range cjkTables {
		if unicode.Is(table, r) {
			return true
		}
	}
	return false
}

// bigramTokens 将文本切成空格分隔的检索词元（CJK bigram + 非 CJK 词）。
func bigramTokens(content string) string {
	var tokens []string
	var word []rune // 当前字母/数字词
	var cjk []rune  // 当前 CJK 连续段
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			tokens = append(tokens, string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			tokens = append(tokens, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range content {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return strings.Join(tokens, " ")
}
