package message

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testSecret = "unit-test-secret-at-least-32-chars!!"

// TestAttachmentSignature 签名 URL 校验：正确签名放行，过期/篡改/换 ID 拒绝（AT.7）。
func TestAttachmentSignature(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	exp := now.Add(10 * time.Minute).Unix()
	sig := signAttachment(testSecret, id, exp)

	if !verifyAttachmentSig(testSecret, id, exp, sig, now) {
		t.Fatal("有效签名应通过校验")
	}
	if verifyAttachmentSig(testSecret, id, exp, sig, now.Add(11*time.Minute)) {
		t.Error("过期签名应被拒绝")
	}
	if verifyAttachmentSig(testSecret, id, exp, sig+"00", now) {
		t.Error("篡改签名应被拒绝")
	}
	if verifyAttachmentSig(testSecret, uuid.New(), exp, sig, now) {
		t.Error("签名不应对其他附件 ID 生效")
	}
	if verifyAttachmentSig(testSecret, id, exp+1, sig, now) {
		t.Error("篡改过期时间应被拒绝")
	}
	if verifyAttachmentSig("another-secret-with-32-characters!!!", id, exp, sig, now) {
		t.Error("不同密钥签名应被拒绝")
	}
}

// TestBuildDownloadURL 生成的下载 URL 自洽：解析出的 exp/sig 能通过校验。
func TestBuildDownloadURL(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	url := buildDownloadURL("/api/v1", testSecret, id, now, false)
	prefix := "/api/v1/attachments/" + id.String() + "?exp="
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("下载 URL 前缀不符：%s", url)
	}
	var exp int64
	var sig string
	if _, err := fmt.Sscanf(strings.TrimPrefix(url, prefix), "%d&sig=%s", &exp, &sig); err != nil {
		t.Fatalf("解析下载 URL 失败：%v", err)
	}
	if !verifyAttachmentSig(testSecret, id, exp, sig, now) {
		t.Fatal("生成的下载 URL 未通过自校验")
	}
	if exp != now.Add(downloadURLTTL).Unix() {
		t.Errorf("过期时间不符：%d", exp)
	}
	// 限定可见消息附件使用更短 TTL
	restrictedURL := buildDownloadURL("/api/v1", testSecret, id, now, true)
	var rexp int64
	var rsig string
	if _, err := fmt.Sscanf(strings.TrimPrefix(restrictedURL, prefix), "%d&sig=%s", &rexp, &rsig); err != nil {
		t.Fatalf("解析限定下载 URL 失败：%v", err)
	}
	if rexp != now.Add(restrictedDownloadURLTTL).Unix() {
		t.Errorf("限定可见附件 TTL 不符：%d", rexp)
	}
}

// TestClientURLPrefixIsolation 用户端（/gapi/v1）上下文中生成的上传/下载 URL
// 必须以用户端前缀开头，且绝不能出现后台前缀 /api/v1（本专项安全要求：
// 防止用户端流量推断后台地址）。
func TestClientURLPrefixIsolation(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	urls := []string{
		buildDownloadURL("/gapi/v1", testSecret, id, now, false),
		buildUploadURL("/gapi/v1", id, "token-placeholder"),
	}
	for _, url := range urls {
		if !strings.HasPrefix(url, "/gapi/v1/attachments/") {
			t.Errorf("用户端 URL 前缀不符：%s", url)
		}
		if strings.Contains(url, "/api/v1") {
			t.Errorf("用户端 URL 泄露后台前缀：%s", url)
		}
	}
	// 后台上下文仍生成后台前缀，行为不变。
	if url := buildUploadURL("/api/v1", id, "t"); !strings.HasPrefix(url, "/api/v1/attachments/") {
		t.Errorf("后台 URL 前缀不符：%s", url)
	}
}

// TestUploadToken 上传令牌：明文与哈希对应、每次生成互不相同。
func TestUploadToken(t *testing.T) {
	token, tokenHash, err := newUploadToken()
	if err != nil {
		t.Fatalf("生成上传令牌失败：%v", err)
	}
	if hashUploadToken(token) != tokenHash {
		t.Fatal("令牌哈希不匹配")
	}
	if hashUploadToken("wrong") == tokenHash {
		t.Fatal("错误令牌不应匹配")
	}
	token2, _, _ := newUploadToken()
	if token == token2 {
		t.Fatal("两次生成的令牌不应相同")
	}
}
