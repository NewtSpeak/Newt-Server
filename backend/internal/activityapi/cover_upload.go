package activityapi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// 活动封面上传：本地提取的游戏图标等，写入 public-assets 供他人可见。
const maxCoverBytes = int64(2 << 20) // 2 MiB

var coverExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

// uploadCover POST /activity/cover multipart file
// 返回 { cover_url: "/public-assets/activity/..." }
func (h *api) uploadCover(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_REQUEST", "message": "缺少 multipart 文件字段 file"}})
		return
	}
	if fileHeader.Size > maxCoverBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "FILE_TOO_LARGE", "message": "封面不能超过 2MB"}})
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "UPLOAD_FAILED", "message": "读取上传失败"}})
		return
	}
	defer file.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	contentType := strings.Split(http.DetectContentType(head[:n]), ";")[0]
	ext, ok := coverExt[contentType]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "UNSUPPORTED_TYPE", "message": "封面仅支持 PNG/JPEG/WebP"}})
		return
	}

	dir := filepath.Join(h.deps.Cfg.DataDir, "activity-covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "STORAGE_ERROR", "message": "创建目录失败"}})
		return
	}
	filename := fmt.Sprintf("%s-%d.%s", user.ID.String(), time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "STORAGE_ERROR", "message": "写入失败"}})
		return
	}
	written, err := out.Write(head[:n])
	if err == nil {
		var copied int64
		copied, err = io.Copy(out, io.LimitReader(file, maxCoverBytes+1))
		written += int(copied)
	}
	_ = out.Close()
	if err != nil || written == 0 || int64(written) > maxCoverBytes {
		_ = os.Remove(target)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "UPLOAD_FAILED", "message": "上传失败"}})
		return
	}

	// 使用 /public-assets/activity/:name（RegisterPublic 挂载）
	url := "/public-assets/activity/" + filename
	c.JSON(http.StatusOK, gin.H{"cover_url": url})
}

// RegisterPublic 挂载活动封面公开访问（/public-assets/activity/:name）。
func RegisterPublic(group *gin.RouterGroup, deps appdeps.Deps) error {
	dir := filepath.Join(deps.Cfg.DataDir, "activity-covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	group.GET("/activity/:name", func(c *gin.Context) {
		name := path.Base(c.Param("name"))
		if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			c.Status(http.StatusNotFound)
			return
		}
		// 仅允许字母数字与 . - _
		for _, r := range name {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
				r == '.' || r == '-' || r == '_' {
				continue
			}
			c.Status(http.StatusNotFound)
			return
		}
		full := filepath.Join(dir, name)
		if !strings.HasPrefix(full, dir) {
			c.Status(http.StatusNotFound)
			return
		}
		c.File(full)
	})
	return nil
}
