package voice

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

// TestRTTProbeURL advertise_wss_url → RTT 探测地址推导。
func TestRTTProbeURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"wss://sfu1.example.com/ws", "https://sfu1.example.com/rtt"},
		{"wss://sfu1.example.com:8443/ws", "https://sfu1.example.com:8443/rtt"},
		{"ws://127.0.0.1:7880/ws", "http://127.0.0.1:7880/rtt"},
		{"wss://sfu.example.com", "https://sfu.example.com/rtt"},
		{"", ""},
		{"not a url\x7f", ""},
		{"/relative/path", ""},
	}
	for _, tc := range cases {
		if got := rttProbeURL(tc.in); got != tc.want {
			t.Errorf("rttProbeURL(%q)=%q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestVoiceNodeViewsFilterAndNoLeak 节点池视图：只下发 ONLINE 节点，
// 且序列化后不泄露容量/负载/调度开关等内部字段（docs 10 X.3）。
func TestVoiceNodeViewsFilterAndNoLeak(t *testing.T) {
	online := sfuctl.NodeInfo{
		ID: uuid.New(), Region: "ap-southeast", Status: model.SfuNodeOnline, Online: true,
		EnabledForScheduling: true, MaxUsers: 500, CurrentUsers: 123,
		CPUPercent: 42.5, MemPercent: 61.2, BandwidthOutMbps: 800, ScreenTracks: 3,
		WebRTCEndpoint: "wss://sfu-online.example.com/ws",
		Labels:         map[string]string{"pool": "secret-pool"},
		LastSeenAt:     time.Now(),
	}
	offline := online
	offline.ID = uuid.New()
	offline.Online = false
	draining := online
	draining.ID = uuid.New()
	draining.Status = model.SfuNodeDraining
	draining.Draining = true
	noEndpoint := online
	noEndpoint.ID = uuid.New()
	noEndpoint.WebRTCEndpoint = ""

	views := voiceNodeViews([]sfuctl.NodeInfo{online, offline, draining, noEndpoint})
	if len(views) != 1 || views[0].NodeID != online.ID {
		t.Fatalf("应只下发 ONLINE 且有端点的节点，got %+v", views)
	}
	if views[0].RTTProbeURL != "https://sfu-online.example.com/rtt" || views[0].Region != "ap-southeast" {
		t.Fatalf("视图字段异常: %+v", views[0])
	}

	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	serialized := string(raw)
	for _, field := range []string{"node_id", "rtt_probe_url", "region"} {
		if !strings.Contains(serialized, `"`+field+`"`) {
			t.Errorf("视图缺少字段 %s: %s", field, serialized)
		}
	}
	// 内部字段与敏感值绝不出现在响应里。
	for _, leak := range []string{
		"max_users", "current_users", "cpu", "mem", "bandwidth", "screen_tracks",
		"enabled_for_scheduling", "labels", "last_seen", "webrtc_endpoint", "cascade",
		"42.5", "61.2", "secret-pool",
	} {
		if strings.Contains(serialized, leak) {
			t.Errorf("响应泄露内部信息 %q: %s", leak, serialized)
		}
	}
}
