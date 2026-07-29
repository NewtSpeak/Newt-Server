package moderation

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// api 成员治理 REST 处理器。
type api struct {
	deps appdeps.Deps
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// guildCtx 解析 guildID 并加载当前用户权限上下文；不可见统一 404。
func (h *api) guildCtx(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return nil, user, false
	}
	return ctx, user, true
}

// highestRoleOf 目标成员的最高角色 position（无角色为 0，即 @everyone 基线）。
func (h *api) highestRoleOf(memberID uuid.UUID) int {
	var highest int
	h.deps.DB.Raw(`SELECT COALESCE(MAX(roles.position), 0) FROM roles JOIN member_roles ON member_roles.role_id = roles.id WHERE member_roles.member_id = ?`, memberID).Scan(&highest)
	return highest
}

// findMemberByPathID 解析路径段中的成员标识：优先 members.id，其次 user_id。
// 文档写 :uid（用户 ID），历史客户端/部分调用也传成员记录 ID，两侧均接受。
func (h *api) findMemberByPathID(guildID, pathID uuid.UUID) (model.Member, bool) {
	var member model.Member
	if err := h.deps.DB.First(&member, "id = ? AND guild_id = ?", pathID, guildID).Error; err == nil {
		return member, true
	}
	if err := h.deps.DB.First(&member, "user_id = ? AND guild_id = ?", pathID, guildID).Error; err == nil {
		return member, true
	}
	return member, false
}

// canGovern 治理层级判定（docs 02 §4）：系统管任意（所有者除外，由调用方先拦）；
// 所有者任意；其余需最高角色 position 严格大于目标。
func canGovern(ctx *perms.GuildContext, targetHighest int) bool {
	if ctx.SystemAdmin || ctx.Owner {
		return true
	}
	return ctx.HighestRole > targetHighest
}

// removeMember 从服务器移除成员（连带角色绑定），发布 GUILD_MEMBER_REMOVE，
// 并在其在语音频道时发 CapsDirty 让语音模块断开。
func (h *api) removeMember(member model.Member, reason string) error {
	if err := h.deps.DB.Where("member_id = ?", member.ID).Delete(&model.MemberRole{}).Error; err != nil {
		return err
	}
	if err := h.deps.DB.Delete(&model.Member{}, "id = ?", member.ID).Error; err != nil {
		return err
	}
	// 剩余成员按 guild 广播；被移除者已不在成员表（且 gateway 成员缓存有 30s TTL，
	// 不能依赖其收到广播），额外定向下发一份，客户端按 member_id+event_at 幂等去重。
	removePayload := eventbus.NewGuildMemberRemovePayload(member, reason)
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventbus.EventGuildMemberRemove, GuildID: &member.GuildID, Payload: removePayload,
	})
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventbus.EventGuildMemberRemove, GuildID: &member.GuildID,
		UserIDs: []uuid.UUID{member.UserID}, Payload: removePayload,
	})
	var state model.VoiceState
	if err := h.deps.DB.First(&state, "guild_id = ? AND user_id = ?", member.GuildID, member.UserID).Error; err == nil && state.ChannelID != nil {
		guildID, channelID := member.GuildID, *state.ChannelID
		h.deps.Bus.Publish(eventbus.Event{
			Type:      eventbus.InternalCapsDirty,
			GuildID:   &guildID,
			ChannelID: &channelID,
			UserIDs:   []uuid.UUID{member.UserID},
			Payload: eventbus.CapsDirtyPayload{
				GuildID:   guildID.String(),
				ChannelID: channelID.String(),
				UserID:    member.UserID.String(),
				Reason:    reason,
			},
		})
	}
	return nil
}

func (h *api) audit(ctx *perms.GuildContext, user model.User, action, targetType, targetID string, detail map[string]any) {
	actorID := user.ID
	actorType := "user"
	if ctx.SystemAdmin {
		actorType = "system_admin"
	} else if ctx.Owner || ctx.Has(rbac.Administrator) {
		actorType = "guild_admin"
	}
	guildID := ctx.Guild.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID:    &actorID,
		ActorType:  actorType,
		GuildID:    &guildID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
	})
}
