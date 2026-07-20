package rbac

type Permission uint64

const (
	CreateInstantInvite Permission = 1 << iota
	KickMembers
	BanMembers
	Administrator
	ManageChannels
	ManageGuild
	AddReactions
	ViewAuditLog
	PrioritySpeaker
	Stream
	ViewChannel
	SendMessages
	SendTTSMessages
	ManageMessages
	EmbedLinks
	AttachFiles
	ReadMessageHistory
	MentionEveryone
	UseExternalEmojis
	ViewGuildInsights
	Connect
	Speak
	MuteMembers
	DeafenMembers
	MoveMembers
	UseVAD
	ChangeNickname
	ManageNicknames
	ManageRoles
	ManageWebhooks
	ManageExpressions
	UseApplicationCommands
	RequestToSpeak
	ManageEvents
	ManageThreads
	CreatePublicThreads
	CreatePrivateThreads
	UseExternalStickers
	SendMessagesInThreads
	ModerateMembers
	ViewCreatorMonetizationAnalytics
	UseSoundboard
	CreateGuildExpressions
	CreateEvents
	UseExternalSounds
	SendVoiceMessages
	// 以下为定稿文档新增的权限节点：
	// docs 11 §7.2 舞台协管节点，docs 14 §7.4 屏幕共享节点。
	// 02 文档「46+ 保留位」被编号更大的 11/14 定稿覆盖（README 冲突规则）。
	StageBringUp     // 46 舞台：抱上麦
	StageBringDown   // 47 舞台：抱下麦
	StageManageQueue // 48 舞台：管理申请队列
	StageChangeMode  // 49 舞台：切换 FREE/STAGE 模式
	StreamEndOthers  // 50 屏幕共享：强制结束他人共享
	StreamQuality    // 51 屏幕共享：可选更高清晰度档
	// 以下为「AI 时代」扩展功能新增的权限节点：
	ManageBots       // 52 机器人：创建/配置/授权本服机器人集成
	ManageBadges     // 53 徽章：分配/回收徽章
	ManageCustomization // 54 自定义：编辑角色名样式（颜色/渐变）等展示定制
)

const AllDefined Permission = (1 << 55) - 1

const DefaultEveryone = ViewChannel | SendMessages | ReadMessageHistory | Connect | Speak | ChangeNickname | AddReactions | UseVAD

type RolePermissions struct {
	ID          string
	Permissions Permission
	Everyone    bool
}

type Overwrite struct {
	TargetID string
	Member   bool
	Allow    Permission
	Deny     Permission
}

func GuildPermissions(owner bool, roles []RolePermissions) Permission {
	if owner {
		return AllDefined
	}
	var result Permission
	for _, role := range roles {
		result |= role.Permissions
	}
	if result&Administrator != 0 {
		return AllDefined
	}
	return result & AllDefined
}

func ChannelPermissions(owner bool, userID string, roles []RolePermissions, overwrites []Overwrite) Permission {
	base := GuildPermissions(owner, roles)
	if owner || base&Administrator != 0 || base == AllDefined {
		return AllDefined
	}

	roleIDs := make(map[string]struct{}, len(roles))
	var everyoneID string
	for _, role := range roles {
		roleIDs[role.ID] = struct{}{}
		if role.Everyone {
			everyoneID = role.ID
		}
	}
	for _, overwrite := range overwrites {
		if !overwrite.Member && overwrite.TargetID == everyoneID {
			base = apply(base, overwrite)
		}
	}

	var roleAllow, roleDeny Permission
	for _, overwrite := range overwrites {
		if overwrite.Member || overwrite.TargetID == everyoneID {
			continue
		}
		if _, ok := roleIDs[overwrite.TargetID]; ok {
			roleDeny |= overwrite.Deny
			roleAllow |= overwrite.Allow
		}
	}
	base &= ^roleDeny
	base |= roleAllow

	for _, overwrite := range overwrites {
		if overwrite.Member && overwrite.TargetID == userID {
			base = apply(base, overwrite)
		}
	}
	return base & AllDefined
}

func Has(current, required Permission) bool { return current&required == required }

func apply(base Permission, overwrite Overwrite) Permission {
	base &= ^overwrite.Deny
	base |= overwrite.Allow &^ overwrite.Deny
	return base
}
