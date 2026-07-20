package customization

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
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// 头像/横幅上传：原始字节 PUT（Content-Type 声明格式），落地 DataDir/profile/。
// 文件名带纳秒版本号保证 URL 不可变，可放心配 immutable 缓存；旧文件上传成功后删除。

const (
	maxAvatarBytes = int64(8 << 20)  // 8 MiB（GIF 动态头像也够用）
	maxBannerBytes = int64(12 << 20) // 12 MiB
)

// allowedImageExt Content-Type → 扩展名；GIF 视为动态头像。
var allowedImageExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

func profileDir(dataDir string) string { return filepath.Join(dataDir, "profile") }

// uploadAvatar PUT /users/@me/avatar：本人上传头像（静态或 GIF 动态）。
func (h *api) uploadAvatar(c *gin.Context) { h.uploadProfileImage(c, "avatar", maxAvatarBytes) }

// uploadBanner PUT /users/@me/banner：本人上传个人横幅。
func (h *api) uploadBanner(c *gin.Context) { h.uploadProfileImage(c, "banner", maxBannerBytes) }

func (h *api) uploadProfileImage(c *gin.Context, kind string, maxBytes int64) {
	user := h.deps.CurrentUser(c)
	contentType := strings.TrimSpace(strings.Split(c.GetHeader("Content-Type"), ";")[0])
	ext, ok := allowedImageExt[contentType]
	if !ok {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "仅支持 PNG/JPEG/WebP/GIF 图片")
		return
	}
	dir := profileDir(h.deps.Cfg.DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建存储目录失败")
		return
	}
	filename := fmt.Sprintf("%s-%s-%d.%s", user.ID, kind, time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入文件失败")
		return
	}
	written, err := io.Copy(file, io.LimitReader(c.Request.Body, maxBytes+1))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || written > maxBytes {
		_ = os.Remove(target)
		if written > maxBytes {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("文件超过 %d 字节上限", maxBytes))
			return
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或写入失败")
		return
	}

	url := "/public-assets/profile/" + filename
	updates := map[string]any{}
	var previousURL string
	if kind == "avatar" {
		previousURL = user.AvatarURL
		updates["avatar_url"] = url
		updates["avatar_animated"] = contentType == "image/gif"
	} else {
		previousURL = user.BannerURL
		updates["banner_url"] = url
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	removeProfileFile(dir, previousURL)

	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err == nil {
		h.publishToUserGuilds(user.ID, eventbus.EventUserUpdate, fresh)
		c.JSON(http.StatusOK, fresh)
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

type profilePatchRequest struct {
	// AccentColor 个人强调色（#RRGGBB），传空字符串清除。
	AccentColor *string `json:"accent_color"`
	ClearAvatar bool    `json:"clear_avatar"`
	ClearBanner bool    `json:"clear_banner"`
}

// patchProfile PATCH /users/@me/profile：设置强调色 / 清除头像横幅。
func (h *api) patchProfile(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input profilePatchRequest
	if !bind(c, &input) {
		return
	}
	updates := map[string]any{}
	if input.AccentColor != nil {
		color := strings.TrimSpace(*input.AccentColor)
		if color != "" && !hexColorPattern.MatchString(color) {
			fail(c, http.StatusBadRequest, "INVALID_COLOR", "强调色需为 #RRGGBB 格式")
			return
		}
		updates["accent_color"] = color
	}
	dir := profileDir(h.deps.Cfg.DataDir)
	if input.ClearAvatar {
		removeProfileFile(dir, user.AvatarURL)
		updates["avatar_url"] = ""
		updates["avatar_animated"] = false
	}
	if input.ClearBanner {
		removeProfileFile(dir, user.BannerURL)
		updates["banner_url"] = ""
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "EMPTY_PATCH", "没有需要更新的字段")
		return
	}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取资料失败")
		return
	}
	h.publishToUserGuilds(user.ID, eventbus.EventUserUpdate, fresh)
	c.JSON(http.StatusOK, fresh)
}

// removeProfileFile 根据历史 URL 删除对应磁盘文件（仅接受本模块生成的文件名）。
func removeProfileFile(dir, url string) {
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

// serveProfileAsset GET /public-assets/profile/{name}：头像/横幅公开访问
//（文件名带版本号不可变，允许长缓存）。
func (h *api) serveProfileAsset(c *gin.Context) {
	name := c.Param("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	contentTypes := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
	}
	contentType, ok := contentTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	target := filepath.Join(profileDir(h.deps.Cfg.DataDir), name)
	if _, err := os.Stat(target); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.File(target)
}
