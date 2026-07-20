package userapi

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
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// 头像存储：本地磁盘 DataDir/profile/（与 customization 专项的头像/横幅同目录同 URL 约定，
// 复用其 /public-assets/profile/:name 公开访问路由）。文件名带纳秒版本号保证 URL 不可变，
// 可配 immutable 长缓存；头像属公开资料（任何能看到该用户的人都可见），
// 故采用公开路径而非 message 附件的签名下载——签名下载面向频道级私有附件，语义不符。

const maxAvatarBytes = int64(8 << 20) // 8 MiB（docs 01 FR-12）

// avatarExt 允许的头像 MIME → 扩展名（docs 01 FR-16；GIF 为动态头像）。
var avatarExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

func avatarDir(dataDir string) string { return filepath.Join(dataDir, "profile") }

// uploadAvatar POST /users/@me/avatar：multipart 上传（字段名 file），≤8MB，
// 仅 png/jpeg/webp/gif（以文件内容嗅探为准，不信任声明的 Content-Type）。
func (h *api) uploadAvatar(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 multipart 文件字段 file")
		return
	}
	if fileHeader.Size > maxAvatarBytes {
		fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("头像不能超过 %d 字节", maxAvatarBytes))
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传内容失败")
		return
	}
	defer file.Close()

	// 内容嗅探（前 512 字节魔数）判定真实格式。
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	contentType := strings.Split(http.DetectContentType(head[:n]), ";")[0]
	ext, ok := avatarExt[contentType]
	if !ok {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "头像仅支持 PNG/JPEG/WebP/GIF 图片")
		return
	}

	dir := avatarDir(h.deps.Cfg.DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建存储目录失败")
		return
	}
	filename := fmt.Sprintf("%s-avatar-%d.%s", user.ID, time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入文件失败")
		return
	}
	written, err := out.Write(head[:n])
	if err == nil {
		var copied int64
		copied, err = io.Copy(out, io.LimitReader(file, maxAvatarBytes+1))
		written += int(copied)
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || int64(written) > maxAvatarBytes {
		_ = os.Remove(target)
		if int64(written) > maxAvatarBytes {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("头像不能超过 %d 字节", maxAvatarBytes))
			return
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或写入失败")
		return
	}

	url := "/public-assets/profile/" + filename
	updates := map[string]any{"avatar_url": url, "avatar_animated": contentType == "image/gif"}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	removeAvatarFile(dir, user.AvatarURL)

	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取资料失败")
		return
	}
	h.publishUserUpdate(fresh)
	c.JSON(http.StatusOK, gin.H{"avatar": url, "user": fresh})
}

// deleteAvatar DELETE /users/@me/avatar：移除头像（清空 URL 并删除磁盘文件）。
func (h *api) deleteAvatar(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	updates := map[string]any{"avatar_url": "", "avatar_animated": false}
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	removeAvatarFile(avatarDir(h.deps.Cfg.DataDir), user.AvatarURL)
	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取资料失败")
		return
	}
	h.publishUserUpdate(fresh)
	c.JSON(http.StatusOK, fresh)
}

// removeAvatarFile 按历史 URL 删除磁盘文件（仅接受本模块/customization 生成的公开资产路径）。
func removeAvatarFile(dir, url string) {
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
