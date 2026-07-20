package message

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSniffVoicePackAudio 音频魔数嗅探：仅放行 OGG 与 MP3（ID3 标签 / 裸 MPEG 帧）。
func TestSniffVoicePackAudio(t *testing.T) {
	cases := []struct {
		name    string
		head    []byte
		wantExt string
		wantOK  bool
	}{
		{"OGG 容器", append([]byte("OggS"), make([]byte, 28)...), "ogg", true},
		{"MP3 带 ID3 标签", append([]byte("ID3"), make([]byte, 16)...), "mp3", true},
		{"裸 MP3 帧同步字 FFFB", []byte{0xFF, 0xFB, 0x90, 0x00}, "mp3", true},
		{"裸 MP3 帧同步字 FFE3", []byte{0xFF, 0xE3, 0x18, 0xC4}, "mp3", true},
		{"PNG 伪装", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, "", false},
		{"WAV 拒收", []byte("RIFF....WAVE"), "", false},
		{"文本拒收", []byte("hello world audio"), "", false},
		{"空内容", nil, "", false},
		{"0xFF 但同步位不足", []byte{0xFF, 0x1B, 0x00}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, _, ok := sniffVoicePackAudio(tc.head)
			if ok != tc.wantOK || ext != tc.wantExt {
				t.Fatalf("sniff=%q/%v，期望 %q/%v", ext, ok, tc.wantExt, tc.wantOK)
			}
		})
	}
}

// TestVoicePackCooldown 服务端频控：同 guild+user 60s 冷却，跨用户/跨服互不影响。
func TestVoicePackCooldown(t *testing.T) {
	cooldown := &voicePackCooldown{}
	guildA, guildB := uuid.New(), uuid.New()
	userA, userB := uuid.New(), uuid.New()
	base := time.Now()

	if !cooldown.allow(guildA, userA, base) {
		t.Fatal("首次触发应放行")
	}
	if cooldown.allow(guildA, userA, base.Add(30*time.Second)) {
		t.Fatal("冷却窗口内应拒绝")
	}
	if !cooldown.allow(guildA, userB, base.Add(time.Second)) {
		t.Fatal("同服不同用户不应互相牵连")
	}
	if !cooldown.allow(guildB, userA, base.Add(2*time.Second)) {
		t.Fatal("同用户不同服不应互相牵连")
	}
	if !cooldown.allow(guildA, userA, base.Add(voicePackCooldownWindow+time.Second)) {
		t.Fatal("冷却过期后应重新放行")
	}
}

// TestVoicePackRoutesDualPrefix 语音包完整模型端点双平面挂载冒烟：
// 两个前缀均存在（未认证 401 而非 404）。
func TestVoicePackRoutesDualPrefix(t *testing.T) {
	router := newDualPrefixRouter(t)
	guildID := uuid.NewString()
	packID := uuid.NewString()
	cases := []struct{ method, path string }{
		{http.MethodGet, "/guilds/" + guildID + "/voice-packs"},
		{http.MethodPost, "/guilds/" + guildID + "/voice-packs"},
		{http.MethodPatch, "/guilds/" + guildID + "/voice-packs/" + packID},
		{http.MethodDelete, "/guilds/" + guildID + "/voice-packs/" + packID},
		{http.MethodPost, "/guilds/" + guildID + "/voice-packs/" + packID + "/audio"},
		{http.MethodPut, "/guilds/" + guildID + "/voice-packs/" + packID + "/select"},
		{http.MethodGet, "/guilds/" + guildID + "/voice-packs/@me"},
		{http.MethodDelete, "/guilds/" + guildID + "/voice-packs/@me"},
	}
	for _, prefix := range []string{"/api/v1", "/gapi/v1"} {
		for _, tc := range cases {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, prefix+tc.path, bytes.NewReader(nil)))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s%s 未认证返回 %d，期待 401（路由应存在且受保护）", tc.method, prefix, tc.path, rec.Code)
			}
		}
	}
}
