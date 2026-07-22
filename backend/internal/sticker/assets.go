package sticker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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

// 允许的 MIME（docs 17 §9 建议）。
var allowedMIME = map[string]string{
	"image/png":  "png",
	"image/webp": "webp",
	"image/gif":  "gif",
	"image/jpeg": "jpg",
}

func stickersDir(dataDir string) string {
	return filepath.Join(dataDir, "stickers")
}

// ensureAsset 按内容 hash 去重写入 sticker_assets；命中则 ref_count++ 并返回已有行。
func (h *api) ensureAsset(tx *gorm.DB, data []byte, mime string) (model.StickerAsset, error) {
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	ext, ok := allowedMIME[mime]
	if !ok {
		return model.StickerAsset{}, errUnsupportedMIME
	}
	if int64(len(data)) == 0 || int64(len(data)) > defaultMaxFileBytes {
		return model.StickerAsset{}, errFileTooLarge
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	mark := markPrefix + hash[:markHashLen]

	var existing model.StickerAsset
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("content_hash = ?", hash).First(&existing).Error
	if err == nil {
		if err := tx.Model(&existing).Update("ref_count", gorm.Expr("ref_count + 1")).Error; err != nil {
			return model.StickerAsset{}, err
		}
		existing.RefCount++
		_ = mark // mark 由 item 层用同一 hash 生成
		return existing, nil
	}
	if err != gorm.ErrRecordNotFound {
		return model.StickerAsset{}, err
	}

	width, height, animated := probeImage(data, mime)
	if width <= 0 || height <= 0 {
		return model.StickerAsset{}, errInvalidImage
	}

	dir := stickersDir(h.cfg().DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.StickerAsset{}, err
	}
	// 文件名 = hash.ext，天然去重；并发同 hash 后写覆盖内容一致可接受。
	filename := hash + "." + ext
	target := filepath.Join(dir, filename)
	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		if writeErr := os.WriteFile(target, data, 0o644); writeErr != nil {
			return model.StickerAsset{}, writeErr
		}
	}

	asset := model.StickerAsset{
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
	if err := tx.Create(&asset).Error; err != nil {
		// 并发创建同 hash：回退读已有行并 +ref
		var race model.StickerAsset
		if tx.Where("content_hash = ?", hash).First(&race).Error == nil {
			_ = tx.Model(&race).Update("ref_count", gorm.Expr("ref_count + 1")).Error
			race.RefCount++
			return race, nil
		}
		return model.StickerAsset{}, err
	}
	return asset, nil
}

func (h *api) releaseAsset(tx *gorm.DB, assetID int64) error {
	var asset model.StickerAsset
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&asset, "id = ?", assetID).Error; err != nil {
		return err
	}
	if asset.RefCount <= 1 {
		// 延迟 GC：仅把 ref_count 置 0，不立刻删 blob（消息/反应快照可能仍依赖）。
		return tx.Model(&asset).Update("ref_count", 0).Error
	}
	return tx.Model(&asset).Update("ref_count", gorm.Expr("ref_count - 1")).Error
}

func markFromHash(contentHash string) string {
	if len(contentHash) < markHashLen {
		return markPrefix + contentHash
	}
	return markPrefix + contentHash[:markHashLen]
}

func probeImage(data []byte, mime string) (width, height int, animated bool) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// 标准库可能无法解码 WebP；用 RIFF/WEBP 头做最小校验并放宽尺寸检查
		if mime == "image/webp" && len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
			animated = bytes.Contains(data, []byte("ANIM"))
			// 尺寸未知时用占位 1×1，避免拒收；客户端可按实际显示
			return 1, 1, animated
		}
		if mime == "image/gif" {
			animated = true
		}
		return 0, 0, animated
	}
	width, height = cfg.Width, cfg.Height
	if format == "gif" || mime == "image/gif" {
		animated = true
	}
	if mime == "image/png" && bytes.Contains(data, []byte("acTL")) {
		animated = true
	}
	if mime == "image/webp" && bytes.Contains(data, []byte("ANIM")) {
		animated = true
	}
	return width, height, animated
}

// serveAsset GET /public-assets/stickers/:name
func (h *api) serveAsset(c *gin.Context) {
	name := filepath.Base(c.Param("name"))
	if name == "" || name == "." || strings.Contains(name, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	// 仅允许 hash.ext 形态
	if !validAssetName(name) {
		c.Status(http.StatusNotFound)
		return
	}
	path := filepath.Join(stickersDir(h.cfg().DataDir), name)
	f, err := os.Open(path)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	mime := mimeFromName(name)
	c.Header("Content-Type", mime)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Length", fmt.Sprintf("%d", stat.Size()))
	http.ServeContent(c.Writer, c.Request, name, stat.ModTime(), f)
}

func validAssetName(name string) bool {
	// sha256 hex (64) + . + ext
	dot := strings.LastIndexByte(name, '.')
	if dot != 64 {
		return false
	}
	hash, ext := name[:dot], name[dot+1:]
	if _, ok := map[string]bool{"png": true, "webp": true, "gif": true, "jpg": true}[ext]; !ok {
		return false
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func mimeFromName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "application/octet-stream"
	}
}

// loadAssetMap 批量查 asset_id → storage_key。
func (h *api) loadAssetMap(assetIDs []int64) map[int64]string {
	out := map[int64]string{}
	if len(assetIDs) == 0 {
		return out
	}
	var assets []model.StickerAsset
	_ = h.db().Select("id", "storage_key").Where("id IN ?", assetIDs).Find(&assets).Error
	for _, a := range assets {
		out[a.ID] = a.StorageKey
	}
	return out
}

func (h *api) storageKeyOf(assetID int64) string {
	var a model.StickerAsset
	if h.db().Select("storage_key").First(&a, "id = ?", assetID).Error != nil {
		return ""
	}
	return a.StorageKey
}
