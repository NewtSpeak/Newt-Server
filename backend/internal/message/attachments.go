package message

import (
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // 图片尺寸探测解码器
	_ "image/jpeg" // 同上
	_ "image/png"  // 同上
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// 附件上传/下载（docs 13 AT）。本地存储实现下，presign 返回本服务自身的
// PUT /api/v1/attachments/{id}/content 地址 + 一次性 upload token；
// 切换对象存储时改为返回真正的预签名地址即可，客户端流程不变。

// uploadTokenTTL presign 签发的上传令牌有效期。
const uploadTokenTTL = time.Hour

type presignRequest struct {
	Filename string `json:"filename" binding:"required,max=255"`
	Size     int64  `json:"size" binding:"required"`
	MIME     string `json:"mime" binding:"max=255"`
}

// presignAttachment POST /channels/{id}/attachments/presign（AT 上传时序步骤 1–2）。
// 需 ATTACH_FILES；校验大小 ≤ 服级上限（默认 25MB）。
func (s *service) presignAttachment(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	ctx, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	if !rbac.Has(bits, rbac.AttachFiles) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少上传附件权限")
		return
	}
	var input presignRequest
	if !bind(c, &input) {
		return
	}
	if input.Size <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_SIZE", "size 需为正整数")
		return
	}
	// DM 走平台默认限额（Server-16 BN.3），服频道吃服级上限。
	guildID := uuid.Nil
	if !channel.Type.IsPrivate() {
		guildID = ctx.Guild.ID
	}
	limit := s.uploadLimitBytes(guildID)
	if input.Size > limit {
		fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("附件大小超过上限 %d 字节", limit))
		return
	}
	mime := strings.TrimSpace(input.MIME)
	if mime == "" {
		mime = "application/octet-stream"
	}
	token, tokenHash, err := newUploadToken()
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "生成上传令牌失败")
		return
	}
	user := s.currentUser(c)
	now := time.Now().UTC()
	attachment := model.Attachment{
		ID:              uuid.New(),
		GuildID:         guildID,
		ChannelID:       channel.ID,
		UploaderID:      user.ID,
		Filename:        strings.TrimSpace(input.Filename),
		MIME:            mime,
		Size:            input.Size,
		Uploaded:        false,
		UploadTokenHash: tokenHash,
		UploadExpiresAt: now.Add(uploadTokenTTL),
		CreatedAt:       now,
	}
	attachment.ObjectKey = attachment.ID.String()
	if err := s.db.Create(&attachment).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建附件记录失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"attachment_id": attachment.ID,
		"upload_url":    buildUploadURL(s.urlPrefix, attachment.ID, token),
		"expires_at":    attachment.UploadExpiresAt,
		"preview":       previewKind(mime),
	})
}

// uploadAttachmentContent PUT /api/v1/attachments/{id}/content?token=（上传时序步骤 3）。
// 校验上传者身份、一次性令牌与声明大小；实际字节数必须与 presign 声明完全一致。
func (s *service) uploadAttachmentContent(c *gin.Context) {
	attachmentID, ok := parseUUIDParam(c, "attachmentID")
	if !ok {
		return
	}
	var attachment model.Attachment
	if err := s.db.First(&attachment, "id = ?", attachmentID).Error; err != nil {
		notFound(c)
		return
	}
	user := s.currentUser(c)
	if attachment.UploaderID != user.ID {
		notFound(c)
		return
	}
	if attachment.Uploaded {
		fail(c, http.StatusConflict, "ALREADY_UPLOADED", "附件内容已上传")
		return
	}
	now := time.Now().UTC()
	token := c.Query("token")
	if token == "" || attachment.UploadTokenHash == "" ||
		hashUploadToken(token) != attachment.UploadTokenHash || now.After(attachment.UploadExpiresAt) {
		fail(c, http.StatusForbidden, "INVALID_UPLOAD_TOKEN", "上传令牌无效或已过期")
		return
	}
	written, err := s.storage.Save(attachment.ObjectKey, c.Request.Body, attachment.Size)
	if err != nil {
		if errors.Is(err, errObjectTooLarge) {
			fail(c, http.StatusBadRequest, "SIZE_MISMATCH", "上传内容超过声明大小")
			return
		}
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入附件失败")
		return
	}
	if written != attachment.Size {
		_ = s.storage.Delete(attachment.ObjectKey)
		fail(c, http.StatusBadRequest, "SIZE_MISMATCH", "上传内容与声明大小不一致")
		return
	}
	// 图片尺寸探测（docs 07 §8-5：客户端占位比例）：仅 image/*，失败静默跳过。
	width, height := 0, 0
	if strings.HasPrefix(attachment.MIME, "image/") {
		if reader, err := s.storage.Open(attachment.ObjectKey); err == nil {
			if config, _, err := image.DecodeConfig(reader); err == nil {
				width, height = config.Width, config.Height
			}
			_ = reader.Close()
		}
	}
	// 令牌一次性：成功后立即作废。
	err = s.db.Model(&model.Attachment{}).Where("id = ? AND uploaded = false", attachment.ID).
		Updates(map[string]any{"uploaded": true, "upload_token_hash": "", "width": width, "height": height}).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新附件状态失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"attachment_id": attachment.ID, "size": written, "uploaded": true, "width": width, "height": height})
}

// downloadAttachment GET /api/v1/attachments/{id}?sig=&exp=（AT.7）。
// 无需登录态：短时签名即访问凭证；签名只在通过频道可见性检查的消息响应中签发。
func (s *service) downloadAttachment(c *gin.Context) {
	attachmentID, ok := parseUUIDParam(c, "attachmentID")
	if !ok {
		return
	}
	exp, err := strconv.ParseInt(c.Query("exp"), 10, 64)
	if err != nil || !verifyAttachmentSig(s.cfg.JWTSecret, attachmentID, exp, c.Query("sig"), time.Now().UTC()) {
		notFound(c)
		return
	}
	var attachment model.Attachment
	if err := s.db.First(&attachment, "id = ? AND uploaded = true", attachmentID).Error; err != nil {
		notFound(c)
		return
	}
	reader, err := s.storage.Open(attachment.ObjectKey)
	if err != nil {
		notFound(c)
		return
	}
	defer reader.Close()
	disposition := "attachment"
	if previewKind(attachment.MIME) != "" {
		disposition = "inline"
	}
	c.Header("Content-Type", attachment.MIME)
	c.Header("Content-Disposition", fmt.Sprintf(`%s; filename*=UTF-8''%s`, disposition, url.PathEscape(attachment.Filename)))
	// http.ServeContent 提供 Range 支持（音视频拖动进度需要）。
	http.ServeContent(c.Writer, c.Request, attachment.Filename, attachment.CreatedAt, reader)
}
