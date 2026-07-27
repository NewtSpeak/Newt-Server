package message

import "errors"

var (
	errVisibleRolesTextOnly = errors.New("仅文本频道支持限定可见范围")
	errVisibleRoleInvalid   = errors.New("visible_role_ids 含无效或不属于本服的角色")
	errVisibleRolesDisabled = errors.New("本频道已关闭限定可见消息")
)
