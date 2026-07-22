package customization

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

const maxRoleBadgeAssetBytes = int64(4 << 20) // 4 MiB（背景图可稍大）

// allowedBadgeAssetExt 徽章 icon / 背景图：位图 + SVG。
var allowedBadgeAssetExt = map[string]string{
	"image/png":     "png",
	"image/jpeg":    "jpg",
	"image/webp":    "webp",
	"image/gif":     "gif",
	"image/svg+xml": "svg",
}

func roleBadgeDir(dataDir string) string {
	return filepath.Join(dataDir, "role-badges")
}

type badgeAssetKind string

const (
	badgeAssetIcon badgeAssetKind = "icon"
	badgeAssetBg   badgeAssetKind = "bg"
)

// uploadRoleBadgeIcon PUT /guilds/{gid}/roles/{rid}/badge-icon
func (h *api) uploadRoleBadgeIcon(c *gin.Context) {
	h.uploadRoleBadgeAsset(c, badgeAssetIcon)
}

// deleteRoleBadgeIcon DELETE /guilds/{gid}/roles/{rid}/badge-icon
func (h *api) deleteRoleBadgeIcon(c *gin.Context) {
	h.deleteRoleBadgeAsset(c, badgeAssetIcon)
}

// uploadRoleBadgeBackground PUT /guilds/{gid}/roles/{rid}/badge-background
func (h *api) uploadRoleBadgeBackground(c *gin.Context) {
	h.uploadRoleBadgeAsset(c, badgeAssetBg)
}

// deleteRoleBadgeBackground DELETE /guilds/{gid}/roles/{rid}/badge-background
func (h *api) deleteRoleBadgeBackground(c *gin.Context) {
	h.deleteRoleBadgeAsset(c, badgeAssetBg)
}

func (h *api) uploadRoleBadgeAsset(c *gin.Context, kind badgeAssetKind) {
	role, ok := h.requireRoleStyleEditor(c)
	if !ok {
		return
	}
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	contentType := strings.TrimSpace(strings.Split(c.GetHeader("Content-Type"), ";")[0])
	ext, ok := allowedBadgeAssetExt[contentType]
	if !ok {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "仅支持 PNG/JPEG/WebP/GIF/SVG")
		return
	}
	dir := roleBadgeDir(h.deps.Cfg.DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建存储目录失败")
		return
	}
	filename := fmt.Sprintf("%s-%s-%s-%d.%s", role.GuildID, role.ID, kind, time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入文件失败")
		return
	}
	written, err := io.Copy(file, io.LimitReader(c.Request.Body, maxRoleBadgeAssetBytes+1))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || written > maxRoleBadgeAssetBytes {
		_ = os.Remove(target)
		if written > maxRoleBadgeAssetBytes {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", "文件超过 4MB 上限")
			return
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或写入失败")
		return
	}

	url := "/public-assets/role-badges/" + filename
	var style RoleStyle
	if role.Style != "" && role.Style != "{}" {
		_ = json.Unmarshal([]byte(role.Style), &style)
	}
	if style.Badge == nil {
		style.Badge = &RoleBadgeStyle{Enabled: true}
	} else {
		style.Badge.Enabled = true
	}
	var prevURL string
	if kind == badgeAssetIcon {
		prevURL = style.Badge.IconURL
		style.Badge.IconURL = url
	} else {
		prevURL = style.Badge.BackgroundImageURL
		style.Badge.BackgroundImageURL = url
	}
	normalized, err := style.Validate()
	if err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusBadRequest, "INVALID_STYLE", err.Error())
		return
	}
	if err := h.deps.DB.Model(&model.Role{}).Where("id = ?", role.ID).Update("style", normalized).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存角色样式失败")
		return
	}
	removeRoleBadgeFile(dir, prevURL)
	role.Style = normalized
	auditAction := "customization.role_badge_icon_upload"
	if kind == badgeAssetBg {
		auditAction = "customization.role_badge_bg_upload"
	}
	h.audit(ctx, user, auditAction, "role", role.ID.String(), map[string]any{
		"url": url, "kind": string(kind),
	})
	if h.deps.Bus != nil {
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventGuildRoleUpdate,
			GuildID: &guildID,
			Payload: eventbus.NewGuildRolePayload(*role),
		})
	}
	key := "icon_url"
	if kind == badgeAssetBg {
		key = "background_image_url"
	}
	c.JSON(http.StatusOK, gin.H{"role": role, key: url})
}

func (h *api) deleteRoleBadgeAsset(c *gin.Context, kind badgeAssetKind) {
	role, ok := h.requireRoleStyleEditor(c)
	if !ok {
		return
	}
	ctx, user, ok := h.guildCtx(c)
	if !ok {
		return
	}
	var style RoleStyle
	if role.Style != "" && role.Style != "{}" {
		_ = json.Unmarshal([]byte(role.Style), &style)
	}
	if style.Badge == nil {
		c.JSON(http.StatusOK, role)
		return
	}
	var prev string
	if kind == badgeAssetIcon {
		if style.Badge.IconURL == "" {
			c.JSON(http.StatusOK, role)
			return
		}
		prev = style.Badge.IconURL
		style.Badge.IconURL = ""
	} else {
		if style.Badge.BackgroundImageURL == "" {
			c.JSON(http.StatusOK, role)
			return
		}
		prev = style.Badge.BackgroundImageURL
		style.Badge.BackgroundImageURL = ""
	}
	normalized, err := style.Validate()
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_STYLE", err.Error())
		return
	}
	if err := h.deps.DB.Model(&model.Role{}).Where("id = ?", role.ID).Update("style", normalized).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存角色样式失败")
		return
	}
	removeRoleBadgeFile(roleBadgeDir(h.deps.Cfg.DataDir), prev)
	role.Style = normalized
	auditAction := "customization.role_badge_icon_delete"
	if kind == badgeAssetBg {
		auditAction = "customization.role_badge_bg_delete"
	}
	h.audit(ctx, user, auditAction, "role", role.ID.String(), nil)
	if h.deps.Bus != nil {
		guildID := ctx.Guild.ID
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventGuildRoleUpdate,
			GuildID: &guildID,
			Payload: eventbus.NewGuildRolePayload(*role),
		})
	}
	c.JSON(http.StatusOK, role)
}

func removeRoleBadgeFile(dir, url string) {
	if url == "" || !strings.HasPrefix(url, "/public-assets/role-badges/") {
		return
	}
	name := strings.TrimPrefix(url, "/public-assets/role-badges/")
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") {
		return
	}
	_ = os.Remove(filepath.Join(dir, name))
}

// serveRoleBadgeAsset GET /public-assets/role-badges/{name}
func (h *api) serveRoleBadgeAsset(c *gin.Context) {
	name := c.Param("name")
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") {
		fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在")
		return
	}
	target := filepath.Join(roleBadgeDir(h.deps.Cfg.DataDir), name)
	if _, err := os.Stat(target); err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在")
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.File(target)
}
