// Package platformbadge 平台级身份徽章（非服内 Badge 表）：
// 系统所有者（User.SystemAdmin）登录/资料/成员展示时自动附带，不落库。
package platformbadge

import (
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// KindSystemOwner 系统所有者徽章 kind，客户端可据此渲染专属样式。
const KindSystemOwner = "system_owner"

// Badge 平台徽章投影（登录、@me、READY、成员展示共用）。
type Badge struct {
	// ID 稳定字符串 ID（非 UUID）；系统所有者固定为 system_owner。
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Emoji       string     `json:"emoji"`
	Color       string     `json:"color"`
	// BadgeID 兼容服内徽章结构（空 UUID）；GrantedAt 便于客户端统一排序。
	BadgeID   uuid.UUID  `json:"badge_id"`
	GrantedAt time.Time  `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// SystemOwner 系统所有者徽章常量（展示用）。
func SystemOwner() Badge {
	return Badge{
		ID:          KindSystemOwner,
		Kind:        KindSystemOwner,
		Name:        "系统所有者",
		Description: "平台系统所有者，可管理全部服务器并打开管理员视图",
		Emoji:       "👑",
		Color:       "#F59E0B",
		BadgeID:     uuid.Nil,
		GrantedAt:   time.Unix(0, 0).UTC(),
	}
}

// ForUser 按账号身份返回平台徽章列表（当前仅 SystemAdmin → 系统所有者）。
func ForUser(user model.User) []Badge {
	if user.SystemAdmin {
		return []Badge{SystemOwner()}
	}
	return []Badge{}
}

// UserView 用户资料 + 平台徽章（登录 / GET @me / READY 复用）。
type UserView struct {
	model.User
	Badges []Badge `json:"badges"`
}

// ViewOf 包装用户与其平台徽章。
func ViewOf(user model.User) UserView {
	badges := ForUser(user)
	if badges == nil {
		badges = []Badge{}
	}
	return UserView{User: user, Badges: badges}
}
