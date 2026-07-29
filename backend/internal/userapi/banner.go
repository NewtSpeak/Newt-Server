package userapi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// 个人横幅：与头像同目录同 URL 约定（DataDir/profile + /public-assets/profile/）。
// 上限 12 MiB，对齐 customization 专项。

const maxBannerBytes = int64(12 << 20)

// uploadBanner POST /users/@me/banner：multipart 上传（字段名 file），≤12MB，
// 仅 png/jpeg/webp/gif（以文件内容嗅探为准）。
func (h *api) uploadBanner(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 multipart 文件字段 file")
		return
	}
	if fileHeader.Size > maxBannerBytes {
		fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("横幅不能超过 %d 字节", maxBannerBytes))
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传内容失败")
		return
	}
	defer file.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	contentType := strings.Split(http.DetectContentType(head[:n]), ";")[0]
	ext, ok := avatarExt[contentType]
	if !ok {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "横幅仅支持 PNG/JPEG/WebP/GIF 图片")
		return
	}

	dir := avatarDir(h.deps.Cfg.DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建存储目录失败")
		return
	}
	filename := fmt.Sprintf("%s-banner-%d.%s", user.ID, time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入文件失败")
		return
	}
	written, err := out.Write(head[:n])
	if err == nil {
		var copied int64
		copied, err = io.Copy(out, io.LimitReader(file, maxBannerBytes+1))
		written += int(copied)
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || int64(written) > maxBannerBytes {
		_ = os.Remove(target)
		if int64(written) > maxBannerBytes {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("横幅不能超过 %d 字节", maxBannerBytes))
			return
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或写入失败")
		return
	}

	url := "/public-assets/profile/" + filename
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Update("banner_url", url).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	removeAvatarFile(dir, user.BannerURL)

	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取资料失败")
		return
	}
	h.publishUserUpdate(fresh)
	c.JSON(http.StatusOK, gin.H{"banner": url, "user": fresh})
}

// deleteBanner DELETE /users/@me/banner：移除个人横幅。
func (h *api) deleteBanner(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	if err := h.deps.DB.Model(&model.User{}).Where("id = ?", user.ID).Update("banner_url", "").Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存资料失败")
		return
	}
	removeAvatarFile(avatarDir(h.deps.Cfg.DataDir), user.BannerURL)
	var fresh model.User
	if err := h.deps.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取资料失败")
		return
	}
	h.publishUserUpdate(fresh)
	c.JSON(http.StatusOK, fresh)
}
