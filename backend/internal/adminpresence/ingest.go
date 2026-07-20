package adminpresence

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// maxAuditUploadBytes 单段审计录音上限（防异常大文件；Opus 长语音也够用）。
const maxAuditUploadBytes = int64(256 << 20) // 256 MiB

// auditDir 审计录音在主节点的落盘目录。
func (h *api) auditDir() string { return filepath.Join(h.deps.Cfg.DataDir, "audit") }

// ingestRecord POST /audit-api/records：SFU 节点上传一段审计录音（原始 .ogg 字节）。
// 认证：Authorization: Bearer <AUDIT_INGEST_TOKEN>（与 SFU 侧配置共享）。
// 元数据经 query 传入：guild_id/channel_id/user_id/session_id/node_id/started/ended（unix 秒）。
func (h *api) ingestRecord(c *gin.Context) {
	token := h.deps.Cfg.AuditIngestToken
	if token == "" {
		fail(c, http.StatusServiceUnavailable, "AUDIT_DISABLED", "审计上传未启用（未配置 AUDIT_INGEST_TOKEN）")
		return
	}
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || strings.TrimPrefix(header, "Bearer ") != token {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "审计上传凭证无效")
		return
	}
	guildID, err1 := uuid.Parse(c.Query("guild_id"))
	channelID, err2 := uuid.Parse(c.Query("channel_id"))
	userID, err3 := uuid.Parse(c.Query("user_id"))
	if err1 != nil || err2 != nil || err3 != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "guild_id/channel_id/user_id 非法")
		return
	}
	recordID := uuid.New()
	dir := h.auditDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建审计目录失败")
		return
	}
	objectKey := recordID.String() + ".ogg"
	target := filepath.Join(dir, objectKey)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入录音失败")
		return
	}
	written, err := io.Copy(file, io.LimitReader(c.Request.Body, maxAuditUploadBytes+1))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || written > maxAuditUploadBytes {
		_ = os.Remove(target)
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "录音内容为空或超限")
		return
	}
	record := model.AudioAuditRecord{
		ID: recordID, GuildID: guildID, ChannelID: channelID, UserID: userID,
		SessionID: c.Query("session_id"), NodeID: c.Query("node_id"),
		ObjectKey: objectKey, MIME: "audio/ogg", Size: written,
		StartedAt: unixQuery(c, "started"), EndedAt: unixQuery(c, "ended"),
	}
	if err := h.deps.DB.Create(&record).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存录音元数据失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": record.ID, "size": written})
}

// downloadAuditRecord GET /admin/audit-records/{id}/audio：系统管理员下载录音。
func (h *api) downloadAuditRecord(c *gin.Context) {
	if _, ok := h.requireSystemAdmin(c); !ok {
		return
	}
	id, ok := parseUUID(c, "recordID")
	if !ok {
		return
	}
	var record model.AudioAuditRecord
	if err := h.deps.DB.First(&record, "id = ?", id).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "录音不存在")
		return
	}
	target := filepath.Join(h.auditDir(), record.ObjectKey)
	if _, err := os.Stat(target); err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "录音文件不存在")
		return
	}
	c.Header("Content-Type", record.MIME)
	c.Header("Content-Disposition", "attachment; filename=\""+record.ID.String()+".ogg\"")
	c.File(target)
}

// unixQuery 解析 unix 秒查询参数为 UTC 时间（缺省/非法回落当前时刻）。
func unixQuery(c *gin.Context, name string) time.Time {
	raw := c.Query(name)
	if raw == "" {
		return time.Now().UTC()
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Now().UTC()
	}
	return time.Unix(seconds, 0).UTC()
}
