// Package auditapi 审计日志查询与撤销 API（治理专项）。
//   - GET  /api/v1/admin/audit-logs              系统管理员：跨服全量流水
//   - POST /api/v1/admin/audit-logs/:id/undo     系统管理员撤销
//   - GET  /api/v1/guilds/{gid}/audit-logs       需 VIEW_AUDIT_LOG：仅本服
//   - POST /api/v1/guilds/{gid}/audit-logs/:id/undo
//   - 用户端 /gapi/v1 镜像本服 list + undo
//
// 均支持 actor_id / action 前缀 / target_type / 时间范围 / undo_status 过滤，
// created_at 倒序 + before 游标分页。
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
	"github.com/owlspeak/owl-server/backend/internal/auditundo"
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

// Register 挂载审计查询/撤销路由，并启动保留策略清理任务（AUDIT_RETENTION_DAYS）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	v1.GET("/admin/audit-logs", deps.Auth, h.listAdmin)
	v1.POST("/admin/audit-logs/:logID/undo", deps.Auth, h.undoAdmin)
	v1.GET("/guilds/:guildID/audit-logs", deps.Auth, h.listGuild)
	v1.POST("/guilds/:guildID/audit-logs/:logID/undo", deps.Auth, h.undoGuild)
	audit.StartRetention(deps.DB)
	return nil
}

// RegisterClient 把本服审计查询/撤销投影到用户端认证平面（/gapi/v1，aud=client）。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	root.GET("/guilds/:guildID/audit-logs", deps.Auth, h.listGuild)
	root.POST("/guilds/:guildID/audit-logs/:logID/undo", deps.Auth, h.undoGuild)
	return nil
}

// RegisterBot 挂载机器人开放平面本服审计查询（对齐 Discord Get Guild Audit Log）。
// 需 VIEW_AUDIT_LOG；不暴露跨服 /admin/audit-logs。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	group.GET("/guilds/:guildID/audit-logs", deps.Auth, h.listGuild)
	return nil
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// entryView 查询响应条目：审计原始字段 + actor/guild/target 展示 + 可撤销元数据。
type entryView struct {
	ID            uuid.UUID       `json:"id"`
	ActorID       *uuid.UUID      `json:"actor_id"`
	ActorType     string          `json:"actor_type"`
	ActorUsername string          `json:"actor_username,omitempty"`
	GuildID       *uuid.UUID      `json:"guild_id"`
	GuildName     string          `json:"guild_name,omitempty"`
	Action        string          `json:"action"`
	ActionLabel   string          `json:"action_label,omitempty"`
	TargetType    string          `json:"target_type"`
	TargetID      string          `json:"target_id"`
	TargetSummary string          `json:"target_summary,omitempty"`
	Detail        json.RawMessage `json:"detail"`
	CreatedAt     time.Time       `json:"created_at"`

	Reversible bool       `json:"reversible"`
	UndoStatus string     `json:"undo_status"`
	UndoHint   string     `json:"undo_hint,omitempty"`
	UndoOfID   *uuid.UUID `json:"undo_of_id,omitempty"`
	UndoneByID *uuid.UUID `json:"undone_by_id,omitempty"`
	UndoneAt   *time.Time `json:"undone_at,omitempty"`
	// BeforeState 仅 include_state=1 时填充。
	BeforeState json.RawMessage `json:"before_state,omitempty"`
	AfterState  json.RawMessage `json:"after_state,omitempty"`
}

type listResponse struct {
	Items      []entryView `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
	HasMore    bool        `json:"has_more"`
}

type undoHTTPResponse struct {
	Original entryView `json:"original"`
	Undo     entryView `json:"undo"`
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

// listGuild GET /guilds/{gid}/audit-logs：本服视图，需 VIEW_AUDIT_LOG。
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

func (h *api) undoAdmin(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	if !user.SystemAdmin {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可在全量入口撤销")
		return
	}
	h.doUndo(c, user, nil)
}

func (h *api) undoGuild(c *gin.Context) {
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
	h.doUndo(c, user, &guildID)
}

func (h *api) doUndo(c *gin.Context, user model.User, guildScope *uuid.UUID) {
	logID, err := uuid.Parse(c.Param("logID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "审计 ID 无效")
		return
	}
	resp, err := auditundo.Undo(h.deps, logID, user, guildScope)
	if err != nil {
		if e, ok := err.(*auditundo.Error); ok {
			fail(c, e.Status, e.Code, e.Message)
			return
		}
		fail(c, http.StatusInternalServerError, "UNDO_FAILED", err.Error())
		return
	}
	includeState := c.Query("include_state") == "1"
	views := h.enrich([]model.AuditLog{resp.Original, resp.Undo}, includeState)
	out := undoHTTPResponse{}
	if len(views) >= 1 {
		out.Original = views[0]
	}
	if len(views) >= 2 {
		out.Undo = views[1]
	}
	c.JSON(http.StatusOK, out)
}

// list 公共过滤 + 游标分页逻辑。
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
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(raw)
		query = query.Where("action LIKE ?", escaped+"%")
	}
	if raw := strings.TrimSpace(c.Query("target_type")); raw != "" {
		query = query.Where("target_type = ?", raw)
	}
	if raw := strings.TrimSpace(c.Query("undo_status")); raw != "" {
		// 运行时 available 可能来自目录推断，库内可能是 available 或旧 none+reversible。
		switch raw {
		case model.AuditUndoAvailable:
			query = query.Where("(undo_status = ? OR (reversible = true AND undo_status IN (?, ?))) AND undone_by_id IS NULL AND undo_of_id IS NULL",
				model.AuditUndoAvailable, model.AuditUndoAvailable, model.AuditUndoNone)
		case model.AuditUndoUndone:
			query = query.Where("undo_status = ? OR undone_by_id IS NOT NULL", model.AuditUndoUndone)
		default:
			query = query.Where("undo_status = ?", raw)
		}
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
		query = query.Where("(created_at, id) < (?, ?)", cursorTime, cursorID)
	}

	var rows []model.AuditLog
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取审计日志失败")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	includeState := c.Query("include_state") == "1"
	response := listResponse{Items: h.enrich(rows, includeState), HasMore: hasMore}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		response.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	c.JSON(http.StatusOK, response)
}

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

func (h *api) enrich(rows []model.AuditLog, includeState bool) []entryView {
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
		status := auditundo.EffectiveUndoStatus(row)
		reversible := status == model.AuditUndoAvailable
		hint := ""
		if reversible {
			hint = audit.HintOf(row.Action)
			if hint == "" {
				hint = "撤销此操作"
			}
		} else if status == model.AuditUndoIrreversible {
			hint = audit.HintOf(row.Action)
			if hint == "" {
				hint = "该操作不可撤销"
			}
		} else if status == model.AuditUndoUndone {
			hint = "已撤销"
		} else if status == model.AuditUndoBlocked {
			hint = "当前无法撤销（缺少执行器或快照）"
		}

		view := entryView{
			ID:         row.ID,
			ActorID:    row.ActorID,
			ActorType:  row.ActorType,
			GuildID:    row.GuildID,
			Action:     row.Action,
			ActionLabel: audit.LabelOf(row.Action),
			TargetType: row.TargetType,
			TargetID:   row.TargetID,
			Detail:     json.RawMessage(row.Detail),
			CreatedAt:  row.CreatedAt,
			Reversible: reversible,
			UndoStatus: status,
			UndoHint:   hint,
			UndoOfID:   row.UndoOfID,
			UndoneByID: row.UndoneByID,
			UndoneAt:   row.UndoneAt,
		}
		if !json.Valid(view.Detail) {
			view.Detail = json.RawMessage("{}")
		}
		if includeState {
			if json.Valid([]byte(row.BeforeState)) {
				view.BeforeState = json.RawMessage(row.BeforeState)
			}
			if json.Valid([]byte(row.AfterState)) {
				view.AfterState = json.RawMessage(row.AfterState)
			}
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
