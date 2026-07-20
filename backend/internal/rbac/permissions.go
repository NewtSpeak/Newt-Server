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
)

const AllDefined Permission = (1 << 46) - 1

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
