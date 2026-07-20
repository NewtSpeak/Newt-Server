package stage

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func ids(n int) []uuid.UUID {
	result := make([]uuid.UUID, n)
	for i := range result {
		result[i] = uuid.New()
	}
	return result
}

func toSet(list []uuid.UUID) map[uuid.UUID]bool {
	set := make(map[uuid.UUID]bool, len(list))
	for _, id := range list {
		set[id] = true
	}
	return set
}

// 第 51+ 人按进房序标记容量禁说（docs 11 Z.3）。
func TestDiffCapacityMutesMarksOverflow(t *testing.T) {
	users := ids(53)
	toMute, toUnmute := DiffCapacityMutes(users, nil, nil, 50)
	if len(toUnmute) != 0 {
		t.Fatalf("不应有解除：%v", toUnmute)
	}
	if len(toMute) != 3 {
		t.Fatalf("应禁说 3 人，实际 %d", len(toMute))
	}
	expect := toSet(users[50:])
	for _, id := range toMute {
		if !expect[id] {
			t.Fatalf("禁说了窗口内用户 %s", id)
		}
	}
}

// 有人离开：FIFO 解除最早被禁说者（= 被禁说者中进房最早者，docs 11 Z.5）。
func TestDiffCapacityMutesFIFORelease(t *testing.T) {
	users := ids(52)
	muted := toSet(users[50:]) // 第 51、52 人已被禁说
	// 第 3 人离开：窗口滑动，第 51 人（muted 中进房最早者）应被解除。
	remaining := append(append([]uuid.UUID{}, users[:2]...), users[3:]...)
	toMute, toUnmute := DiffCapacityMutes(remaining, nil, muted, 50)
	if len(toMute) != 0 {
		t.Fatalf("不应新增禁说：%v", toMute)
	}
	if len(toUnmute) != 1 || toUnmute[0] != users[50] {
		t.Fatalf("应解除最早被禁说者 %s，实际 %v", users[50], toUnmute)
	}
}

// SPEAKER（被抱上者）免疫容量禁说；已离开频道的禁说标记被清理。
func TestDiffCapacityMutesSpeakerExemptAndAbsentCleanup(t *testing.T) {
	users := ids(51)
	speakers := map[uuid.UUID]bool{users[50]: true} // 第 51 人已被抱上
	toMute, toUnmute := DiffCapacityMutes(users, speakers, nil, 50)
	if len(toMute) != 0 {
		t.Fatalf("SPEAKER 不应被容量禁说：%v", toMute)
	}
	ghost := uuid.New() // 已离房但标记残留
	toMute, toUnmute = DiffCapacityMutes(users[:10], nil, map[uuid.UUID]bool{ghost: true}, 50)
	if len(toMute) != 0 || len(toUnmute) != 1 || toUnmute[0] != ghost {
		t.Fatalf("应清理已离房者标记，实际 mute=%v unmute=%v", toMute, toUnmute)
	}
}

// FREE→STAGE 留台裁剪：可说话更久者（Since 更早）优先留台（docs 11 Y.3）。
func TestTrimSpeakersKeepsOldest(t *testing.T) {
	base := time.Now()
	seats := []Seat{
		{UserID: uuid.New(), Since: base.Add(3 * time.Minute)},
		{UserID: uuid.New(), Since: base},
		{UserID: uuid.New(), Since: base.Add(1 * time.Minute)},
		{UserID: uuid.New(), Since: base.Add(2 * time.Minute)},
	}
	kept, demoted := TrimSpeakers(seats, 2)
	if len(kept) != 2 || len(demoted) != 2 {
		t.Fatalf("应留 2 降 2，实际 kept=%d demoted=%d", len(kept), len(demoted))
	}
	if kept[0].UserID != seats[1].UserID || kept[1].UserID != seats[2].UserID {
		t.Fatalf("留台顺序错误：应为进入更早者优先")
	}
	kept, demoted = TrimSpeakers(seats, 10)
	if len(kept) != 4 || demoted != nil {
		t.Fatalf("未超额时不应裁剪")
	}
}

// 申请裁决：幂等、队满拒绝、关申请拒绝（docs 11 AB）。
func TestDecideApply(t *testing.T) {
	base := ApplyInput{Mode: "STAGE", RequestEnabled: true, InChannel: true}
	if code, _ := DecideApply(base); code != "" {
		t.Fatalf("正常申请应通过，实际 %s", code)
	}
	// 重复申请幂等，保持原位（AB.8）。
	dup := base
	dup.AlreadyQueued = true
	if code, idem := DecideApply(dup); code != "" || !idem {
		t.Fatalf("重复申请应幂等成功，实际 code=%s idem=%v", code, idem)
	}
	// 队列满 100 拒绝（AB.7）。
	full := base
	full.QueueLength = MaxQueueLength
	if code, _ := DecideApply(full); code != "STAGE_QUEUE_FULL" {
		t.Fatalf("队满应拒绝，实际 %s", code)
	}
	// 已在队列时即使队满也幂等成功。
	fullDup := full
	fullDup.AlreadyQueued = true
	if code, idem := DecideApply(fullDup); code != "" || !idem {
		t.Fatalf("在队者重复申请应幂等，实际 code=%s", code)
	}
	// 关闭申请仅抱麦（AB.3）。
	disabled := base
	disabled.RequestEnabled = false
	if code, _ := DecideApply(disabled); code != "STAGE_REQUEST_DISABLED" {
		t.Fatalf("关申请应返回 STAGE_REQUEST_DISABLED，实际 %s", code)
	}
	// FREE 模式无申请语义。
	free := base
	free.Mode = "FREE_DISCUSSION"
	if code, _ := DecideApply(free); code != "STAGE_NOT_ACTIVE" {
		t.Fatalf("FREE 模式应拒绝申请，实际 %s", code)
	}
	// 禁说者不可申请。
	restricted := base
	restricted.Restricted = true
	if code, _ := DecideApply(restricted); code != "RESTRICTED" {
		t.Fatalf("禁说者应拒绝，实际 %s", code)
	}
}

// 抱上满额拒绝（docs 11 AA.3）。
func TestDecideBringUpFullRejected(t *testing.T) {
	base := BringUpInput{Mode: "STAGE", InChannel: true, SpeakerCount: 19, MaxSpeakers: 20}
	if code := DecideBringUp(base); code != "" {
		t.Fatalf("有空位应允许，实际 %s", code)
	}
	full := base
	full.SpeakerCount = 20
	if code := DecideBringUp(full); code != "STAGE_FULL" {
		t.Fatalf("满额应返回 STAGE_FULL，实际 %s", code)
	}
	already := base
	already.AlreadySpeaker = true
	if code := DecideBringUp(already); code != "STAGE_ALREADY_SPEAKER" {
		t.Fatalf("已在台上应幂等，实际 %s", code)
	}
	out := base
	out.InChannel = false
	if code := DecideBringUp(out); code != "NOT_IN_VOICE" {
		t.Fatalf("不在频道应拒绝，实际 %s", code)
	}
	restricted := base
	restricted.Restricted = true
	if code := DecideBringUp(restricted); code != "RESTRICTED" {
		t.Fatalf("禁说者应拒绝上台，实际 %s", code)
	}
}

// 动态降额分段函数（docs 14 AY.4–AY.6，系数待压测标定）。
func TestDynamicScreenCapSegments(t *testing.T) {
	cases := []struct {
		name string
		cpu  float64
		want int
	}{
		{"低负载不降额", 30, 8},
		{"中负载 0.75 系数", 65, 6},
		{"高负载 0.50 系数", 80, 4},
		{"超高负载 0.25 系数", 95, 2},
	}
	for _, tc := range cases {
		nodes := []NodeLoad{{CPUPercent: tc.cpu, MaxUsers: 100, CurrentUsers: 10}}
		if got := DynamicScreenCap(nodes, 8); got != tc.want {
			t.Fatalf("%s：期望 %d，实际 %d", tc.name, tc.want, got)
		}
	}
	// 用户占用率高于 CPU 时取较大者。
	nodes := []NodeLoad{{CPUPercent: 10, MaxUsers: 100, CurrentUsers: 95}}
	if got := DynamicScreenCap(nodes, 8); got != 2 {
		t.Fatalf("占用率 95%% 应按 0.25 降额，实际 %d", got)
	}
	// 至少保留 1 路；无节点数据不降额；结果不超过基准。
	if got := DynamicScreenCap([]NodeLoad{{CPUPercent: 99, MaxUsers: 10, CurrentUsers: 10}}, 2); got != 1 {
		t.Fatalf("降额下限应为 1，实际 %d", got)
	}
	if got := DynamicScreenCap(nil, 5); got != 5 {
		t.Fatalf("无负载数据应不降额，实际 %d", got)
	}
}

// 有效上限与剩余配额计算（docs 14 AY §3.1）。
func TestEffectiveAndRemaining(t *testing.T) {
	if got := EffectiveGuildCap(3, false, 1); got != 3 {
		t.Fatalf("动态关闭时应等于基准，实际 %d", got)
	}
	if got := EffectiveGuildCap(3, true, 2); got != 2 {
		t.Fatalf("动态开启取 min，实际 %d", got)
	}
	if got := EffectiveGuildCap(3, true, 9); got != 3 {
		t.Fatalf("有效上限永不高于基准，实际 %d", got)
	}
	// 实际允许 = min(频道剩余, 服有效剩余)。
	if got := RemainingScreens(1, 2, 2, 3); got != 1 {
		t.Fatalf("期望剩余 1，实际 %d", got)
	}
	if got := RemainingScreens(0, 2, 3, 3); got != 0 {
		t.Fatalf("服满额时应为 0，实际 %d", got)
	}
	if got := RemainingScreens(5, 2, 0, 3); got != 0 {
		t.Fatalf("频道超占时不应为负，实际 %d", got)
	}
}

// 质量档位校验（docs 14 BA.1）。
func TestQualityAllowed(t *testing.T) {
	if !QualityAllowed("480p", "720p") || !QualityAllowed("720p", "720p") {
		t.Fatal("不高于上限的档位应允许")
	}
	if QualityAllowed("1080p", "720p") {
		t.Fatal("超过上限档位应拒绝")
	}
	if QualityAllowed("4k", "1080p") || QualityAllowed("720p", "bogus") {
		t.Fatal("未知档位应拒绝")
	}
}
