package message

import "errors"

var (
	errVisibleRolesTextOnly = errors.New("仅文本频道支持限定可见范围")
	errVisibleRoleInvalid   = errors.New("visible_role_ids 含无效或不属于本服的角色")
	errVisibleRolesDisabled = errors.New("本频道已关闭限定可见消息")
	errVisibleUserInvalid   = errors.New("visible_user_ids 含无效或不属于本服的用户")
	errVisibleUsersTooMany  = errors.New("visible_user_ids 超过人数上限")
)

// maxVisibleUsers 单条消息限定可见用户上限（与 bot ephemeral 名单量级一致）。
const maxVisibleUsers = 20
