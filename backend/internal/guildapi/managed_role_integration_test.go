package guildapi_test

// 内置「管理员」角色（internal/guildseed）集成测试：需要真实 PostgreSQL，
// 运行方式见 integration_test.go 头注释。
//
// 覆盖（任务验收项）：
//  1. 建服自动创建 managed 管理员角色（ADMINISTRATOR、position=guildseed.AdminRolePosition），
//     roles 列表 JSON 携带 managed 字段；
//  2. 保护规则：不可删除（409 MANAGED_ROLE）、permissions/position 锁定（409）、
//     不参与批量排序（409）；名称可由所有者改；
//  3. 成员操作门槛：MANAGE_ROLES 持有者动不了它（403），owner 授予后成员
//     permissions/@me 含 ADMINISTRATOR；已是管理员者可再授予他人；
//     管理员之间不能互相摘除（层级相等），owner 可以；
//  4. 存量回填 EnsureManagedAdminRoles 幂等；同名手建角色存在时跳过不炸。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/guildseed"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// findManagedRole 从用户端 roles 列表 JSON 中找 managed 角色（顺带校验字段形态）。
func findManagedRole(t *testing.T, env *testEnv, token string, guildID uuid.UUID) map[string]any {
	t.Helper()
	rec := doListRoles(t, env, token, guildID)
	for _, item := range rec {
		role := item.(map[string]any)
		if role["managed"] == true {
			return role
		}
	}
	t.Fatalf("roles 列表中没有 managed 角色: %v", rec)
	return nil
}

func doListRoles(t *testing.T, env *testEnv, token string, guildID uuid.UUID) []any {
	t.Helper()
	rec, _ := env.do(t, http.MethodGet, fmt.Sprintf("/gapi/v1/guilds/%s/roles", guildID), token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("roles 列表返回 %d: %s", rec.Code, rec.Body.String())
	}
	var roles []any
	if err := json.Unmarshal(rec.Body.Bytes(), &roles); err != nil {
		t.Fatalf("解析 roles 列表失败: %v", err)
	}
	return roles
}

func TestManagedAdminRole(t *testing.T) {
	env := newEnv(t)
	owner := env.signup(t)
	memberA := env.signup(t)
	memberB := env.signup(t)
	manager := env.signup(t)
	guildID := env.createGuild(t, owner)
	memberAID := env.join(t, guildID, memberA)
	memberBID := env.join(t, guildID, memberB)
	managerMemberID := env.join(t, guildID, manager)
	base := fmt.Sprintf("/gapi/v1/guilds/%s", guildID)

	// 1. 建服即有 managed 管理员角色：名称/权限/层级/JSON 字段形态。
	adminRole := findManagedRole(t, env, owner.Token, guildID)
	if adminRole["name"] != guildseed.AdminRoleName {
		t.Errorf("内置角色名 %v，期待 %s", adminRole["name"], guildseed.AdminRoleName)
	}
	if got := adminRole["permissions"].(float64); uint64(got) != uint64(rbac.Administrator) {
		t.Errorf("内置角色 permissions=%v，期待 ADMINISTRATOR(%d)", got, uint64(rbac.Administrator))
	}
	if got := adminRole["position"].(float64); int(got) != guildseed.AdminRolePosition {
		t.Errorf("内置角色 position=%v，期待 %d", got, guildseed.AdminRolePosition)
	}
	if adminRole["is_everyone"] != false {
		t.Errorf("内置角色 is_everyone=%v，期待 false", adminRole["is_everyone"])
	}
	adminRoleID := adminRole["id"].(string)
	rolePath := fmt.Sprintf("%s/roles/%s", base, adminRoleID)

	// 2a. 不可删除（owner 也不行）→ 409 MANAGED_ROLE。
	rec, body := env.do(t, http.MethodDelete, rolePath, owner.Token, nil)
	if rec.Code != http.StatusConflict || errCode(body) != "MANAGED_ROLE" {
		t.Fatalf("删除内置角色返回 %d/%s，期待 409/MANAGED_ROLE", rec.Code, errCode(body))
	}
	// 2b. permissions 锁定 → 409。
	rec, body = env.do(t, http.MethodPatch, rolePath, owner.Token, map[string]any{
		"name": guildseed.AdminRoleName, "permissions": int64(uint64(rbac.ManageRoles)), "position": guildseed.AdminRolePosition,
	})
	if rec.Code != http.StatusConflict || errCode(body) != "MANAGED_ROLE" {
		t.Fatalf("改内置角色权限返回 %d/%s，期待 409/MANAGED_ROLE", rec.Code, errCode(body))
	}
	// 2c. position 锁定 → 409。
	rec, body = env.do(t, http.MethodPatch, rolePath, owner.Token, map[string]any{
		"name": guildseed.AdminRoleName, "permissions": int64(uint64(rbac.Administrator)), "position": 2,
	})
	if rec.Code != http.StatusConflict || errCode(body) != "MANAGED_ROLE" {
		t.Fatalf("改内置角色层级返回 %d/%s，期待 409/MANAGED_ROLE", rec.Code, errCode(body))
	}
	// 2d. 批量排序不可包含内置角色 → 409。
	rec, body = env.do(t, http.MethodPatch, base+"/roles", owner.Token, []map[string]any{
		{"id": adminRoleID, "position": 3},
	})
	if rec.Code != http.StatusConflict || errCode(body) != "MANAGED_ROLE" {
		t.Fatalf("排序内置角色返回 %d/%s，期待 409/MANAGED_ROLE", rec.Code, errCode(body))
	}
	// 2e. 名称可改（owner，permissions/position 原样回传）→ 200 且 managed 保持。
	rec, body = env.do(t, http.MethodPatch, rolePath, owner.Token, map[string]any{
		"name": "服务器管理组", "permissions": int64(uint64(rbac.Administrator)), "position": guildseed.AdminRolePosition,
	})
	if rec.Code != http.StatusOK || body["name"] != "服务器管理组" || body["managed"] != true {
		t.Fatalf("owner 改内置角色名返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 3a. MANAGE_ROLES 持有者（层级 5）动不了内置角色的成员 → 403。
	modRoleID := env.createRole(t, owner, guildID, "mod", rbac.ManageRoles, 5)
	env.assignRole(t, owner, guildID, managerMemberID, modRoleID)
	rec, _ = env.do(t, http.MethodPut, fmt.Sprintf("%s/members/%s/roles/%s", base, memberAID, adminRoleID), manager.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("MANAGE_ROLES 持有者绑定内置角色返回 %d，期待 403", rec.Code)
	}
	// MANAGE_ROLES 持有者也改不了它（层级不足）→ 403。
	rec, _ = env.do(t, http.MethodPatch, rolePath, manager.Token, map[string]any{
		"name": "劫持", "permissions": int64(uint64(rbac.Administrator)), "position": guildseed.AdminRolePosition,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("MANAGE_ROLES 持有者改内置角色返回 %d，期待 403", rec.Code)
	}

	// 3b. owner 授予成员 A → 200 + GUILD_MEMBER_UPDATE，permissions/@me 含 ADMINISTRATOR。
	rec, _ = env.do(t, http.MethodPut, fmt.Sprintf("%s/members/%s/roles/%s", base, memberAID, adminRoleID), owner.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner 授予内置角色返回 %d", rec.Code)
	}
	env.events.wait(t, "授予管理员后的 GUILD_MEMBER_UPDATE", func(e eventbus.Event) bool {
		payload, ok := e.Payload.(eventbus.GuildMemberUpdatePayload)
		if !ok || e.Type != eventbus.EventGuildMemberUpdate {
			return false
		}
		for _, id := range payload.RoleIDs {
			if id.String() == adminRoleID {
				return true
			}
		}
		return false
	})
	rec, body = env.do(t, http.MethodGet, base+"/permissions/@me", memberA.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("permissions/@me 返回 %d: %s", rec.Code, rec.Body.String())
	}
	mask, err := strconv.ParseUint(body["permissions"].(string), 10, 64)
	if err != nil {
		t.Fatalf("permissions 掩码解析失败: %v", body["permissions"])
	}
	if mask&uint64(rbac.Administrator) == 0 {
		t.Fatalf("管理员成员的 permissions/@me=%d 不含 ADMINISTRATOR", mask)
	}

	// 3c. 已是管理员的 A 可再授予 B → 200。
	rec, _ = env.do(t, http.MethodPut, fmt.Sprintf("%s/members/%s/roles/%s", base, memberBID, adminRoleID), memberA.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员授予他人内置角色返回 %d", rec.Code)
	}
	// 管理员也删不掉/改不了权限（409 在层级校验之前）。
	rec, body = env.do(t, http.MethodDelete, rolePath, memberA.Token, nil)
	if rec.Code != http.StatusConflict || errCode(body) != "MANAGED_ROLE" {
		t.Fatalf("管理员删内置角色返回 %d/%s，期待 409/MANAGED_ROLE", rec.Code, errCode(body))
	}
	// 管理员之间不能互相摘除（最高层级相等，目标成员治理校验不过）→ 403。
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/members/%s/roles/%s", base, memberBID, adminRoleID), memberA.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("管理员摘除另一管理员返回 %d，期待 403", rec.Code)
	}
	// owner 摘除 → 204。
	rec, _ = env.do(t, http.MethodDelete, fmt.Sprintf("%s/members/%s/roles/%s", base, memberBID, adminRoleID), owner.Token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner 摘除管理员返回 %d，期待 204", rec.Code)
	}

	// 4. 存量回填：删掉 managed 角色行模拟旧库，EnsureManagedAdminRoles 幂等补建。
	if err := env.db.Exec("DELETE FROM member_roles WHERE role_id = ?", adminRoleID).Error; err != nil {
		t.Fatalf("清理角色绑定失败: %v", err)
	}
	if err := env.db.Exec("DELETE FROM roles WHERE id = ?", adminRoleID).Error; err != nil {
		t.Fatalf("删除 managed 角色失败: %v", err)
	}
	for i := 0; i < 2; i++ { // 跑两遍验证幂等
		if err := guildseed.EnsureManagedAdminRoles(env.db); err != nil {
			t.Fatalf("回填第 %d 遍失败: %v", i+1, err)
		}
	}
	var refilled []model.Role
	if err := env.db.Find(&refilled, "guild_id = ? AND managed = true", guildID).Error; err != nil {
		t.Fatalf("查询回填角色失败: %v", err)
	}
	if len(refilled) != 1 {
		t.Fatalf("回填后 managed 角色数 %d，期待 1", len(refilled))
	}
	if uint64(refilled[0].Permissions) != uint64(rbac.Administrator) || refilled[0].Position != guildseed.AdminRolePosition {
		t.Fatalf("回填角色形态不符: %+v", refilled[0])
	}

	// 5. 同名手建角色占位时回填跳过（不炸、不覆盖）。
	otherOwner := env.signup(t)
	otherGuildID := env.createGuild(t, otherOwner)
	if err := env.db.Exec("DELETE FROM roles WHERE guild_id = ? AND managed = true", otherGuildID).Error; err != nil {
		t.Fatalf("删除 managed 角色失败: %v", err)
	}
	manual := model.Role{ID: uuid.New(), GuildID: otherGuildID, Name: guildseed.AdminRoleName, Permissions: int64(uint64(rbac.KickMembers)), Position: 3}
	if err := env.db.Create(&manual).Error; err != nil {
		t.Fatalf("手建同名角色失败: %v", err)
	}
	if err := guildseed.EnsureManagedAdminRoles(env.db); err != nil {
		t.Fatalf("同名占位时回填失败: %v", err)
	}
	var managedCount int64
	env.db.Model(&model.Role{}).Where("guild_id = ? AND managed = true", otherGuildID).Count(&managedCount)
	if managedCount != 0 {
		t.Fatalf("同名占位时不应强行创建 managed 角色（count=%d）", managedCount)
	}
}
