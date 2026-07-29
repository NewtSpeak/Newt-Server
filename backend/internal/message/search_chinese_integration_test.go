package message_test

// 中文搜索（bigram 辅助索引）集成测试：需要真实 PostgreSQL，
// 运行方式见 client_integration_test.go 头注释。
//
// 覆盖：
//  1. 中文多字词组命中（"世界" 命中 "你好，美丽的世界"）；
//  2. 非相邻多词查询（"你好 世界"）走 bigram AND 语义（ILIKE 子串兜底无法命中，
//     证明命中来自 bigram 索引路径）；
//  3. 存量回填 RebuildSearchBigrams：索引列清空后 bigram 查询失效，回填后恢复；
//  4. ACL 不受影响：非成员对同一查询零命中。

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/message"
)

// searchHits 执行一次搜索并返回命中数；429（限流）返回 -1 由调用方重试。
func searchHits(t *testing.T, router *gin.Engine, token, query string) int {
	t.Helper()
	path := "/gapi/v1/search/messages?q=" + url.QueryEscape(query)
	rec, body := doJSONReq(t, router, http.MethodGet, path, token, nil)
	if rec.Code == http.StatusTooManyRequests {
		return -1
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("搜索 %q 返回 %d: %s", query, rec.Code, rec.Body.String())
	}
	hits, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("搜索响应缺 messages: %s", rec.Body.String())
	}
	return len(hits)
}

// waitForHits 轮询直到命中数达到 want（索引异步，AU.6 秒级滞后可接受）。
func waitForHits(t *testing.T, router *gin.Engine, token, query string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := searchHits(t, router, token, query); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待搜索 %q 命中 %d 条超时", query, want)
		}
		time.Sleep(600 * time.Millisecond)
	}
}

func TestChineseSearchBigram(t *testing.T) {
	router, db, _ := newTextRouter(t)
	token, _, channelID := setupTextFixture(t, router, db)
	base := "/gapi/v1/channels/" + channelID.String()

	// 用随机后缀防与库中历史测试数据串扰（total/命中数按本频道 ACL 已隔离，
	// 但同库反复跑时保持正文唯一性更稳）。
	marker := fmt.Sprintf("标记%04x", rand.Uint32()&0xffff)
	content := "你好，美丽的世界 " + marker
	rec, _ := doJSONReq(t, router, http.MethodPost, base+"/messages", token, map[string]string{"content": content})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发中文消息返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 1. 词组子串（"世界"）即刻可命中（ILIKE 兜底），随后 bigram 索引就绪。
	waitForHits(t, router, token, "世界 "+marker, 1)

	// 2. 非相邻多词查询："你好 世界" 中间隔着"，美丽的"——ILIKE 兜底
	//    （%你好 世界%）不可能命中，命中只能来自 bigram AND 匹配。
	waitForHits(t, router, token, "你好 世界", 1)

	// 3. 存量回填：清空索引列模拟旧数据 → bigram 查询失效 → 回填后恢复。
	//    注：进程级启动回填（sync.Once）在本测试前几步的数秒轮询中早已跑完，
	//    此处手工清列不会被它抢先重填。
	if err := db.Exec(`UPDATE messages SET content_tsv = NULL, content_bigrams = NULL WHERE channel_id = ?`, channelID).Error; err != nil {
		t.Fatalf("清空索引列失败: %v", err)
	}
	waitUntil(t, "索引清空后 bigram 查询失效", func() bool {
		return searchHits(t, router, token, "你好 世界") == 0
	})
	if err := message.RebuildSearchBigrams(db); err != nil {
		t.Fatalf("存量回填失败: %v", err)
	}
	waitForHits(t, router, token, "你好 世界", 1)

	// 4. ACL：非成员同一查询零命中（可见频道集合为空）。
	strangerName := fmt.Sprintf("zh_s%07x", rand.Uint32())
	rec, stranger := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": strangerName, "email": strangerName + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册路人返回 %d: %s", rec.Code, rec.Body.String())
	}
	if got := searchHits(t, router, stranger["access_token"].(string), "你好 世界"); got != 0 {
		t.Fatalf("非成员搜索命中 %d 条，期待 0（ACL 泄漏）", got)
	}
}

// waitUntil 通用轮询（限流 -1 也会重试）。
func waitUntil(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %s", description)
		}
		time.Sleep(600 * time.Millisecond)
	}
}
