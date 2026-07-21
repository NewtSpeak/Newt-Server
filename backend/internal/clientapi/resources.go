package clientapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/guildapi"
	"github.com/owlspeak/owl-server/backend/internal/guildseed"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/moderation"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
	"gorm.io/gorm"
)

// errInviteExhausted 并发竞争下邀请次数被用尽（对外映射为 404，不泄露信息）。
var errInviteExhausted = errors.New("邀请使用次数已用尽")

// publishGuildJoined 建服/入服后的事件下发：对当事人定向发 GUILD_CREATE 全量快照；
// 加入（非建服）时再按 guild 广播 GUILD_MEMBER_ADD（docs 14 §3.2）。
func (h *api) publishGuildJoined(user model.User, member model.Member, isJoin bool) {
	if h.deps.Bus == nil {
		return
	}
	// 系统所有者保留 SystemAdmin，快照可见全部频道；普通用户按成员权限过滤。
	if payload, err := snapshot.NewGuildCreatePayload(h.deps.DB, user, member.GuildID); err == nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type: eventbus.EventGuildCreate, GuildID: &member.GuildID,
			UserIDs: []uuid.UUID{user.ID}, Payload: payload,
		})
	}
	if isJoin {
		h.deps.Bus.Publish(eventbus.Event{
			Type: eventbus.EventGuildMemberAdd, GuildID: &member.GuildID,
			Payload: eventbus.NewGuildMemberAddPayload(member, user),
		})
	}
}

// guildCtx 加载当前用户在指定服务器的权限上下文（用户端语义）。
// 系统所有者（system_admin）享受全权限短路（docs 04 FR-32），可管理未加入的服务器；
// 普通用户非成员返回 404（perms.ErrNotFound 的防扫频语义：不可见即不存在）。
func (h *api) guildCtx(c *gin.Context) (*perms.GuildContext, model.User, bool) {
	user := currentUser(c)
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

// myGuilds GET /gapi/v1/users/@me/guilds：我加入的服务器列表。
// 系统所有者返回平台全部服务器（docs 04 FR-32 管理视图）；普通用户仅成员关系。
// 每条附带 banners（多 banner 列表，服务器外观专项）；banner 图片 URL 为
// /public-assets 公开路径（平面中立前缀，与用户头像同约定），不涉及后台前缀。
func (h *api) myGuilds(c *gin.Context) {
	user := currentUser(c)
	var guilds []model.Guild
	var err error
	if user.SystemAdmin {
		err = h.deps.DB.Order("created_at DESC").Find(&guilds).Error
	} else {
		err = h.deps.DB.Joins("JOIN members ON members.guild_id = guilds.id").
			Where("members.user_id = ?", user.ID).
			Order("guilds.created_at DESC").Find(&guilds).Error
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取服务器列表失败")
		return
	}
	c.JSON(http.StatusOK, guildapi.WithBanners(h.deps.DB, guilds))
}

type createGuildRequest struct {
	Name string `json:"name" binding:"required,min=2,max=100"`
}

// createGuild POST /gapi/v1/guilds：用户创建服务器。
// 事务逻辑与后台 createGuild 一致（服务器 + 所有者成员 + 默认角色种子），
// 为避免与 httpapi 包耦合此处独立实现，两侧需保持同步演进；
// 默认角色（@everyone + 内置管理员）收敛在 guildseed.SeedDefaultRoles。
func (h *api) createGuild(c *gin.Context) {
	var input createGuildRequest
	if !bind(c, &input) {
		return
	}
	user := currentUser(c)
	guild := model.Guild{ID: uuid.New(), Name: strings.TrimSpace(input.Name), OwnerUserID: user.ID}
	var member model.Member
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&guild).Error; err != nil {
			return err
		}
		member = model.Member{ID: uuid.New(), GuildID: guild.ID, UserID: user.ID}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		return guildseed.SeedDefaultRoles(tx, guild.ID)
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建服务器失败")
		return
	}
	h.publishGuildJoined(user, member, false)
	c.JSON(http.StatusCreated, guild)
}

// listChannels GET /gapi/v1/guilds/{gid}/channels：当前用户可见的频道（VIEW_CHANNEL）。
func (h *api) listChannels(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	channels, err := ctx.VisibleChannels(h.deps.DB)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取频道失败")
		return
	}
	c.JSON(http.StatusOK, channels)
}

type memberSummary struct {
	ID             uuid.UUID   `json:"id"`
	UserID         uuid.UUID   `json:"user_id"`
	Username       string      `json:"username"`
	// DisplayName 系统显示名（全局，非服内昵称）；展示优先级：nickname > display_name > username。
	DisplayName    string      `json:"display_name"`
	Nickname       string      `json:"nickname"`
	AvatarURL      string      `json:"avatar_url"`
	AvatarAnimated bool        `json:"avatar_animated"`
	BannerURL      string      `json:"banner_url"`
	Bio            string      `json:"bio"`
	IsOwner        bool        `json:"is_owner"`
	RoleIDs        []uuid.UUID `json:"role_ids"`
}

// listMembers GET /gapi/v1/guilds/{gid}/members：服务器成员列表（需本人是成员）。
// 支持游标分页（Owl-Desktop docs 02 FR-24/FR-25 大服懒加载）：
// ?limit=1..1000（缺省=全量，兼容旧客户端）、?after=<member_id>（按 created_at,id 游标）。
// 附带用户全局资料（显示名/头像/横幅/签名），供成员列表与消息区渲染。
func (h *api) listMembers(c *gin.Context) {
	ctx, _, ok := h.guildCtx(c)
	if !ok {
		return
	}
	guild := ctx.Guild
	type row struct {
		model.Member
		Username       string
		DisplayName    string
		AvatarURL      string
		AvatarAnimated bool
		BannerURL      string
		Bio            string
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "limit 需为 1–1000 的整数")
			return
		}
		if parsed > 1000 {
			parsed = 1000
		}
		limit = parsed
	}
	query := `SELECT members.*, users.username, users.display_name, users.avatar_url,
			users.avatar_animated, users.banner_url, users.bio
		FROM members JOIN users ON users.id = members.user_id WHERE members.guild_id = ?`
	args := []any{guild.ID}
	if raw := c.Query("after"); raw != "" {
		afterID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "after 需为成员 ID")
			return
		}
		var anchor model.Member
		if err := h.deps.DB.First(&anchor, "id = ? AND guild_id = ?", afterID, guild.ID).Error; err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "after 游标成员不存在")
			return
		}
		query += ` AND (members.created_at, members.id) > (?, ?)`
		args = append(args, anchor.CreatedAt, anchor.ID)
	}
	query += ` ORDER BY members.created_at ASC, members.id ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	var rows []row
	err := h.deps.DB.Raw(query, args...).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取成员失败")
		return
	}
	memberIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		memberIDs = append(memberIDs, r.Member.ID)
	}
	bindings := make(map[uuid.UUID][]uuid.UUID)
	if len(memberIDs) > 0 {
		var links []model.MemberRole
		if err := h.deps.DB.Where("member_id IN ?", memberIDs).Find(&links).Error; err == nil {
			for _, link := range links {
				bindings[link.MemberID] = append(bindings[link.MemberID], link.RoleID)
			}
		}
	}
	result := make([]memberSummary, 0, len(rows))
	for _, r := range rows {
		roleIDs := bindings[r.Member.ID]
		if roleIDs == nil {
			roleIDs = []uuid.UUID{}
		}
		result = append(result, memberSummary{
			ID:             r.Member.ID,
			UserID:         r.Member.UserID,
			Username:       r.Username,
			DisplayName:    r.DisplayName,
			Nickname:       r.Member.Nickname,
			AvatarURL:      r.AvatarURL,
			AvatarAnimated: r.AvatarAnimated,
			BannerURL:      r.BannerURL,
			Bio:            r.Bio,
			IsOwner:        r.Member.UserID == guild.OwnerUserID,
			RoleIDs:        roleIDs,
		})
	}
	c.JSON(http.StatusOK, result)
}

// joinInvite POST /gapi/v1/invites/{code}/join：凭邀请码加入服务器。
// 邀请解析/次数消耗复用 moderation 的共享助手：过期/不存在/超次统一 404
// 不泄露信息；被 Ban 拒绝（docs 12 AG.4）；已是成员幂等返回 200（不计次数）。
func (h *api) joinInvite(c *gin.Context) {
	user := currentUser(c)
	invite, status := moderation.ResolveActiveInvite(h.deps.DB, c.Param("code"))
	if status != 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
		return
	}
	var ban model.GuildBan
	if err := h.deps.DB.First(&ban, "guild_id = ? AND user_id = ?", invite.GuildID, user.ID).Error; err == nil {
		fail(c, http.StatusForbidden, "BANNED", "你已被该服务器封禁，无法加入")
		return
	}
	var existing model.Member
	if err := h.deps.DB.First(&existing, "guild_id = ? AND user_id = ?", invite.GuildID, user.ID).Error; err == nil {
		c.JSON(http.StatusOK, existing)
		return
	}
	member := model.Member{ID: uuid.New(), GuildID: invite.GuildID, UserID: user.ID}
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		consumed, err := moderation.ConsumeInviteUse(tx, invite.ID)
		if err != nil {
			return err
		}
		if !consumed {
			return errInviteExhausted
		}
		return tx.Create(&member).Error
	})
	if err != nil {
		if errors.Is(err, errInviteExhausted) {
			fail(c, http.StatusNotFound, "NOT_FOUND", "邀请不存在或已过期")
			return
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(http.StatusOK, member)
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "加入服务器失败")
		return
	}
	actorID := user.ID
	guildID := invite.GuildID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID:    &actorID,
		ActorType:  "user",
		GuildID:    &guildID,
		Action:     "moderation.member_join",
		TargetType: "member",
		TargetID:   member.ID.String(),
		Detail:     map[string]any{"invite_code": invite.Code},
	})
	h.publishGuildJoined(user, member, true)
	c.JSON(http.StatusCreated, member)
}
