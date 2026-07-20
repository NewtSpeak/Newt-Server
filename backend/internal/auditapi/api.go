// Package auditapi 审计日志查询 API（治理专项）。
//   - GET /api/v1/admin/audit-logs            系统管理员：跨服全量流水
//   - GET /api/v1/guilds/{gid}/audit-logs     需 VIEW_AUDIT_LOG 权限位（服管/所有者天然满足）：仅本服记录
//
// 均支持 actor_id / action 前缀 / target_type / 时间范围过滤，created_at 倒序 + before 游标分页。
package auditapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

type api struct {
	deps appdeps.Deps
}

// Register 挂载审计查询路由，并启动保留策略清理任务（AUDIT_RETENTION_DAYS）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	v1.GET("/admin/audit-logs", deps.Auth, h.listAdmin)
	v1.GET("/guilds/:guildID/audit-logs", deps.Auth, h.listGuild)
	audit.StartRetention(deps.DB)
	return nil
}

// RegisterClient 把本服审计查询投影到用户端认证平面（/gapi/v1，aud=client）：
// GET /guilds/{gid}/audit-logs（需 VIEW_AUDIT_LOG，服管/所有者天然满足）。
// deps.CurrentUser 必须为剥离 SystemAdmin 标志的用户端读取函数（clientapi 注入）；
// 跨服全量流水（/admin/audit-logs）保持仅后台可达。保留策略任务不重复启动。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	root.GET("/guilds/:guildID/audit-logs", deps.Auth, h.listGuild)
	return nil
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// entryView 查询响应条目：审计原始字段 + actor/guild/target 的展示信息。
type entryView struct {
	ID            uuid.UUID       `json:"id"`
	ActorID       *uuid.UUID      `json:"actor_id"`
	ActorType     string          `json:"actor_type"`
	ActorUsername string          `json:"actor_username,omitempty"`
	GuildID       *uuid.UUID      `json:"guild_id"`
	GuildName     string          `json:"guild_name,omitempty"`
	Action        string          `json:"action"`
	TargetType    string          `json:"target_type"`
	TargetID      string          `json:"target_id"`
	TargetSummary string          `json:"target_summary,omitempty"`
	Detail        json.RawMessage `json:"detail"`
	CreatedAt     time.Time       `json:"created_at"`
}

type listResponse struct {
	Items      []entryView `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
	HasMore    bool        `json:"has_more"`
}

// listAdmin GET /admin/audit-logs：系统管理员全量视图，可按 guild_id 过滤。
func (h *api) listAdmin(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	if !user.SystemAdmin {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可查看全量审计日志")
		return
	}
	query := h.deps.DB.Model(&model.AuditLog{})
	if raw := c.Query("guild_id"); raw != "" {
		guildID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "guild_id 无效")
			return
		}
		query = query.Where("guild_id = ?", guildID)
	}
	h.list(c, query)
}

// listGuild GET /guilds/{gid}/audit-logs：本服视图，需 VIEW_AUDIT_LOG（服管/所有者天然满足）。
func (h *api) listGuild(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	guildID, err := uuid.Parse(c.Param("guildID"))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	ctx, err := perms.LoadGuild(h.deps.DB, user, guildID)
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "服务器不存在")
		return
	}
	if !ctx.SystemAdmin && !ctx.Owner && !ctx.Has(rbac.ViewAuditLog) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "需要查看审计日志权限")
		return
	}
	h.list(c, h.deps.DB.Model(&model.AuditLog{}).Where("guild_id = ?", guildID))
}

// list 公共过滤 + 游标分页逻辑；query 已含作用域约束（全量或单服）。
func (h *api) list(c *gin.Context, query *gorm.DB) {
	if raw := c.Query("actor_id"); raw != "" {
		actorID, err := uuid.Parse(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "actor_id 无效")
			return
		}
		query = query.Where("actor_id = ?", actorID)
	}
	if raw := strings.TrimSpace(c.Query("action")); raw != "" {
		// action 前缀匹配（如 restriction. 匹配该模块全部动作）；转义 LIKE 元字符防注入通配。
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(raw)
		query = query.Where("action LIKE ?", escaped+"%")
	}
	if raw := strings.TrimSpace(c.Query("target_type")); raw != "" {
		query = query.Where("target_type = ?", raw)
	}
	if raw := c.Query("since"); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "since 需为 RFC3339 时间")
			return
		}
		query = query.Where("created_at >= ?", since)
	}
	if raw := c.Query("until"); raw != "" {
		until, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "until 需为 RFC3339 时间")
			return
		}
		query = query.Where("created_at <= ?", until)
	}
	limit := defaultLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "limit 无效")
			return
		}
		limit = min(parsed, maxLimit)
	}
	if raw := c.Query("before"); raw != "" {
		cursorTime, cursorID, ok := decodeCursor(raw)
		if !ok {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "before 游标无效")
			return
		}
		// (created_at, id) 复合游标：同一时间戳的多条记录靠 id 决出稳定顺序。
		query = query.Where("(created_at, id) < (?, ?)", cursorTime, cursorID)
	}

	var rows []model.AuditLog
	// 多取一条用于判断 has_more。
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取审计日志失败")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	response := listResponse{Items: h.enrich(rows), HasMore: hasMore}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		response.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	c.JSON(http.StatusOK, response)
}

// encodeCursor 游标编码为 "UnixNano_uuid"，与 (created_at, id) 复合排序一致。
func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	return strconv.FormatInt(createdAt.UnixNano(), 10) + "_" + id.String()
}

func decodeCursor(raw string) (time.Time, uuid.UUID, bool) {
	parts := strings.SplitN(raw, "_", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, false
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return time.Unix(0, nanos).UTC(), id, true
}

// enrich 批量联表补充 actor_username / guild_name / target_summary，避免逐行查询。
func (h *api) enrich(rows []model.AuditLog) []entryView {
	views := make([]entryView, 0, len(rows))
	userIDs := map[uuid.UUID]struct{}{}
	guildIDs := map[uuid.UUID]struct{}{}
	channelIDs := map[uuid.UUID]struct{}{}
	roleIDs := map[uuid.UUID]struct{}{}
	nodeIDs := map[uuid.UUID]struct{}{}
	for _, row := range rows {
		if row.ActorID != nil {
			userIDs[*row.ActorID] = struct{}{}
		}
		if row.GuildID != nil {
			guildIDs[*row.GuildID] = struct{}{}
		}
		if targetID, err := uuid.Parse(row.TargetID); err == nil {
			switch row.TargetType {
			case "user":
				userIDs[targetID] = struct{}{}
			case "channel":
				channelIDs[targetID] = struct{}{}
			case "role":
				roleIDs[targetID] = struct{}{}
			case "sfu_node":
				nodeIDs[targetID] = struct{}{}
			case "guild":
				guildIDs[targetID] = struct{}{}
			}
		}
	}
	usernames := lookupNames[model.User](h.deps.DB, "users", "username", userIDs)
	guildNames := lookupNames[model.Guild](h.deps.DB, "guilds", "name", guildIDs)
	channelNames := lookupNames[model.Channel](h.deps.DB, "channels", "name", channelIDs)
	roleNames := lookupNames[model.Role](h.deps.DB, "roles", "name", roleIDs)
	nodeNames := lookupNames[model.SfuNode](h.deps.DB, "sfu_nodes", "display_name", nodeIDs)

	for _, row := range rows {
		view := entryView{
			ID:         row.ID,
			ActorID:    row.ActorID,
			ActorType:  row.ActorType,
			GuildID:    row.GuildID,
			Action:     row.Action,
			TargetType: row.TargetType,
			TargetID:   row.TargetID,
			Detail:     json.RawMessage(row.Detail),
			CreatedAt:  row.CreatedAt,
		}
		if !json.Valid(view.Detail) {
			view.Detail = json.RawMessage("{}")
		}
		if row.ActorID != nil {
			view.ActorUsername = usernames[*row.ActorID]
		}
		if row.GuildID != nil {
			view.GuildName = guildNames[*row.GuildID]
		}
		if targetID, err := uuid.Parse(row.TargetID); err == nil {
			switch row.TargetType {
			case "user":
				view.TargetSummary = usernames[targetID]
			case "channel":
				view.TargetSummary = channelNames[targetID]
			case "role":
				view.TargetSummary = roleNames[targetID]
			case "sfu_node":
				view.TargetSummary = nodeNames[targetID]
			case "guild":
				view.TargetSummary = guildNames[targetID]
			}
		}
		views = append(views, view)
	}
	return views
}

// lookupNames 按 id 集合批量查某表的展示名（id → name）。
func lookupNames[T any](db *gorm.DB, table, column string, ids map[uuid.UUID]struct{}) map[uuid.UUID]string {
	result := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return result
	}
	list := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var rows []struct {
		ID   uuid.UUID
		Name string
	}
	if err := db.Table(table).Select("id, "+column+" AS name").Where("id IN ?", list).Scan(&rows).Error; err != nil {
		return result
	}
	for _, row := range rows {
		result[row.ID] = row.Name
	}
	return result
}
