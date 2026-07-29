package sticker

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
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
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 允许的 MIME（docs 17 §9：图片 + 短视频；每服务器实例共用此白名单）。
var allowedMIME = map[string]string{
	"image/png":       "png",
	"image/webp":      "webp",
	"image/gif":       "gif",
	"image/jpeg":      "jpg",
	"image/jpg":       "jpg",
	"image/apng":      "png",
	"video/mp4":       "mp4",
	"video/webm":      "webm",
	"video/quicktime": "mov",
	// 部分浏览器对 mp4 上报的 sniff 结果
	"application/mp4": "mp4",
}

var allowedExt = map[string]bool{
	"png": true, "webp": true, "gif": true, "jpg": true,
	"mp4": true, "webm": true, "mov": true,
}

func stickersDir(dataDir string) string {
	return filepath.Join(dataDir, "stickers")
}

// normalizeMIME 去掉参数并小写；对常见别名做归一。
func normalizeMIME(raw string) string {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	switch mime {
	case "image/jpg":
		return "image/jpeg"
	case "image/apng":
		return "image/png"
	case "application/mp4":
		return "video/mp4"
	default:
		return mime
	}
}

// sniffMediaMIME 在 Content-Type 不可靠时按魔数补全。
func sniffMediaMIME(data []byte, hinted string) string {
	hinted = normalizeMIME(hinted)
	if _, ok := allowedMIME[hinted]; ok {
		// 仍用 sniff 校正错误标注的 mp4/webm
		if !strings.HasPrefix(hinted, "video/") {
			return hinted
		}
	}
	if len(data) >= 12 {
		// ISO BMFF: ....ftyp
		if string(data[4:8]) == "ftyp" {
			return "video/mp4"
		}
		// WebM / Matroska EBML
		if data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3 {
			return "video/webm"
		}
		// RIFF....WEBP
		if string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
			return "image/webp"
		}
		// GIF
		if string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a" {
			return "image/gif"
		}
		// PNG
		if len(data) >= 8 && data[0] == 0x89 && string(data[1:4]) == "PNG" {
			return "image/png"
		}
		// JPEG
		if data[0] == 0xFF && data[1] == 0xD8 {
			return "image/jpeg"
		}
	}
	detected := normalizeMIME(http.DetectContentType(data))
	if _, ok := allowedMIME[detected]; ok {
		return detected
	}
	if hinted != "" {
		return hinted
	}
	return detected
}

// ensureAsset 按内容 hash 去重写入 sticker_assets；命中则 ref_count++ 并返回已有行。
func (h *api) ensureAsset(tx *gorm.DB, data []byte, mime string) (model.StickerAsset, error) {
	mime = sniffMediaMIME(data, mime)
	ext, ok := allowedMIME[mime]
	if !ok {
		return model.StickerAsset{}, errUnsupportedMIME
	}
	if int64(len(data)) == 0 {
		return model.StickerAsset{}, errInvalidImage
	}
	if h.fileExceedsLimit(int64(len(data))) {
		return model.StickerAsset{}, errFileTooLarge
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	var existing model.StickerAsset
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("content_hash = ?", hash).First(&existing).Error
	if err == nil {
		if err := tx.Model(&existing).Update("ref_count", gorm.Expr("ref_count + 1")).Error; err != nil {
			return model.StickerAsset{}, err
		}
		existing.RefCount++
		return existing, nil
	}
	if err != gorm.ErrRecordNotFound {
		return model.StickerAsset{}, err
	}

	width, height, animated := probeMedia(data, mime)
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

// probeMedia 解析宽高与是否动画（图片 / 短视频）。
func probeMedia(data []byte, mime string) (width, height int, animated bool) {
	if strings.HasPrefix(mime, "video/") {
		animated = true
		if w, h, ok := probeMP4Size(data); ok {
			return w, h, true
		}
		// 无法解析时用占位尺寸，避免拒收合法视频
		return 1, 1, true
	}
	return probeImage(data, mime)
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

// probeMP4Size 粗解析 ISO BMFF 中 tkhd 的宽高（定点数 16.16）。
// 失败返回 ok=false；调用方可用占位。
func probeMP4Size(data []byte) (width, height int, ok bool) {
	if len(data) < 32 || string(data[4:8]) != "ftyp" {
		return 0, 0, false
	}
	// 线性扫描 box，找 tkhd（可能在 moov/trak 内；递归跳过容器）
	type box struct{ start, end int }
	stack := []box{{0, len(data)}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		off := cur.start
		for off+8 <= cur.end {
			if off+8 > len(data) {
				break
			}
			size := int(binary.BigEndian.Uint32(data[off : off+4]))
			typ := string(data[off+4 : off+8])
			header := 8
			if size == 1 {
				if off+16 > len(data) {
					break
				}
				size64 := binary.BigEndian.Uint64(data[off+8 : off+16])
				if size64 > uint64(^uint(0)>>1) {
					break
				}
				size = int(size64)
				header = 16
			} else if size == 0 {
				size = cur.end - off
			}
			if size < header || off+size > cur.end {
				break
			}
			payloadStart := off + header
			payloadEnd := off + size
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl", "edts":
				stack = append(stack, box{payloadStart, payloadEnd})
			case "tkhd":
				// version(1) + flags(3) + ... 宽高在末尾 8 字节之前的固定偏移
				// v0: 84 bytes min；v1: 96 bytes min
				if payloadEnd-payloadStart < 4 {
					break
				}
				ver := data[payloadStart]
				var whOff int
				if ver == 1 {
					whOff = payloadStart + 4 + 8 + 8 + 4 + 4 + 8 + 4*2 + 2*2 + 2*2 + 4*9
				} else {
					// v0
					whOff = payloadStart + 4 + 4 + 4 + 4 + 4 + 4 + 4*2 + 2*2 + 2*2 + 4*9
				}
				if whOff+8 <= payloadEnd {
					wFixed := binary.BigEndian.Uint32(data[whOff : whOff+4])
					hFixed := binary.BigEndian.Uint32(data[whOff+4 : whOff+8])
					w := int(wFixed >> 16)
					h := int(hFixed >> 16)
					if w > 0 && h > 0 {
						return w, h, true
					}
				}
			}
			off += size
		}
	}
	return 0, 0, false
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
	if !allowedExt[ext] {
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
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
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
