package guildapi

// 纯逻辑单元测试（不依赖数据库）：层级/防提权判定与权限掩码字符串化。

import (
	"testing"

	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

func normalCtx(highest int, bits rbac.Permission) *perms.GuildContext {
	return &perms.GuildContext{HighestRole: highest, Permissions: bits}
}

func TestCanGrant(t *testing.T) {
	cases := []struct {
		name      string
		ctx       *perms.GuildContext
		requested rbac.Permission
		position  int
		want      bool
	}{
		{"所有者任意授予", &perms.GuildContext{Owner: true}, rbac.Administrator, 100, true},
		{"系统管任意授予", &perms.GuildContext{SystemAdmin: true}, rbac.Administrator, 100, true},
		{"层级之下且权限子集", normalCtx(5, rbac.ManageRoles|rbac.KickMembers), rbac.KickMembers, 4, true},
		{"目标层级等于自身", normalCtx(5, rbac.ManageRoles|rbac.KickMembers), rbac.KickMembers, 5, false},
		{"目标层级高于自身", normalCtx(5, rbac.ManageRoles|rbac.KickMembers), rbac.KickMembers, 6, false},
		{"授予超过自身的权限位", normalCtx(5, rbac.ManageRoles), rbac.BanMembers, 4, false},
		{"授予 Administrator 防提权", normalCtx(5, rbac.ManageRoles|rbac.KickMembers), rbac.Administrator, 1, false},
	}
	for _, tc := range cases {
		if got := canGrant(tc.ctx, tc.requested, tc.position); got != tc.want {
			t.Errorf("%s: canGrant=%v，期待 %v", tc.name, got, tc.want)
		}
	}
}

func TestCanManageRole(t *testing.T) {
	everyone := model.Role{IsEveryone: true, Position: 0}
	low := model.Role{Position: 3}
	same := model.Role{Position: 5}
	high := model.Role{Position: 9}
	cases := []struct {
		name string
		ctx  *perms.GuildContext
		role model.Role
		want bool
	}{
		{"所有者可动 @everyone", &perms.GuildContext{Owner: true}, everyone, true},
		{"系统管可动 @everyone", &perms.GuildContext{SystemAdmin: true}, everyone, true},
		{"普通管理者不可动 @everyone", normalCtx(5, rbac.ManageRoles), everyone, false},
		{"层级之下可管理", normalCtx(5, rbac.ManageRoles), low, true},
		{"同层级不可管理", normalCtx(5, rbac.ManageRoles), same, false},
		{"更高层级不可管理", normalCtx(5, rbac.ManageRoles), high, false},
	}
	for _, tc := range cases {
		if got := canManageRole(tc.ctx, tc.role); got != tc.want {
			t.Errorf("%s: canManageRole=%v，期待 %v", tc.name, got, tc.want)
		}
	}
}

// TestMaskString 掩码字符串化：扩展位（52–54）超出 JS Number 2^53 精度，
// 必须以十进制字符串无损下发。
func TestMaskString(t *testing.T) {
	if got := maskString(0); got != "0" {
		t.Errorf("maskString(0)=%s", got)
	}
	if got := maskString(rbac.Permission(1) << 54); got != "18014398509481984" {
		t.Errorf("maskString(1<<54)=%s，期待 18014398509481984", got)
	}
	all := ^rbac.Permission(0)
	if got := maskString(all); got != "18446744073709551615" {
		t.Errorf("maskString(全 1)=%s", got)
	}
}
