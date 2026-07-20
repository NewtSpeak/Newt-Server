package sfunode

import (
	"testing"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

func makeNodes(n int) []model.SfuNode {
	nodes := make([]model.SfuNode, n)
	for i := range nodes {
		nodes[i] = model.SfuNode{ID: uuid.New(), Status: model.SfuNodeOnline}
	}
	return nodes
}

// TestResolvePool 节点池过滤：勾选优先，空池按开关回落平台默认池（docs 07 专项 2.2）。
func TestResolvePool(t *testing.T) {
	selected := makeNodes(2)
	defaults := makeNodes(3)

	t.Run("有勾选节点时只用勾选集", func(t *testing.T) {
		got := ResolvePool(selected, defaults, true)
		if len(got) != 2 || got[0].ID != selected[0].ID {
			t.Fatalf("应返回勾选集，实际 %d 个", len(got))
		}
	})

	t.Run("空池且回落开启时用平台默认池", func(t *testing.T) {
		got := ResolvePool(nil, defaults, true)
		if len(got) != 3 {
			t.Fatalf("应回落平台默认池，实际 %d 个", len(got))
		}
	})

	t.Run("空池且回落关闭时返回空", func(t *testing.T) {
		if got := ResolvePool(nil, defaults, false); len(got) != 0 {
			t.Fatalf("回落关闭时应返回空，实际 %d 个", len(got))
		}
	})

	t.Run("有勾选时回落开关不影响结果", func(t *testing.T) {
		if got := ResolvePool(selected, defaults, false); len(got) != 2 {
			t.Fatalf("勾选集不受回落开关影响，实际 %d 个", len(got))
		}
	})
}

// TestNodeInfoFromModel DB 记录 → 调度 NodeInfo 的转换。
func TestNodeInfoFromModel(t *testing.T) {
	node := model.SfuNode{
		ID:                   uuid.New(),
		DisplayName:          "东京-1",
		Status:               model.SfuNodeDraining,
		Labels:               model.SfuLabelMap{"region": "ap-tokyo", "network": "public"},
		WebRTCHosts:          model.SfuStringList{"1.2.3.4:50000", "backup:50001"},
		MaxUsers:             1000,
		CurrentUsers:         77,
		EnabledForScheduling: true,
	}
	info := NodeInfoFromModel(node)
	if info.Region != "ap-tokyo" {
		t.Fatalf("Region 应取自 labels，实际 %q", info.Region)
	}
	if !info.Draining || info.Online {
		t.Fatalf("DRAINING 节点：Draining=true Online=false，实际 %+v", info)
	}
	if info.WebRTCEndpoint != "1.2.3.4:50000" {
		t.Fatalf("WebRTCEndpoint 应取第一个 host，实际 %q", info.WebRTCEndpoint)
	}
	if !info.EnabledForScheduling || info.MaxUsers != 1000 || info.CurrentUsers != 77 {
		t.Fatalf("容量字段透传失败: %+v", info)
	}

	node.Status = model.SfuNodeOnline
	if info := NodeInfoFromModel(node); !info.Online || info.Draining {
		t.Fatalf("ONLINE 节点：Online=true Draining=false，实际 %+v", info)
	}
}

// TestControlHost enroll 响应中控制通道地址推导。
func TestControlHost(t *testing.T) {
	cases := []struct{ listen, requestHost, want string }{
		{":8443", "api.example.com:8080", "api.example.com:8443"},
		{":8443", "api.example.com", "api.example.com:8443"},
		{"0.0.0.0:8443", "api.example.com:8080", "api.example.com:8443"},
		{"10.0.0.5:8443", "api.example.com:8080", "10.0.0.5:8443"},
	}
	for _, tc := range cases {
		if got := controlHost(tc.listen, tc.requestHost); got != tc.want {
			t.Errorf("controlHost(%q, %q) = %q, 期望 %q", tc.listen, tc.requestHost, got, tc.want)
		}
	}
}
