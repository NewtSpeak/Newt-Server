package voice

import (
	"testing"

	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// TestProjectCaps caps 投影表驱动（docs 02 §7 + 舞台/Restriction 叠加规则）。
func TestProjectCaps(t *testing.T) {
	base := rbac.ViewChannel | rbac.Connect
	cases := []struct {
		name string
		in   capsInput
		want map[string]bool // cap → 是否应存在
	}{
		{
			name: "普通可说话成员",
			in:   capsInput{Bits: base | rbac.Speak, StageAudio: true, StageScreen: true},
			want: map[string]bool{CapJoin: true, CapSubscribeAudio: true, CapPublishAudio: true, CapPublishScreen: false},
		},
		{
			name: "无 SPEAK 只听",
			in:   capsInput{Bits: base, StageAudio: true, StageScreen: true},
			want: map[string]bool{CapJoin: true, CapSubscribeAudio: true, CapPublishAudio: false},
		},
		{
			name: "server_mute 剥夺 publish_audio",
			in:   capsInput{Bits: base | rbac.Speak, ServerMute: true, StageAudio: true},
			want: map[string]bool{CapJoin: true, CapSubscribeAudio: true, CapPublishAudio: false},
		},
		{
			name: "舞台听众不可发音频（STAGE 模式非 SPEAKER）",
			in:   capsInput{Bits: base | rbac.Speak, StageAudio: false},
			want: map[string]bool{CapJoin: true, CapSubscribeAudio: true, CapPublishAudio: false},
		},
		{
			name: "Restriction 禁说剥夺 publish_audio",
			in:   capsInput{Bits: base | rbac.Speak, StageAudio: true, DenySpeak: true},
			want: map[string]bool{CapJoin: true, CapSubscribeAudio: true, CapPublishAudio: false},
		},
		{
			name: "STREAM + 舞台允许 → publish_screen",
			in:   capsInput{Bits: base | rbac.Speak | rbac.Stream, StageAudio: true, StageScreen: true},
			want: map[string]bool{CapPublishAudio: true, CapPublishScreen: true},
		},
		{
			name: "STREAM 但舞台不许 → 无 publish_screen",
			in:   capsInput{Bits: base | rbac.Stream, StageScreen: false},
			want: map[string]bool{CapPublishScreen: false},
		},
		{
			name: "priority_speaker 透传",
			in:   capsInput{Bits: base | rbac.PrioritySpeaker},
			want: map[string]bool{CapPrioritySpeaker: true},
		},
		{
			name: "server_deaf 不影响恒给的 subscribe_audio（下行限制由 SFU 控制指令处理）",
			in:   capsInput{Bits: base | rbac.Speak, StageAudio: true},
			want: map[string]bool{CapSubscribeAudio: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := projectCaps(tc.in)
			if !hasCap(caps, CapJoin) || !hasCap(caps, CapSubscribeAudio) {
				t.Fatalf("join/subscribe_audio 必须恒给，得 %v", caps)
			}
			for cap, want := range tc.want {
				if hasCap(caps, cap) != want {
					t.Fatalf("cap %s 期望 %v，caps=%v", cap, want, caps)
				}
			}
		})
	}
}
