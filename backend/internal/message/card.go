package message

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// 卡片交互按钮（设计文档 2026-07-26）：服务端从本版本起解析并校验 card.buttons，
// 其余 card 键（title/description/fields/footer/…）继续原样透传不解释。
//   - url 与 custom_id 互斥且必居其一：url 为外链按钮，custom_id 为交互回调按钮；
//   - visible_to 声明按钮可见名单（users/roles 并集），下发前按接收者裁剪并剥除该字段
//     （作者视图保留全量）；
//   - row/size/style 为纯渲染提示，服务端只做类型与枚举校验。

const (
	maxCardButtons        = 25 // 5 行 × 5 个（对齐 Discord components 上限）
	maxButtonLabelRunes   = 40
	maxButtonURLBytes     = 1024
	maxButtonCustomIDLen  = 64
	maxButtonVisibleUsers = 20
	maxButtonVisibleRoles = 10
	maxButtonRow          = 4
)

var buttonStyles = map[string]bool{"primary": true, "secondary": true, "success": true, "danger": true}
var buttonSizes = map[string]bool{"xs": true, "sm": true, "md": true, "lg": true}

// buttonVisibility 按钮可见名单：users/roles 取并集；双空视同全员可见。
type buttonVisibility struct {
	Users []uuid.UUID `json:"users,omitempty"`
	Roles []uuid.UUID `json:"roles,omitempty"`
}

func (v *buttonVisibility) empty() bool {
	return v == nil || (len(v.Users) == 0 && len(v.Roles) == 0)
}

// cardButton card.buttons 数组元素（服务端认识的键；未知键在归一化时丢弃）。
type cardButton struct {
	Label     string            `json:"label"`
	URL       string            `json:"url,omitempty"`
	CustomID  string            `json:"custom_id,omitempty"`
	Style     string            `json:"style,omitempty"`
	Size      string            `json:"size,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
	Row       *int              `json:"row,omitempty"`
	VisibleTo *buttonVisibility `json:"visible_to,omitempty"`
}

// hasRestrictedButtons 是否存在带 visible_to 的按钮（决定是否走分组裁剪分发路径）。
func hasRestrictedButtons(buttons []cardButton) bool {
	for _, button := range buttons {
		if !button.VisibleTo.empty() {
			return true
		}
	}
	return false
}

// parseCardButtons 从 card JSON 中解析 buttons 数组；card 无 buttons 键时返回 (nil, nil)，
// 行为与历史版本完全一致（零兼容成本）。校验失败返回携带具体原因的 error。
func parseCardButtons(card string) ([]cardButton, error) {
	if card == "" {
		return nil, nil
	}
	var envelope struct {
		Buttons []json.RawMessage `json:"buttons"`
	}
	if err := json.Unmarshal([]byte(card), &envelope); err != nil {
		return nil, errBadCard
	}
	if envelope.Buttons == nil {
		return nil, nil
	}
	if len(envelope.Buttons) > maxCardButtons {
		return nil, fmt.Errorf("buttons 数量超过上限 %d", maxCardButtons)
	}
	buttons := make([]cardButton, 0, len(envelope.Buttons))
	seenCustomIDs := make(map[string]struct{}, len(envelope.Buttons))
	for i, raw := range envelope.Buttons {
		var button cardButton
		if err := json.Unmarshal(raw, &button); err != nil {
			return nil, fmt.Errorf("buttons[%d] 非法：需为对象", i)
		}
		if err := validateCardButton(&button); err != nil {
			return nil, fmt.Errorf("buttons[%d] %s", i, err.Error())
		}
		if button.CustomID != "" {
			if _, dup := seenCustomIDs[button.CustomID]; dup {
				return nil, fmt.Errorf("buttons[%d] custom_id 重复：%s", i, button.CustomID)
			}
			seenCustomIDs[button.CustomID] = struct{}{}
		}
		buttons = append(buttons, button)
	}
	return buttons, nil
}

// validateCardButton 单按钮校验（label/url|custom_id 互斥/style/size/row/visible_to）。
func validateCardButton(button *cardButton) error {
	label := strings.TrimSpace(button.Label)
	if label == "" {
		return errors.New("label 不能为空")
	}
	if runeLen(label) > maxButtonLabelRunes {
		return fmt.Errorf("label 超过 %d 字符", maxButtonLabelRunes)
	}
	hasURL := strings.TrimSpace(button.URL) != ""
	hasCustomID := strings.TrimSpace(button.CustomID) != ""
	if hasURL == hasCustomID {
		return errors.New("url 与 custom_id 必须恰好提供其一")
	}
	if hasURL {
		if len(button.URL) > maxButtonURLBytes || !isSafeHTTPURL(button.URL) {
			return errors.New("url 非法：仅允许 http(s) 且不超过 1024 字节")
		}
	}
	if hasCustomID {
		if len(button.CustomID) > maxButtonCustomIDLen || !isValidCustomID(button.CustomID) {
			return errors.New("custom_id 非法：1-64 字符，仅允许字母数字与 _-:. ")
		}
	}
	if button.Style != "" && !buttonStyles[button.Style] {
		return errors.New("style 非法：仅允许 primary/secondary/success/danger")
	}
	if button.Size != "" && !buttonSizes[button.Size] {
		return errors.New("size 非法：仅允许 xs/sm/md/lg")
	}
	if button.Row != nil && (*button.Row < 0 || *button.Row > maxButtonRow) {
		return fmt.Errorf("row 非法：需为 0-%d 的整数", maxButtonRow)
	}
	if !button.VisibleTo.empty() {
		if len(button.VisibleTo.Users) > maxButtonVisibleUsers {
			return fmt.Errorf("visible_to.users 超过上限 %d", maxButtonVisibleUsers)
		}
		if len(button.VisibleTo.Roles) > maxButtonVisibleRoles {
			return fmt.Errorf("visible_to.roles 超过上限 %d", maxButtonVisibleRoles)
		}
	}
	return nil
}

func runeLen(value string) int {
	count := 0
	for range value {
		count++
	}
	return count
}

// isSafeHTTPURL 仅放行 http/https 外链（对齐客户端 isSafeHttpUrl，防 javascript: 等危险协议）。
func isSafeHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func isValidCustomID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == ':' || r == '.':
		default:
			return false
		}
	}
	return true
}

// buttonVisibleTo 按钮对某接收者是否可见：无 visible_to → 全员；
// users 直查、roles 与接收者角色集求交（作者恒见全量，由调用方短路）。
func buttonVisibleTo(button cardButton, viewerID uuid.UUID, viewerRoles map[uuid.UUID]bool) bool {
	if button.VisibleTo.empty() {
		return true
	}
	for _, id := range button.VisibleTo.Users {
		if id == viewerID {
			return true
		}
	}
	for _, id := range button.VisibleTo.Roles {
		if viewerRoles[id] {
			return true
		}
	}
	return false
}

// buttonVisibilityBitmap 计算接收者的按钮可见位图（buttons ≤ 25，uint32 足够）：
// 相同位图的接收者共享同一份裁剪后的 payload（分组分发用）。
func buttonVisibilityBitmap(buttons []cardButton, viewerID uuid.UUID, viewerRoles map[uuid.UUID]bool) uint32 {
	var bitmap uint32
	for i, button := range buttons {
		if buttonVisibleTo(button, viewerID, viewerRoles) {
			bitmap |= 1 << i
		}
	}
	return bitmap
}

// fullVisibilityBitmap 全量可见位图（作者视图）。
func fullVisibilityBitmap(count int) uint32 {
	return (1 << count) - 1
}

// trimCardButtons 按可见位图重写 card 的 buttons 键并剥除 visible_to（其余键原样保留）。
// 位图全 1 时也要走一遍（剥除 visible_to，避免向接收者泄露定向声明）；
// 解析失败时回退原文（发送期已校验过，理论不可达）。
func trimCardButtons(card string, buttons []cardButton, bitmap uint32) string {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(card), &envelope); err != nil {
		return card
	}
	kept := make([]cardButton, 0, len(buttons))
	for i, button := range buttons {
		if bitmap&(1<<i) == 0 {
			continue
		}
		button.VisibleTo = nil
		kept = append(kept, button)
	}
	if len(kept) == 0 {
		delete(envelope, "buttons")
	} else {
		raw, err := json.Marshal(kept)
		if err != nil {
			return card
		}
		envelope["buttons"] = raw
	}
	rewritten, err := json.Marshal(envelope)
	if err != nil {
		return card
	}
	return string(rewritten)
}

// cardNeedsTrim 廉价预判：card 是否可能含 visible_to 声明（绝大多数消息零开销跳过）。
func cardNeedsTrim(card *string) bool {
	return card != nil && strings.Contains(*card, `"visible_to"`)
}
