package message

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// 消息提及解析（docs 05 FR-19/FR-22、15 §7-2）。
// wire format：<@user_id> 用户提及、<@&role_id> 角色提及、@everyone / @here 字面量；
// user_id / role_id 均为 UUID 字符串。解析在发消息与编辑消息时由服务端执行，
// 结果落库并随 MESSAGE_CREATE/UPDATE payload 下发（客户端本地判断是否 @ 自己）。

// mentionUserPattern / mentionRolePattern 提取 wire format 中的 UUID；
// 非法 UUID（uuid.Parse 失败）静默忽略，正文原样保留。
var (
	mentionUserPattern = regexp.MustCompile(`<@([0-9a-fA-F-]{36})>`)
	mentionRolePattern = regexp.MustCompile(`<@&([0-9a-fA-F-]{36})>`)
)

// mentionTokens 正文解析出的原始提及（未经成员/角色校验）。
type mentionTokens struct {
	UserIDs         []uuid.UUID
	RoleIDs         []uuid.UUID
	EveryoneLiteral bool // 正文含 @everyone / @here 字面量（是否生效见 everyoneEffective）
}

// parseMentionTokens 纯解析：提取去重后的用户/角色提及与 everyone 字面量。
func parseMentionTokens(content string) mentionTokens {
	tokens := mentionTokens{
		EveryoneLiteral: strings.Contains(content, "@everyone") || strings.Contains(content, "@here"),
	}
	tokens.UserIDs = extractMentionIDs(mentionUserPattern, content)
	tokens.RoleIDs = extractMentionIDs(mentionRolePattern, content)
	return tokens
}

func extractMentionIDs(pattern *regexp.Regexp, content string) []uuid.UUID {
	matches := pattern.FindAllStringSubmatch(content, -1)
	ids := make([]uuid.UUID, 0, len(matches))
	seen := make(map[uuid.UUID]struct{}, len(matches))
	for _, match := range matches {
		id, err := uuid.Parse(match[1])
		if err != nil {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// everyoneEffective @everyone / @here 是否生效：仅当作者具备 MENTION_EVERYONE 权限；
// 无权限时字面量不生效（mention_everyone=false）但正文原样保留（docs 05 FR-19）。
func everyoneEffective(tokens mentionTokens, bits rbac.Permission) bool {
	return tokens.EveryoneLiteral && rbac.Has(bits, rbac.MentionEveryone)
}

// resolvedMentions 校验后的提及结果（落库字段的来源）。
type resolvedMentions struct {
	Users    model.UUIDList
	Roles    model.UUIDList
	Everyone bool
}

// resolveMentions 解析正文并按 guild 校验：
//   - 用户提及必须确为该服成员（防跨服 ID 塞入 payload）；
//   - 角色提及必须为该服角色，且排除 @everyone 角色（防经 <@&everyone_role_id>
//     绕过 MENTION_EVERYONE 权限做全员扇出）；
//   - everyone 字面量需作者具备 MENTION_EVERYONE 权限。
func (s *service) resolveMentions(guildID uuid.UUID, content string, bits rbac.Permission) (resolvedMentions, error) {
	tokens := parseMentionTokens(content)
	result := resolvedMentions{
		Users:    model.UUIDList{},
		Roles:    model.UUIDList{},
		Everyone: everyoneEffective(tokens, bits),
	}
	if len(tokens.UserIDs) > 0 {
		var memberUserIDs []uuid.UUID
		if err := s.db.Model(&model.Member{}).
			Where("guild_id = ? AND user_id IN ?", guildID, tokens.UserIDs).
			Pluck("user_id", &memberUserIDs).Error; err != nil {
			return result, err
		}
		result.Users = keepOrder(tokens.UserIDs, memberUserIDs)
	}
	if len(tokens.RoleIDs) > 0 {
		var roleIDs []uuid.UUID
		if err := s.db.Model(&model.Role{}).
			Where("guild_id = ? AND id IN ? AND is_everyone = false", guildID, tokens.RoleIDs).
			Pluck("id", &roleIDs).Error; err != nil {
			return result, err
		}
		result.Roles = keepOrder(tokens.RoleIDs, roleIDs)
	}
	return result, nil
}

// keepOrder 按正文出现顺序保留通过校验的 ID（DB IN 查询结果顺序不稳定）。
func keepOrder(ordered []uuid.UUID, allowed []uuid.UUID) model.UUIDList {
	allowedSet := make(map[uuid.UUID]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	result := make(model.UUIDList, 0, len(allowed))
	for _, id := range ordered {
		if _, ok := allowedSet[id]; ok {
			result = append(result, id)
		}
	}
	return result
}
