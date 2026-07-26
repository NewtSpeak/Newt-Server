package audit

// ActionInfo 描述某 action 的默认可逆性与撤销提示（中文，供 API enrich）。
type ActionInfo struct {
	Reversible   bool
	Irreversible bool
	// UndoHint 撤销将发生什么（展示在确认框）。
	UndoHint string
	// Label 操作中文名。
	Label string
	// RequiredPerm 反向操作所需权限位名称（文档用；实际校验在 handler）。
	RequiredPerm string
}

// catalog 全量管理 action 目录。未登记 action 仍可写审计，可逆性由 Entry 或 before 推断。
var catalog = map[string]ActionInfo{
	// —— 成员治理 ——
	"moderation.ban": {
		Reversible: true, Label: "封禁用户",
		UndoHint: "解除封禁（不会自动拉回成员，对方需自行重新加入）", RequiredPerm: "BAN_MEMBERS",
	},
	"moderation.unban": {
		Reversible: true, Label: "解除封禁",
		UndoHint: "按原原因重新封禁该用户", RequiredPerm: "BAN_MEMBERS",
	},
	"moderation.kick": {
		Irreversible: true, Label: "踢出成员",
		UndoHint: "踢出不可强制撤销，需用户重新加入",
	},
	"moderation.member_leave": {
		Irreversible: true, Label: "成员退出",
	},
	"moderation.member_join": {
		Irreversible: true, Label: "成员加入",
	},
	"moderation.nickname_update": {
		Reversible: true, Label: "修改昵称",
		UndoHint: "恢复变更前的昵称", RequiredPerm: "MANAGE_NICKNAMES",
	},
	"moderation.invite_create": {
		Reversible: true, Label: "创建邀请",
		UndoHint: "撤销该邀请码", RequiredPerm: "CREATE_INSTANT_INVITE",
	},

	// —— Restriction ——
	"restriction.create": {
		Reversible: true, Label: "施加限制",
		UndoHint: "解除该限制", RequiredPerm: "MODERATE_MEMBERS",
	},
	"restriction.lift": {
		Reversible: true, Label: "解除限制",
		UndoHint: "按快照重新施加该限制", RequiredPerm: "MODERATE_MEMBERS",
	},
	"restriction.update": {
		Reversible: true, Label: "修改限制",
		UndoHint: "恢复限制变更前的字段", RequiredPerm: "MODERATE_MEMBERS",
	},
	"restriction.expire": {
		Irreversible: true, Label: "限制到期",
	},

	// —— RBAC / 结构 ——
	"rbac.role_create": {
		Reversible: true, Label: "创建角色",
		UndoHint: "删除该角色（将清理成员绑定）", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.role_update": {
		Reversible: true, Label: "修改角色",
		UndoHint: "恢复角色变更前的名称、权限与层级", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.role_delete": {
		Reversible: true, Label: "删除角色",
		UndoHint: "按快照重建角色并恢复成员绑定", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.role_reorder": {
		Reversible: true, Label: "角色排序",
		UndoHint: "恢复角色排序", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.member_role_assign": {
		Reversible: true, Label: "分配角色",
		UndoHint: "移除该角色绑定", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.member_role_remove": {
		Reversible: true, Label: "移除角色",
		UndoHint: "重新分配该角色", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.channel_create": {
		Reversible: true, Label: "创建频道",
		UndoHint: "删除该频道（若已有消息可能失败）", RequiredPerm: "MANAGE_CHANNELS",
	},
	"rbac.channel_update": {
		Reversible: true, Label: "修改频道",
		UndoHint: "恢复频道变更前的设置", RequiredPerm: "MANAGE_CHANNELS",
	},
	"rbac.channel_delete": {
		Irreversible: true, Label: "删除频道",
		UndoHint: "频道及消息历史不可自动恢复",
	},
	"rbac.channel_reorder": {
		Reversible: true, Label: "频道排序",
		UndoHint: "恢复频道排序", RequiredPerm: "MANAGE_CHANNELS",
	},
	"rbac.channel_overwrite_update": {
		Reversible: true, Label: "更新频道权限覆盖",
		UndoHint: "恢复覆盖变更前的 allow/deny", RequiredPerm: "MANAGE_ROLES",
	},
	"rbac.channel_overwrite_delete": {
		Reversible: true, Label: "删除频道权限覆盖",
		UndoHint: "重建被删除的权限覆盖", RequiredPerm: "MANAGE_ROLES",
	},

	// —— Guild ——
	"guild.create": {
		Reversible: true, Label: "创建服务器",
		UndoHint: "删除该服务器（危险）", RequiredPerm: "OWNER",
	},
	"guild.update": {
		Reversible: true, Label: "更新服务器",
		UndoHint: "恢复服务器名称与简介", RequiredPerm: "MANAGE_GUILD",
	},
	"guild.delete": {
		Irreversible: true, Label: "删除服务器",
	},
	"guild.transfer_ownership": {
		Reversible: true, Label: "转让所有权",
		UndoHint: "将所有权转回原所有者", RequiredPerm: "OWNER",
	},
	"guild.icon_update":  {Reversible: true, Label: "更新图标", UndoHint: "移除或恢复图标", RequiredPerm: "MANAGE_GUILD"},
	"guild.icon_remove":  {Reversible: true, Label: "移除图标", UndoHint: "需重新上传图标", RequiredPerm: "MANAGE_GUILD"},
	"guild.banner_update": {Reversible: true, Label: "更新横幅", UndoHint: "移除或恢复横幅", RequiredPerm: "MANAGE_GUILD"},
	"guild.banner_remove": {Reversible: true, Label: "移除横幅", UndoHint: "需重新上传横幅", RequiredPerm: "MANAGE_GUILD"},
	"guild.banner_add":    {Reversible: true, Label: "添加横幅", UndoHint: "移除该横幅", RequiredPerm: "MANAGE_GUILD"},
	"guild.banner_reorder": {Reversible: true, Label: "横幅排序", UndoHint: "恢复横幅顺序", RequiredPerm: "MANAGE_GUILD"},

	// —— 消息 / 配置 ——
	"message.delete_by_moderator": {
		Irreversible: true, Label: "管理删除消息",
		UndoHint: "消息内容不可从审计自动恢复",
	},
	"message.upload_limit": {
		Reversible: true, Label: "调整上传限制",
		UndoHint: "恢复上传限制配置", RequiredPerm: "MANAGE_GUILD",
	},
	"message.retention": {
		Reversible: true, Label: "调整消息保留",
		UndoHint: "恢复消息保留配置", RequiredPerm: "MANAGE_GUILD",
	},

	// —— 语音 / SFU ——
	"voice.admin_disconnect": {Irreversible: true, Label: "管理断开语音"},
	"voice.admin_move":       {Irreversible: true, Label: "管理移动语音"},
	"voice.server_state_update": {
		Reversible: true, Label: "服务端语音状态",
		UndoHint: "恢复服务器静音/禁听状态", RequiredPerm: "MUTE_MEMBERS",
	},
	"voice.migration.created":   {Irreversible: true, Label: "语音迁移创建"},
	"voice.migration.completed": {Irreversible: true, Label: "语音迁移完成"},
	"voice.migration.failed":    {Irreversible: true, Label: "语音迁移失败"},
	"sfu_node.drain": {
		Reversible: true, Label: "节点排空",
		UndoHint: "取消节点排空", RequiredPerm: "SYSTEM_ADMIN",
	},
	"sfu_node.undrain": {
		Reversible: true, Label: "取消排空",
		UndoHint: "重新排空节点", RequiredPerm: "SYSTEM_ADMIN",
	},
	"sfu_pool.guild_update": {
		Reversible: true, Label: "更新节点池（服管）",
		UndoHint: "恢复节点池配置", RequiredPerm: "MANAGE_GUILD",
	},
	"sfu_pool.admin_update": {
		Reversible: true, Label: "更新节点池（系统管）",
		UndoHint: "恢复节点池配置", RequiredPerm: "SYSTEM_ADMIN",
	},

	// —— 舞台 / 屏幕 ——
	"stage.config_update": {
		Reversible: true, Label: "舞台配置变更",
		UndoHint: "恢复舞台配置", RequiredPerm: "MANAGE_CHANNELS",
	},
	"stage.bring_down": {Irreversible: true, Label: "舞台抱下麦"},
	"stage.bring_up":   {Irreversible: true, Label: "舞台抱上麦"},
	"screen.stop_user": {Irreversible: true, Label: "强制结束共享"},
	"screen.guild_quota_update": {
		Reversible: true, Label: "调整屏幕配额",
		UndoHint: "恢复屏幕共享配额", RequiredPerm: "MANAGE_GUILD",
	},
	"screen.platform_settings_update": {
		Reversible: true, Label: "平台共享设置",
		UndoHint: "恢复平台屏幕共享设置", RequiredPerm: "SYSTEM_ADMIN",
	},

	// —— Bot / 贴图 ——
	"bot.install": {
		Reversible: true, Label: "安装机器人",
		UndoHint: "卸载该机器人", RequiredPerm: "MANAGE_BOTS",
	},
	"bot.uninstall": {
		Reversible: true, Label: "卸载机器人",
		UndoHint: "重新安装该机器人", RequiredPerm: "MANAGE_BOTS",
	},
	"bot.create":       {Irreversible: true, Label: "创建机器人"},
	"bot.delete":       {Irreversible: true, Label: "删除机器人"},
	"bot.token_create": {Irreversible: true, Label: "创建 Bot Token"},
	"bot.token_revoke": {Irreversible: true, Label: "吊销 Bot Token"},
	"bot.update": {
		Reversible: true, Label: "更新机器人",
		UndoHint: "恢复机器人元数据", RequiredPerm: "MANAGE_BOTS",
	},
	"sticker.pack.guild_ban": {
		Reversible: true, Label: "服内封禁贴图包",
		UndoHint: "解除贴图包封禁", RequiredPerm: "MANAGE_EXPRESSIONS",
	},
	"sticker.pack.guild_unban": {
		Reversible: true, Label: "解除贴图包封禁",
		UndoHint: "重新封禁贴图包", RequiredPerm: "MANAGE_EXPRESSIONS",
	},
	"sticker.pack.purge":      {Irreversible: true, Label: "清除贴图包"},
	"sticker.item.purge":      {Irreversible: true, Label: "清除贴图"},
	"sticker.pack.soft_delete": {Irreversible: true, Label: "软删除贴图包"},

	// —— SFU 节点自动部署（internal/sfudeploy）——
	"sfu.deploy.start": {
		Irreversible: true, Label: "发起 SFU 节点自动部署",
	},
	"sfu.deploy.finish": {
		Irreversible: true, Label: "SFU 节点部署结束",
	},
	"sfu.deploy.cancel": {
		Irreversible: true, Label: "取消 SFU 节点部署",
	},
	"sfu.deploy_server.create": {
		Reversible: true, Label: "保存部署目标服务器",
		UndoHint: "删除该服务器及其加密凭据",
	},
	"sfu.deploy_server.delete": {
		Irreversible: true, Label: "删除部署目标服务器",
		UndoHint: "凭据已随记录销毁，需重新录入",
	},

	// —— 撤销本身 ——
	"audit.undo": {
		Irreversible: true, Label: "撤销操作",
		UndoHint: "撤销记录不可再次撤销",
	},
}

// Lookup 查询 action 目录信息。
func Lookup(action string) (ActionInfo, bool) {
	info, ok := catalog[action]
	return info, ok
}

// LabelOf 返回 action 中文标签；未知则返回原 action。
func LabelOf(action string) string {
	if info, ok := catalog[action]; ok && info.Label != "" {
		return info.Label
	}
	return action
}

// HintOf 返回撤销提示。
func HintOf(action string) string {
	if info, ok := catalog[action]; ok {
		return info.UndoHint
	}
	return ""
}
