package rbac

import "testing"

func TestGuildPermissions(t *testing.T) {
	tests := []struct {
		name  string
		owner bool
		roles []RolePermissions
		want  Permission
	}{
		{name: "角色权限取并集", roles: []RolePermissions{{Permissions: ViewChannel}, {Permissions: SendMessages}}, want: ViewChannel | SendMessages},
		{name: "所有者短路", owner: true, want: AllDefined},
		{name: "管理员短路", roles: []RolePermissions{{Permissions: Administrator}}, want: AllDefined},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GuildPermissions(tt.owner, tt.roles); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestChannelPermissionsOrder(t *testing.T) {
	roles := []RolePermissions{
		{ID: "everyone", Permissions: ViewChannel | SendMessages, Everyone: true},
		{ID: "moderator", Permissions: ManageMessages},
	}
	overwrites := []Overwrite{
		{TargetID: "everyone", Deny: SendMessages},
		{TargetID: "moderator", Allow: SendMessages, Deny: ViewChannel},
		{TargetID: "user-1", Member: true, Allow: ViewChannel, Deny: SendMessages},
	}
	got := ChannelPermissions(false, "user-1", roles, overwrites)
	if Has(got, SendMessages) {
		t.Fatal("成员 deny 应在角色 allow 后生效")
	}
	if !Has(got, ViewChannel) {
		t.Fatal("成员 allow 应在角色 deny 后生效")
	}
	if !Has(got, ManageMessages) {
		t.Fatal("未覆盖的角色权限应保留")
	}
}

func TestDenyWinsWithinOverwrite(t *testing.T) {
	roles := []RolePermissions{{ID: "everyone", Everyone: true}}
	overwrites := []Overwrite{{TargetID: "everyone", Allow: ViewChannel, Deny: ViewChannel}}
	if got := ChannelPermissions(false, "user", roles, overwrites); Has(got, ViewChannel) {
		t.Fatal("同一覆盖中 allow/deny 冲突时 deny 必须优先")
	}
}
