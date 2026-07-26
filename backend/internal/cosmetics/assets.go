package cosmetics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var allowedMIME = map[string]string{
	"image/png":       "png",
	"image/jpeg":      "jpg",
	"image/webp":      "webp",
	"image/gif":       "gif",
	"image/apng":      "png",
	"video/mp4":       "mp4",
	"video/webm":      "webm",
	"application/mp4": "mp4",
	"audio/ogg":       "ogg",
	"audio/mpeg":      "mp3",
	"audio/mp3":       "mp3",
	"audio/wav":       "wav",
	"audio/wave":      "wav",
	"audio/x-wav":     "wav",
}

func cosmeticsDir(dataDir string) string {
	return filepath.Join(dataDir, "cosmetics")
}

func normalizeMIME(raw string) string {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	switch mime {
	case "image/jpg":
		return "image/jpeg"
	case "image/apng":
		return "image/png"
	case "application/mp4":
		return "video/mp4"
	case "audio/mp3":
		return "audio/mpeg"
	default:
		return mime
	}
}

func sniffMediaMIME(data []byte, hinted string) string {
	hinted = normalizeMIME(hinted)
	if _, ok := allowedMIME[hinted]; ok && !strings.HasPrefix(hinted, "video/") {
		return hinted
	}
	if len(data) >= 12 {
		if string(data[4:8]) == "ftyp" {
			return "video/mp4"
		}
		if data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3 {
			return "video/webm"
		}
		if string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
			return "image/webp"
		}
		if string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a" {
			return "image/gif"
		}
		if data[0] == 0x89 && string(data[1:4]) == "PNG" {
			return "image/png"
		}
		if data[0] == 0xFF && data[1] == 0xD8 {
			return "image/jpeg"
		}
		if string(data[0:4]) == "OggS" {
			return "audio/ogg"
		}
		if string(data[0:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "WAVE" {
			return "audio/wav"
		}
		if len(data) >= 3 && string(data[0:3]) == "ID3" {
			return "audio/mpeg"
		}
		if data[0] == 0xFF && (data[1]&0xE0) == 0xE0 {
			return "audio/mpeg"
		}
	}
	detected := normalizeMIME(http.DetectContentType(data))
	if _, ok := allowedMIME[detected]; ok {
		return detected
	}
	if _, ok := allowedMIME[hinted]; ok {
		return hinted
	}
	return detected
}

func isAnimatedMIME(mime string) bool {
	switch mime {
	case "image/gif", "video/mp4", "video/webm":
		return true
	default:
		return false
	}
}

// pngIsAnimated 判断 PNG 是否为 APNG：按 chunk 结构遍历，acTL 出现在首个 IDAT 之前
// 才算动图（APNG 规范定义；避免 bytes.Contains 在压缩像素流上的字节巧合误报）。
func pngIsAnimated(data []byte) bool {
	const sigLen = 8
	if len(data) < sigLen+8 || data[0] != 0x89 || string(data[1:4]) != "PNG" {
		return false
	}
	off := sigLen
	// 有界遍历：acTL 规范上必须紧邻文件头部（IHDR 之后、IDAT 之前），64 个 chunk 足够
	for i := 0; i < 64 && off+8 <= len(data); i++ {
		length := int(uint32(data[off])<<24 | uint32(data[off+1])<<16 |
			uint32(data[off+2])<<8 | uint32(data[off+3]))
		typ := string(data[off+4 : off+8])
		switch typ {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		off += 8 + length + 4 // length + type + payload + CRC
	}
	return false
}

// webpIsAnimated 判断 WebP 是否为动图：动图必为 VP8X 扩展格式且 flags 的
// animation 位（0x02）置位；VP8/VP8L 简单格式必为静图，无需扫描 ANIM chunk。
func webpIsAnimated(data []byte) bool {
	if len(data) < 21 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	if string(data[12:16]) != "VP8X" {
		return false
	}
	return data[20]&0x02 != 0
}

// webpDimensions 从 VP8X chunk 解析画布宽高（24bit 小端存"值减一"）；
// 标准库无法解码 WebP，此为 Width/Height 的兜底来源。
func webpDimensions(data []byte) (width, height int, ok bool) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" ||
		string(data[12:16]) != "VP8X" {
		return 0, 0, false
	}
	width = int(data[24]) | int(data[25])<<8 | int(data[26])<<16
	height = int(data[27]) | int(data[28])<<8 | int(data[29])<<16
	return width + 1, height + 1, true
}

// releaseAsset 递减资产引用；归零时延迟 GC——仅置 0，不删行不删文件
//（与 sticker/assets.go 行为一致，物理清扫留给后续独立清扫器）。
func (h *api) releaseAsset(tx *gorm.DB, assetID int64) error {
	var asset model.CosmeticAsset
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&asset, "id = ?", assetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if asset.RefCount <= 1 {
		return tx.Model(&asset).Update("ref_count", 0).Error
	}
	return tx.Model(&asset).Update("ref_count", gorm.Expr("ref_count - 1")).Error
}

// storeAssetBytes 按 content_hash 去重写入资产，返回 asset 记录。
// 在调用方事务内执行（tx），与 item 引用变更保持原子。
func (h *api) storeAssetBytes(tx *gorm.DB, data []byte, contentType string, maxBytes int64) (*model.CosmeticAsset, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("空文件")
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("文件超过 %d 字节上限", maxBytes)
	}
	mime := sniffMediaMIME(data, contentType)
	ext, ok := allowedMIME[mime]
	if !ok {
		return nil, fmt.Errorf("不支持的媒体类型 %s", mime)
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	var existing model.CosmeticAsset
	err := tx.Where("content_hash = ?", hash).First(&existing).Error
	if err == nil {
		_ = tx.Model(&existing).UpdateColumn("ref_count", gorm.Expr("ref_count + 1")).Error
		existing.RefCount++
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	dir := cosmeticsDir(h.cfg().DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	filename := fmt.Sprintf("%s.%s", hash[:32], ext)
	target := filepath.Join(dir, filename)
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return nil, err
	}

	width, height := 0, 0
	if strings.HasPrefix(mime, "image/") {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			width, height = cfg.Width, cfg.Height
		} else if w, h2, ok := webpDimensions(data); ok {
			width, height = w, h2
		}
	}

	// MIME 折叠后动图身份靠内容嗅探补齐：APNG（acTL）与动态 WebP（VP8X animation 位）
	animated := isAnimatedMIME(mime) ||
		(mime == "image/png" && pngIsAnimated(data)) ||
		(mime == "image/webp" && webpIsAnimated(data))

	asset := model.CosmeticAsset{
		ID:          h.ids.Next(),
		ContentHash: hash,
		StorageKey:  filename,
		MIME:        mime,
		SizeBytes:   int64(len(data)),
		Width:       width,
		Height:      height,
		Animated:    animated,
		RefCount:    1,
		CreatedAt:   time.Now().UTC(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "content_hash"}},
		DoUpdates: clause.Assignments(map[string]any{"ref_count": gorm.Expr("cosmetic_assets.ref_count + 1")}),
	}).Create(&asset).Error; err != nil {
		// 冲突后重读
		if err2 := tx.Where("content_hash = ?", hash).First(&existing).Error; err2 == nil {
			return &existing, nil
		}
		_ = os.Remove(target)
		return nil, err
	}
	return &asset, nil
}

func assetPublicURL(a model.CosmeticAsset) string {
	return publicAssetURLPrefix + a.StorageKey
}

// serveAsset GET /public-assets/cosmetics/:name
func (h *api) serveAsset(c *gin.Context) {
	name := c.Param("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	contentTypes := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
		".mp4": "video/mp4", ".webm": "video/webm",
		".ogg": "audio/ogg", ".mp3": "audio/mpeg", ".wav": "audio/wav",
	}
	ct, ok := contentTypes[ext]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	target := filepath.Join(cosmeticsDir(h.cfg().DataDir), name)
	if _, err := os.Stat(target); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", ct)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.File(target)
}

// readBodyLimited 读取请求体并限制大小。
func readBodyLimited(c *gin.Context, max int64) ([]byte, error) {
	if max <= 0 {
		max = defaultMaxAssetBytes
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("FILE_TOO_LARGE")
	}
	return data, nil
}

func (h *api) loadAssetsByIDs(ids []int64) map[int64]model.CosmeticAsset {
	out := map[int64]model.CosmeticAsset{}
	if len(ids) == 0 {
		return out
	}
	var rows []model.CosmeticAsset
	_ = h.db().Where("id IN ?", ids).Find(&rows).Error
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}
