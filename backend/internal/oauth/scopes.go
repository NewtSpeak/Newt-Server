package oauth

import (
	"strings"

	"github.com/owlspeak/owl-server/backend/internal/security"
)

// 内置 scope（粗粒度 v1）。
const (
	ScopeOpenID            = "openid"
	ScopeProfile           = "profile"
	ScopeOfflineAccess     = "offline_access"
	ScopeGapiFull          = "gapi.full"
	ScopeGapiRead          = "gapi.read"
	ScopeGapiGuildsManage  = "gapi.guilds.manage"
	ScopePlatformRead      = "platform.read"
	ScopePlatformAdmin     = "platform.admin"
)

// DefaultCLIScopes CLI 默认申请的 scope。
var DefaultCLIScopes = []string{
	ScopeOpenID, ScopeProfile, ScopeGapiFull, ScopeOfflineAccess,
}

// AllKnownScopes 已知 scope 集合（未知 scope 拒绝）。
var AllKnownScopes = map[string]struct{}{
	ScopeOpenID:           {},
	ScopeProfile:          {},
	ScopeOfflineAccess:    {},
	ScopeGapiFull:         {},
	ScopeGapiRead:         {},
	ScopeGapiGuildsManage: {},
	ScopePlatformRead:     {},
	ScopePlatformAdmin:    {},
}

// ScopeNeedsSystemAdmin 平台 scope 仅 system_admin 可被授予。
func ScopeNeedsSystemAdmin(scope string) bool {
	return scope == ScopePlatformRead || scope == ScopePlatformAdmin
}

// ParseScopeList 拆分 scope 字符串。
func ParseScopeList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Fields(raw)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ValidateRequestedScopes 校验请求 scope；未知 scope 返回 false。
func ValidateRequestedScopes(scopes []string) (normalized string, ok bool) {
	if len(scopes) == 0 {
		scopes = DefaultCLIScopes
	}
	for _, s := range scopes {
		for _, part := range ParseScopeList(s) {
			if _, known := AllKnownScopes[part]; !known {
				return "", false
			}
		}
	}
	return security.NormalizeScopes(scopes), true
}

// FilterScopesForUser 按用户能力裁剪 scope（非 system_admin 去掉 platform.*）。
func FilterScopesForUser(scope string, systemAdmin bool) string {
	parts := ParseScopeList(scope)
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if ScopeNeedsSystemAdmin(p) && !systemAdmin {
			continue
		}
		kept = append(kept, p)
	}
	return security.NormalizeScopes(kept)
}

// HasGapiAccess agent 是否可访问 /gapi（任意 gapi.* scope）。
func HasGapiAccess(scope string) bool {
	return security.ScopeHasAny(scope, ScopeGapiFull, ScopeGapiRead, ScopeGapiGuildsManage)
}

// HasGapiWrite agent 是否可对 /gapi 做写操作。
func HasGapiWrite(scope string) bool {
	return security.ScopeHasAny(scope, ScopeGapiFull, ScopeGapiGuildsManage)
}

// HasPlatformRead agent 是否可只读 /api/v1。
func HasPlatformRead(scope string) bool {
	return security.ScopeHasAny(scope, ScopePlatformRead, ScopePlatformAdmin)
}

// HasPlatformWrite agent 是否可写 /api/v1。
func HasPlatformWrite(scope string) bool {
	return security.ScopeContains(scope, ScopePlatformAdmin)
}
