package voice

import (
	"testing"

	"github.com/google/uuid"
)

// TestElectAnchor 选举规则表驱动：人最多 → 负载低 → region 偏好 → node_id 字典序（docs 08 B.2）。
func TestElectAnchor(t *testing.T) {
	n1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	n2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	n3 := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	cases := []struct {
		name       string
		candidates []anchorCandidate
		prefer     string
		want       uuid.UUID
		ok         bool
	}{
		{
			name: "人最多者当选",
			candidates: []anchorCandidate{
				{NodeID: n1, RoomUsers: 3, CPUPercent: 10},
				{NodeID: n2, RoomUsers: 10, CPUPercent: 90},
			},
			want: n2, ok: true,
		},
		{
			name: "并列时负载低者当选",
			candidates: []anchorCandidate{
				{NodeID: n1, RoomUsers: 5, CPUPercent: 80},
				{NodeID: n2, RoomUsers: 5, CPUPercent: 20},
			},
			want: n2, ok: true,
		},
		{
			name: "人数负载都并列时 region 偏好",
			candidates: []anchorCandidate{
				{NodeID: n1, RoomUsers: 5, CPUPercent: 20, Region: "eu-west"},
				{NodeID: n2, RoomUsers: 5, CPUPercent: 20, Region: "ap-east"},
			},
			prefer: "ap-east",
			want:   n2, ok: true,
		},
		{
			name: "全部并列时 node_id 字典序稳定",
			candidates: []anchorCandidate{
				{NodeID: n3, RoomUsers: 5, CPUPercent: 20},
				{NodeID: n1, RoomUsers: 5, CPUPercent: 20},
				{NodeID: n2, RoomUsers: 5, CPUPercent: 20},
			},
			want: n1, ok: true,
		},
		{
			name:       "无候选返回失败",
			candidates: nil,
			want:       uuid.Nil, ok: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := electAnchor(tc.candidates, tc.prefer)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("electAnchor=(%s,%v)，期望 (%s,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestNextEpoch epoch 单调递增（docs 08 §3.1）。
func TestNextEpoch(t *testing.T) {
	if nextEpoch(1) != 2 || nextEpoch(41) != 42 {
		t.Fatal("epoch 应严格 +1 递增")
	}
	epoch := int64(0)
	for i := 0; i < 5; i++ {
		next := nextEpoch(epoch)
		if next <= epoch {
			t.Fatal("epoch 必须单调递增")
		}
		epoch = next
	}
}

// TestStarEdges 纯星型：所有非 anchor 成员挂 anchor，深度 1，无自环。
func TestStarEdges(t *testing.T) {
	anchor := uuid.New()
	leaf1, leaf2 := uuid.New(), uuid.New()
	edges := starEdges(anchor, []uuid.UUID{anchor, leaf1, leaf2})
	if len(edges) != 2 {
		t.Fatalf("应有 2 条边，得 %d", len(edges))
	}
	for _, e := range edges {
		if e.ParentNodeID != anchor {
			t.Fatal("星型边 parent 必须是 anchor")
		}
		if e.ChildNodeID == anchor {
			t.Fatal("anchor 不能自挂")
		}
	}
}
