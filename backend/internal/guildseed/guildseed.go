// Package guildseed 建服默认角色种子与存量回填。
//
// 建服时除 @everyone 外自动创建一个内置「@admin」角色（Role.Managed=true，
// permissions 锁定为 ADMINISTRATOR——RBAC 短路等价于全部权限，渐变红配色），
// 服务器所有者可直接把成员拉进该角色完成提管，无需手动建角色配权限。
// 内置角色的保护规则（不可删除、权限锁定、成员操作仅限所有者/管理员）
// 由 internal/guildapi 的角色端点实施。
//
// 独立成包的原因：clientapi 与 httpapi 各有一份 createGuild（历史上有意不耦合），
// 建服种子逻辑在此收敛为单一实现；同时 internal/database 启动时需要调用存量回填，
// 本包仅依赖 model/rbac/gorm，不会把 guildapi 的依赖树引入 database。
package guildseed

import (
	"log"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// AdminRoleName 内置管理员角色的初始名称（名称可由所有者改，managed 标记不变）。
	AdminRoleName = "@admin"
	// legacyAdminRoleName 旧默认名：存量回填时仍叫此名的 managed 角色会更名为 @admin。
	legacyAdminRoleName = "管理员"
	// AdminRoleColor 内置管理员角色的基础色（成员列表分组/降级渲染用的主色）。
	AdminRoleColor = "#E03131"
	// AdminRoleStyle 内置管理员角色的用户名样式：线性渐变红
	//（schema 见 internal/customization/style.go 的 RoleStyle）。
	AdminRoleStyle = `{"type":"linear","colors":["#FF6B6B","#C40000"],"angle":135}`
	// AdminRolePosition 内置管理员角色的层级：取一个显著高于常规手建角色的值，
	// 表达「仅次于 owner」的语义——普通 MANAGE_ROLES 持有者按现有层级校验
	//（HighestRole 必须严格大于目标角色 position）天然动不了它。
	// 成员绑定/解绑另有显式门槛（仅所有者或已持有 ADMINISTRATOR，见 guildapi），
	// 不依赖该数值本身。
	AdminRolePosition = 1000
)

// SeedDefaultRoles 在建服事务内创建默认角色：@everyone（position 0）与
// 内置「@admin」（完整权限、渐变红、managed）。调用方保证 guild 行已创建。
func SeedDefaultRoles(tx *gorm.DB, guildID uuid.UUID) error {
	everyone := model.Role{
		ID: uuid.New(), GuildID: guildID, Name: "@everyone",
		Permissions: int64(uint64(rbac.DefaultEveryone)), Position: 0, IsEveryone: true,
	}
	if err := tx.Create(&everyone).Error; err != nil {
		return err
	}
	admin := adminRole(guildID)
	return tx.Create(&admin).Error
}

func adminRole(guildID uuid.UUID) model.Role {
	return model.Role{
		ID: uuid.New(), GuildID: guildID, Name: AdminRoleName,
		// ADMINISTRATOR = 完整权限：rbac.GuildPermissions 对含该位的角色短路返回
		// AllDefined（全部权限位）。不直接存 AllDefined——它超出 JS Number 2^53
		// 精度（参见 guildapi/roles.go 对 52–54 扩展位的处理），会在前端解析/回传
		// 时丢精度并误触 managed 权限锁校验。
		Permissions: int64(uint64(rbac.Administrator)),
		Position:    AdminRolePosition, Managed: true,
		Color: AdminRoleColor, Style: AdminRoleStyle,
	}
}

// EnsureManagedAdminRoles 存量回填：对没有 managed 管理员角色的既有 guild 逐一
// 创建之，并把既有 managed 角色对齐当前默认值（仍叫旧默认名「管理员」且无同名
// 冲突的更名为 @admin；未配置颜色/样式的补默认渐变红——所有者自定义过的名称与
// 外观不覆盖）。幂等，启动时（AutoMigrate 后）调用；advisory 锁
// 防多实例并发重复。不发 GUILD_ROLE_CREATE/UPDATE——启动时 Gateway 尚无客户端
// 连接，且客户端连接后收到的 READY/GUILD_CREATE 快照本就包含全量角色。
// 若某 guild 已有同名（"@admin"）的手建角色，唯一索引冲突时跳过该服并记日志
//（该服已自行管理同名角色，不强行覆盖）。
func EnsureManagedAdminRoles(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "owl:managed-admin-roles").Error; err != nil {
			return err
		}
		var guildIDs []uuid.UUID
		err := tx.Model(&model.Guild{}).
			Where("NOT EXISTS (SELECT 1 FROM roles WHERE roles.guild_id = guilds.id AND roles.managed = true)").
			Pluck("id", &guildIDs).Error
		if err != nil {
			return err
		}
		for _, guildID := range guildIDs {
			admin := adminRole(guildID)
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&admin)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				log.Printf("guildseed: guild %s 已存在名为 %q 的角色，跳过内置管理员回填", guildID, AdminRoleName)
			}
		}
		return upgradeManagedAdminRoles(tx)
	})
}

// upgradeManagedAdminRoles 把既有 managed 角色对齐当前默认值（见 EnsureManagedAdminRoles 注释）。
func upgradeManagedAdminRoles(tx *gorm.DB) error {
	// 权限归一为 ADMINISTRATOR（等价完整权限，见 adminRole 注释）：managed 角色
	// 权限对外锁定，存量值只可能是历史种子值，直接对齐，顺带消除超出 JS 2^53
	// 精度的历史全量位集值。
	if err := tx.Model(&model.Role{}).
		Where("managed = true AND permissions <> ?", int64(uint64(rbac.Administrator))).
		Update("permissions", int64(uint64(rbac.Administrator))).Error; err != nil {
		return err
	}
	// 旧默认名更名（同 guild 已有 @admin 同名角色时跳过，避开唯一索引冲突）。
	if err := tx.Exec(
		`UPDATE roles SET name = ? WHERE managed = true AND name = ?
		   AND NOT EXISTS (SELECT 1 FROM roles other WHERE other.guild_id = roles.guild_id AND other.name = ?)`,
		AdminRoleName, legacyAdminRoleName, AdminRoleName,
	).Error; err != nil {
		return err
	}
	// 颜色/样式仅补空缺，不覆盖所有者的自定义。
	if err := tx.Model(&model.Role{}).
		Where("managed = true AND color = ''").
		Update("color", AdminRoleColor).Error; err != nil {
		return err
	}
	return tx.Model(&model.Role{}).
		Where("managed = true AND style::text IN ('{}', '')").
		Update("style", AdminRoleStyle).Error
}
