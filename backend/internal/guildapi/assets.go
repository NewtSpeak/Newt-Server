package guildapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// 服务器图标/横幅存储（Newt-Desktop docs 02 FR-13/§8-9）：与用户头像同用
// DataDir/profile 目录和 /public-assets/profile/:name 公开访问路由（customization
// 模块注册），文件名带纳秒版本号保证 URL 不可变，可配 immutable 长缓存。
// 图标/banner 额外支持短循环 MP4（客户端默认静音、悬停出声；banner 多张轮播）。

const maxGuildAssetBytes = int64(8 << 20) // 8 MiB，与用户头像一致

var guildAssetExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
	"video/mp4":  "mp4",
}

// sniffGuildAsset 魔数嗅探展示资产类型（不信客户端 Content-Type）。
// 除 http.DetectContentType 外，额外识别 ISO BMFF / MP4 的 ftyp 盒（部分封装
// 会被 DetectContentType 判为 application/octet-stream）。
func sniffGuildAsset(head []byte) (contentType, ext string, ok bool) {
	if len(head) == 0 {
		return "", "", false
	}
	contentType = strings.Split(http.DetectContentType(head), ";")[0]
	if ext, allowed := guildAssetExt[contentType]; allowed {
		return contentType, ext, true
	}
	// MP4/ISO BMFF：offset 4 起为 "ftyp"
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		return "video/mp4", "mp4", true
	}
	return "", "", false
}

// uploadGuildIcon POST /guilds/{gid}/icon（multipart 字段 file，需 MANAGE_GUILD）。
func (h *api) uploadGuildIcon(c *gin.Context) { h.uploadGuildAsset(c, "icon") }

// deleteGuildIcon DELETE /guilds/{gid}/icon（需 MANAGE_GUILD）。
func (h *api) deleteGuildIcon(c *gin.Context) { h.deleteGuildAsset(c, "icon") }

// uploadGuildBanner POST /guilds/{gid}/banner（multipart 字段 file，需 MANAGE_GUILD）。
func (h *api) uploadGuildBanner(c *gin.Context) { h.uploadGuildAsset(c, "banner") }

// deleteGuildBanner DELETE /guilds/{gid}/banner（需 MANAGE_GUILD）。
func (h *api) deleteGuildBanner(c *gin.Context) { h.deleteGuildAsset(c, "banner") }

// saveGuildAssetFile 图片上传共享核心（图标/横幅/banner 列表复用）：
// 读取 multipart 字段 file → 魔数嗅探校验格式（不信 Content-Type）→ 大小上限 →
// 写入 DataDir/profile，返回公开访问 URL。失败时已写入错误响应（ok=false）。
func (h *api) saveGuildAssetFile(c *gin.Context, guildID uuid.UUID, kind string) (string, bool) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 multipart 文件字段 file")
		return "", false
	}
	if fileHeader.Size > maxGuildAssetBytes {
		fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("文件不能超过 %d 字节", maxGuildAssetBytes))
		return "", false
	}
	file, err := fileHeader.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传内容失败")
		return "", false
	}
	defer file.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	_, ext, allowed := sniffGuildAsset(head[:n])
	if !allowed {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "仅支持 PNG/JPEG/WebP/GIF 图片或 MP4 视频")
		return "", false
	}

	dir := filepath.Join(h.deps.Cfg.DataDir, "profile")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建存储目录失败")
		return "", false
	}
	filename := fmt.Sprintf("guild-%s-%s-%d.%s", guildID, kind, time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入文件失败")
		return "", false
	}
	written, err := out.Write(head[:n])
	if err == nil {
		var copied int64
		copied, err = io.Copy(out, io.LimitReader(file, maxGuildAssetBytes+1))
		written += int(copied)
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || int64(written) > maxGuildAssetBytes {
		_ = os.Remove(target)
		if int64(written) > maxGuildAssetBytes {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("文件不能超过 %d 字节", maxGuildAssetBytes))
			return "", false
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或写入失败")
		return "", false
	}
	return "/public-assets/profile/" + filename, true
}

// uploadGuildAsset 图标/横幅上传共享核心：保存文件 → 更新 Guild 对应 URL 列 →
// 删除旧文件 → GUILD_UPDATE 广播。
func (h *api) uploadGuildAsset(c *gin.Context, kind string) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageGuild)
	if !ok {
		return
	}
	url, ok := h.saveGuildAssetFile(c, ctx.Guild.ID, kind)
	if !ok {
		return
	}
	dir := filepath.Join(h.deps.Cfg.DataDir, "profile")
	target := filepath.Join(dir, path.Base(url))
	column, oldURL := "icon_url", ctx.Guild.IconURL
	if kind == "banner" {
		column, oldURL = "banner_url", ctx.Guild.BannerURL
	}
	if err := h.deps.DB.Model(&model.Guild{}).Where("id = ?", ctx.Guild.ID).Update(column, url).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存服务器资料失败")
		return
	}
	removeGuildAssetFile(dir, oldURL)

	guild := ctx.Guild
	if kind == "banner" {
		guild.BannerURL = url
	} else {
		guild.IconURL = url
	}
	h.audit(ctx, user, "guild."+kind+"_update", "guild", guild.ID.String(), map[string]any{"url": url})
	guildID := guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildUpdatePayload(guild),
	})
	c.JSON(http.StatusOK, gin.H{"url": url, "guild": guild})
}

// deleteGuildAsset 图标/横幅移除共享核心。
func (h *api) deleteGuildAsset(c *gin.Context, kind string) {
	ctx, user, ok := h.requireGuildPermission(c, rbac.ManageGuild)
	if !ok {
		return
	}
	column, oldURL := "icon_url", ctx.Guild.IconURL
	if kind == "banner" {
		column, oldURL = "banner_url", ctx.Guild.BannerURL
	}
	if err := h.deps.DB.Model(&model.Guild{}).Where("id = ?", ctx.Guild.ID).Update(column, "").Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存服务器资料失败")
		return
	}
	removeGuildAssetFile(filepath.Join(h.deps.Cfg.DataDir, "profile"), oldURL)
	guild := ctx.Guild
	if kind == "banner" {
		guild.BannerURL = ""
	} else {
		guild.IconURL = ""
	}
	h.audit(ctx, user, "guild."+kind+"_remove", "guild", guild.ID.String(), nil)
	guildID := guild.ID
	h.publish(eventbus.Event{
		Type: eventbus.EventGuildUpdate, GuildID: &guildID,
		Payload: eventbus.NewGuildUpdatePayload(guild),
	})
	c.JSON(http.StatusOK, guild)
}

// removeGuildAssetFile 按历史 URL 删除磁盘文件（仅接受本模块生成的公开资产路径）。
func removeGuildAssetFile(dir, url string) {
	if url == "" || !strings.HasPrefix(url, "/public-assets/profile/") {
		return
	}
	name := path.Base(url)
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = err
	}
}
