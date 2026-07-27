package message

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// 附件下载短时签名 URL（docs 13 AT.7）。
// 频道 VIEW_CHANNEL 校验发生在「签发时」：download_url 只出现在消息响应的附件元数据里，
// 能拿到消息响应即已通过频道可见性检查；下载端点仅验签名与过期，便于 <img>/<video> 等
// 无鉴权头的场景直接取用。密钥复用 cfg.JWTSecret。

// downloadURLTTL 公开消息附件签名有效期。
const downloadURLTTL = 15 * time.Minute

// restrictedDownloadURLTTL 限定可见消息附件更短 TTL，降低 URL 外泄窗口。
const restrictedDownloadURLTTL = 5 * time.Minute

// signAttachment 计算附件下载签名：HMAC-SHA256(secret, "attachment:{id}:{exp}") 的十六进制。
func signAttachment(secret string, attachmentID uuid.UUID, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "attachment:%s:%d", attachmentID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyAttachmentSig 校验签名与过期时间（常数时间比较防侧信道）。
func verifyAttachmentSig(secret string, attachmentID uuid.UUID, exp int64, sig string, now time.Time) bool {
	if exp <= now.Unix() {
		return false
	}
	expected := signAttachment(secret, attachmentID, exp)
	return hmac.Equal([]byte(expected), []byte(sig))
}

// buildDownloadURL 生成消息响应中的附件下载相对路径。
// prefix 为挂载前缀（后台 /api/v1、用户端 /gapi/v1）：URL 必须与请求所在认证平面
// 一致，用户端响应中绝不能出现后台前缀（防止用户端流量推断后台地址）。
// restricted=true 时使用更短 TTL（限定可见消息仅向授权者签发 URL）。
func buildDownloadURL(prefix, secret string, attachmentID uuid.UUID, now time.Time, restricted bool) string {
	ttl := downloadURLTTL
	if restricted {
		ttl = restrictedDownloadURLTTL
	}
	exp := now.Add(ttl).Unix()
	sig := signAttachment(secret, attachmentID, exp)
	return prefix + "/attachments/" + attachmentID.String() + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig
}

// buildUploadURL 生成 presign 响应中的附件直传相对路径（前缀语义同 buildDownloadURL）。
func buildUploadURL(prefix string, attachmentID uuid.UUID, token string) string {
	return prefix + "/attachments/" + attachmentID.String() + "/content?token=" + token
}

// newUploadToken 生成一次性上传令牌，返回（明文, SHA-256 十六进制）。
// 明文随 upload_url 交给客户端，库中只存哈希。
func newUploadToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

// hashUploadToken 上传时对客户端提交的令牌求哈希以便比对。
func hashUploadToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
