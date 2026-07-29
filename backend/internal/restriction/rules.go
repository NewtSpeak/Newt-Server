package restriction

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// 时长与内容边界（docs 12 AI.1 / AI.3）。
const (
	MinDuration     = 60 * time.Second
	MaxDuration     = 28 * 24 * time.Hour
	MaxReasonLength = 512
)

// RuleError 业务校验错误，Code 对应 docs 12 §11 错误码表。
type RuleError struct {
	Code    string
	Message string
}

func (e *RuleError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func ruleError(code, message string) *RuleError { return &RuleError{Code: code, Message: message} }

// ValidScope 判断作用域取值是否合法。
func ValidScope(scope Scope) bool {
	switch scope {
	case ScopeTextChannel, ScopeVoiceChannel, ScopeGuildAllText, ScopeGuildAllVoice:
		return true
	}
	return false
}

// ValidKind 判断记录类型取值是否合法。
func ValidKind(kind Kind) bool { return kind == KindSanction || kind == KindChannelBan }

// ValidateScopeDeny 校验 deny 维度与 scope 匹配（docs 12 AH.3 / §2.2）：
// 文字作用域仅允许 view_text/send_text，语音作用域仅允许 listen_voice/speak_voice，
// 且至少要有一个 deny 维度。
func ValidateScopeDeny(scope Scope, deny DenyFlags) *RuleError {
	if !deny.ViewText && !deny.SendText && !deny.ListenVoice && !deny.SpeakVoice {
		return ruleError("INVALID_SCOPE_DENY", "至少需要一个限制维度")
	}
	switch scope {
	case ScopeTextChannel, ScopeGuildAllText:
		if deny.ListenVoice || deny.SpeakVoice {
			return ruleError("INVALID_SCOPE_DENY", "文字作用域只允许 view_text / send_text 维度")
		}
	case ScopeVoiceChannel, ScopeGuildAllVoice:
		if deny.ViewText || deny.SendText {
			return ruleError("INVALID_SCOPE_DENY", "语音作用域只允许 listen_voice / speak_voice 维度")
		}
	default:
		return ruleError("INVALID_SCOPE_DENY", "未知的限制作用域")
	}
	return nil
}

// ApplyImplications 蕴含规则（docs 12 AH.4 / AH.5）：禁看⇒禁发、禁听⇒禁说，落库前自动补全。
func ApplyImplications(deny DenyFlags) DenyFlags {
	if deny.ViewText {
		deny.SendText = true
	}
	if deny.ListenVoice {
		deny.SpeakVoice = true
	}
	return deny
}

// ValidateDuration 校验时长（docs 12 AI.3）：临时限制 60 秒～28 天；
// 长期（expires_at=null）仅允许 CHANNEL_BAN 或系统管理员。
func ValidateDuration(kind Kind, expiresAt *time.Time, now time.Time, actorSystemAdmin bool) *RuleError {
	if expiresAt == nil {
		if kind == KindChannelBan || actorSystemAdmin {
			return nil
		}
		return ruleError("DURATION_OUT_OF_RANGE", "长期限制仅允许 CHANNEL_BAN 或系统管理员创建")
	}
	duration := expiresAt.Sub(now)
	if duration < MinDuration || duration > MaxDuration {
		return ruleError("DURATION_OUT_OF_RANGE", "限制时长必须在 60 秒到 28 天之间")
	}
	return nil
}

// Actor 施加者的层级信息。
type Actor struct {
	UserID      uuid.UUID
	SystemAdmin bool
	Owner       bool
	HighestRole int
}

// Target 目标成员的层级信息。
type Target struct {
	UserID      uuid.UUID
	IsOwner     bool
	HighestRole int
}

// CheckTarget 保护对象判定（docs 12 AK / §11）：
// 禁止自限；所有者仅系统管理员可限；普通管理者需层级严格高于目标。
func CheckTarget(actor Actor, target Target) *RuleError {
	if actor.UserID == target.UserID {
		return ruleError("CANNOT_RESTRICT_SELF", "不能对自己创建限制")
	}
	if actor.SystemAdmin {
		return nil
	}
	if target.IsOwner {
		return ruleError("CANNOT_RESTRICT_TARGET", "不能限制服务器所有者")
	}
	if actor.Owner {
		return nil
	}
	if target.HighestRole >= actor.HighestRole {
		return ruleError("CANNOT_RESTRICT_TARGET", "不能限制角色层级不低于自己的成员")
	}
	return nil
}

// MaskBits 按 deny 维度对 RBAC bits 做「只收紧」处理（docs 02 §5.5、12 §6.2）。
func MaskBits(bits rbac.Permission, deny DenyFlags) rbac.Permission {
	if deny.ViewText {
		bits &^= rbac.ViewChannel | rbac.ReadMessageHistory
	}
	if deny.SendText {
		bits &^= rbac.SendMessages | rbac.AddReactions
	}
	if deny.ListenVoice {
		bits &^= rbac.Connect
	}
	if deny.SpeakVoice {
		bits &^= rbac.Speak
	}
	return bits
}

// Union 合并两组 deny（docs 12 AI.4：多条重叠取并集，任一禁则禁）。
func Union(a, b DenyFlags) DenyFlags {
	return DenyFlags{
		ViewText:    a.ViewText || b.ViewText,
		SendText:    a.SendText || b.SendText,
		ListenVoice: a.ListenVoice || b.ListenVoice,
		SpeakVoice:  a.SpeakVoice || b.SpeakVoice,
	}
}

// denyOf 从模型行提取 deny 维度。
func denyOf(r model.Restriction) DenyFlags {
	return DenyFlags{
		ViewText:    r.DenyViewText,
		SendText:    r.DenySendText,
		ListenVoice: r.DenyListenVoice,
		SpeakVoice:  r.DenySpeakVoice,
	}
}

// appliesAt 判断限制作用域是否命中给定频道。
// channel 为 nil 表示计算服务器级权限：仅 GUILD_ALL_* 作用域命中。
func appliesAt(scope Scope, restrictionChannelID *uuid.UUID, channel *model.Channel) bool {
	if channel == nil {
		return scope == ScopeGuildAllText || scope == ScopeGuildAllVoice
	}
	switch channel.Type {
	case model.ChannelText:
		if scope == ScopeGuildAllText {
			return true
		}
		return scope == ScopeTextChannel && restrictionChannelID != nil && *restrictionChannelID == channel.ID
	case model.ChannelVoice:
		if scope == ScopeGuildAllVoice {
			return true
		}
		return scope == ScopeVoiceChannel && restrictionChannelID != nil && *restrictionChannelID == channel.ID
	}
	return false
}

// appliesForType Denies 查询用：按媒体类型 + 可选频道 ID 判断命中；
// channelID 为 nil 表示只看该类型的服务器级（GUILD_ALL_*）限制。
func appliesForType(scope Scope, restrictionChannelID *uuid.UUID, channelID *uuid.UUID, channelType model.ChannelType) bool {
	switch channelType {
	case model.ChannelText:
		if scope == ScopeGuildAllText {
			return true
		}
		return scope == ScopeTextChannel && channelID != nil && restrictionChannelID != nil && *restrictionChannelID == *channelID
	case model.ChannelVoice:
		if scope == ScopeGuildAllVoice {
			return true
		}
		return scope == ScopeVoiceChannel && channelID != nil && restrictionChannelID != nil && *restrictionChannelID == *channelID
	}
	return false
}

// MaskWithRestrictions 纯函数：在 RBAC bits 上应用一组限制记录（惰性过滤 active）。
// 供 Service.Mask 与单元测试复用。
func MaskWithRestrictions(bits rbac.Permission, restrictions []model.Restriction, channel *model.Channel, now time.Time) rbac.Permission {
	for _, r := range restrictions {
		if !r.ActiveAt(now) {
			continue
		}
		if !appliesAt(Scope(r.Scope), r.ChannelID, channel) {
			continue
		}
		bits = MaskBits(bits, denyOf(r))
	}
	return bits
}

// DenyUnionFor 纯函数：聚合命中给定位置的全部生效限制的 deny 并集。
func DenyUnionFor(restrictions []model.Restriction, channelID *uuid.UUID, channelType model.ChannelType, now time.Time) DenyFlags {
	var result DenyFlags
	for _, r := range restrictions {
		if !r.ActiveAt(now) {
			continue
		}
		if !appliesForType(Scope(r.Scope), r.ChannelID, channelID, channelType) {
			continue
		}
		result = Union(result, denyOf(r))
	}
	return result
}
