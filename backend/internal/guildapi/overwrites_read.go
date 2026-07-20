package guildapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// overwriteView 覆盖读回条目：附目标名称便于控制台直接展示；
// allow/deny 同时给出十进制字符串形态（掩码 uint64 全 64 位无损）。
type overwriteView struct {
	ID         uuid.UUID           `json:"id"`
	ChannelID  uuid.UUID           `json:"channel_id"`
	Type       model.OverwriteType `json:"type"`
	TargetID   uuid.UUID           `json:"target_id"`
	TargetName string              `json:"target_name"`
	Allow      int64               `json:"allow"`
	Deny       int64               `json:"deny"`
	AllowStr   string              `json:"allow_str"`
	DenyStr    string              `json:"deny_str"`
}

// listOverwrites GET /guilds/{gid}/channels/{cid}/overwrites：频道既有覆盖读回
//（需 MANAGE_ROLES，控制台覆盖编辑器据此回显，替代从零盲写）。
func (h *api) listOverwrites(c *gin.Context) {
	ctx, _, ok := h.requireGuildPermission(c, rbac.ManageRoles)
	if !ok {
		return
	}
	channelID, err := uuid.Parse(c.Param("channelID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ? AND guild_id = ?", channelID, ctx.Guild.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var overwrites []model.ChannelOverwrite
	if err := h.deps.DB.Where("channel_id = ?", channel.ID).Order("created_at ASC").Find(&overwrites).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取覆盖失败")
		return
	}

	// 批量解析目标名称：角色名 / 成员用户名。
	roleIDs := make([]uuid.UUID, 0)
	memberIDs := make([]uuid.UUID, 0)
	for _, overwrite := range overwrites {
		if overwrite.Type == model.OverwriteRole {
			roleIDs = append(roleIDs, overwrite.TargetID)
		} else {
			memberIDs = append(memberIDs, overwrite.TargetID)
		}
	}
	names := make(map[uuid.UUID]string)
	if len(roleIDs) > 0 {
		var roles []model.Role
		if err := h.deps.DB.Where("id IN ?", roleIDs).Find(&roles).Error; err == nil {
			for _, role := range roles {
				names[role.ID] = role.Name
			}
		}
	}
	if len(memberIDs) > 0 {
		type row struct {
			ID       uuid.UUID
			Username string
			Nickname string
		}
		var rows []row
		err := h.deps.DB.Raw(`SELECT members.id, users.username, members.nickname
			FROM members JOIN users ON users.id = members.user_id
			WHERE members.id IN ?`, memberIDs).Scan(&rows).Error
		if err == nil {
			for _, r := range rows {
				name := r.Username
				if r.Nickname != "" {
					name = r.Nickname
				}
				names[r.ID] = name
			}
		}
	}

	result := make([]overwriteView, 0, len(overwrites))
	for _, overwrite := range overwrites {
		result = append(result, overwriteView{
			ID:         overwrite.ID,
			ChannelID:  overwrite.ChannelID,
			Type:       overwrite.Type,
			TargetID:   overwrite.TargetID,
			TargetName: names[overwrite.TargetID],
			Allow:      overwrite.Allow,
			Deny:       overwrite.Deny,
			AllowStr:   maskString(permissionMask(overwrite.Allow)),
			DenyStr:    maskString(permissionMask(overwrite.Deny)),
		})
	}
	c.JSON(http.StatusOK, result)
}
