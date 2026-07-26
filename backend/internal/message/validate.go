package message

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// 消息与附件的纯逻辑校验（docs 13 AP.3/AP.4、AT.3/AT.5、AV）。
// 说明：AP.2 的「有限 Markdown」由客户端按白名单标签渲染并负责 XSS 防护，
// 服务端只存储原始纯文本，不做 Markdown 解析或 HTML 清洗。

const (
	// maxContentRunes 正文最大长度（AP.3，按 Unicode 字符数而非字节）。
	maxContentRunes = 4000
	// maxAttachmentsPerMessage 每消息附件上限（AT.3）。
	maxAttachmentsPerMessage = 10
	// defaultUploadLimitBytes 平台默认单文件上限 25MB（AT.4）。
	defaultUploadLimitBytes = int64(25 << 20)
	// nonceWindow nonce 幂等判定窗口（AR.6）：窗口内同 channel+author+nonce 视为重复提交。
	nonceWindow = 10 * time.Minute
	// maxEmojiBytes / maxEmojiRunes 反应 emoji 的长度上限（组合 emoji 可能多码点）。
	maxEmojiBytes = 64
	maxEmojiRunes = 16
)

var (
	errContentTooLong = errors.New("正文长度超过 4000 字符")
	errContentEmpty   = errors.New("正文与附件不能同时为空")
	errTooManyFiles   = errors.New("每条消息最多携带 10 个附件")
	errBadEmoji       = errors.New("emoji 非法：需为合法 Unicode 字符串且长度受限")
	errBadCard        = errors.New("card 非法：需为 JSON 对象且不超过 16KB")
)

// maxCardBytes 卡片消息载荷上限（bot 专项）：16KB 容纳嵌入/字段/交互按钮
//（含 visible_to 声明）结构；病态膨胀由按钮数量与名单上限约束（见 card.go）。
const maxCardBytes = 16 << 10

// validateContent 校验消息正文与附件数量组合（AP.3/AP.4/AT.3）。
// hasCard=true 时允许正文与附件同时为空（纯卡片消息，bot 专项）。
func validateContent(content string, attachmentCount int, hasCard bool) error {
	if utf8.RuneCountInString(content) > maxContentRunes {
		return errContentTooLong
	}
	if strings.TrimSpace(content) == "" && attachmentCount == 0 && !hasCard {
		return errContentEmpty
	}
	if attachmentCount > maxAttachmentsPerMessage {
		return errTooManyFiles
	}
	return nil
}

// validateCard 校验卡片载荷（bot 专项）：必须是 JSON 对象且大小受限；
// buttons 键（若有）按 card.go 规则强校验（设计文档 2026-07-26），其余键不解释。
// 返回归一化（原样）字符串与解析后的按钮列表（无 buttons 键时为 nil）。
func validateCard(raw []byte) (string, []cardButton, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	if len(raw) > maxCardBytes {
		return "", nil, errBadCard
	}
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") || !json.Valid(raw) {
		return "", nil, errBadCard
	}
	buttons, err := parseCardButtons(trimmed)
	if err != nil {
		return "", nil, err
	}
	return trimmed, buttons, nil
}

// nonceDuplicate nonce 幂等判定：已有消息落在窗口内视为重复（AR.6）。
func nonceDuplicate(existingCreatedAt, now time.Time) bool {
	return now.Sub(existingCreatedAt) < nonceWindow
}

// previewKind 附件预览白名单（AT.5 / 5B.3）：仅图片、音频、视频、PDF 标注 preview；
// 其余 MIME 照存但只提供下载。返回空串表示不可预览。
func previewKind(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	// 去掉 "; charset=..." 等参数部分。
	if idx := strings.IndexByte(mime, ';'); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case mime == "application/pdf":
		return "pdf"
	default:
		return ""
	}
}

// validateEmoji 反应 emoji 校验（AV）：合法 UTF-8、非空、长度受限、不含空白与控制字符。
// docs 17：自定义表情反应键形如 item:{snowflake}，单独放行。
func validateEmoji(emoji string) error {
	if emoji == "" || len(emoji) > maxEmojiBytes || !utf8.ValidString(emoji) {
		return errBadEmoji
	}
	// 自定义贴图/小表情反应：item:1234567890
	if strings.HasPrefix(emoji, "item:") {
		raw := strings.TrimPrefix(emoji, "item:")
		if raw == "" {
			return errBadEmoji
		}
		for _, r := range raw {
			if r < '0' || r > '9' {
				return errBadEmoji
			}
		}
		return nil
	}
	if utf8.RuneCountInString(emoji) > maxEmojiRunes {
		return errBadEmoji
	}
	for _, r := range emoji {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return errBadEmoji
		}
	}
	return nil
}
