// Package auditundo 管理操作的补偿撤销执行器。
package auditundo

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// Context 撤销执行上下文。
type Context struct {
	Deps  appdeps.Deps
	Actor model.User
	// GuildCtx 本服操作时已加载；平台级可为 nil。
	GuildCtx *perms.GuildContext
}

// Result 补偿执行结果，由框架写入 audit.undo 并可选广播事件。
type Result struct {
	TargetType string
	TargetID   string
	Detail     map[string]any
	Before     map[string]any
	After      map[string]any
	Events     []eventbus.Event
}

// Handler 执行某 action 的补偿。须幂等友好；实体不存在返回 *Error。
type Handler func(ctx Context, log model.AuditLog) (Result, error)

// Spec 描述 handler 的权限要求。
type Spec struct {
	// Perm 需要的权限位；0 表示仅 SystemAdmin / Owner（见 RequireOwner）。
	Perm rbac.Permission
	// RequireOwner 仅所有者或系统管。
	RequireOwner bool
	// RequireSystemAdmin 仅系统管。
	RequireSystemAdmin bool
	// AllowNoGuild 允许无 guild 的平台级日志。
	AllowNoGuild bool
	Handler      Handler
}

var (
	mu       sync.RWMutex
	handlers = map[string]Spec{}
)

// Register 注册 action 的撤销 handler（包 init 或 RegisterAll 调用）。
func Register(action string, spec Spec) {
	mu.Lock()
	defer mu.Unlock()
	handlers[action] = spec
}

// Lookup 查找 handler。
func Lookup(action string) (Spec, bool) {
	mu.RLock()
	defer mu.RUnlock()
	spec, ok := handlers[action]
	return spec, ok
}

// Has 是否已注册可执行撤销。
func Has(action string) bool {
	_, ok := Lookup(action)
	return ok
}

// Error 可映射为 HTTP 的撤销错误。
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func errf(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// CheckPerm 校验 actor 是否具备该 Spec 的撤销权限。
func CheckPerm(ctx Context, spec Spec) error {
	if ctx.Actor.SystemAdmin {
		return nil
	}
	if spec.RequireSystemAdmin {
		return errf(403, "MISSING_PERMISSION", "需要系统管理员权限")
	}
	if ctx.GuildCtx == nil {
		if spec.AllowNoGuild {
			return errf(403, "MISSING_PERMISSION", "需要系统管理员权限")
		}
		return errf(400, "INVALID_REQUEST", "该操作需要服务器上下文")
	}
	g := ctx.GuildCtx
	if g.SystemAdmin || g.Owner {
		return nil
	}
	if spec.RequireOwner {
		return errf(403, "MISSING_PERMISSION", "需要服务器所有者权限")
	}
	if spec.Perm != 0 && !g.Has(spec.Perm) {
		return errf(403, "MISSING_PERMISSION", "没有撤销该操作所需的权限")
	}
	return nil
}

// ParseUUID 从字符串解析 UUID。
func ParseUUID(raw string) (uuid.UUID, error) {
	return uuid.Parse(raw)
}
