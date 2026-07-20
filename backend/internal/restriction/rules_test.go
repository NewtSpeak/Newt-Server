package restriction

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

func TestValidateScopeDeny(t *testing.T) {
	cases := []struct {
		name     string
		scope    Scope
		deny     DenyFlags
		wantCode string
	}{
		{"文字频道-禁看", ScopeTextChannel, DenyFlags{ViewText: true}, ""},
		{"文字频道-禁发", ScopeTextChannel, DenyFlags{SendText: true}, ""},
		{"全服文字-禁看禁发", ScopeGuildAllText, DenyFlags{ViewText: true, SendText: true}, ""},
		{"语音频道-禁听", ScopeVoiceChannel, DenyFlags{ListenVoice: true}, ""},
		{"语音频道-禁说", ScopeVoiceChannel, DenyFlags{SpeakVoice: true}, ""},
		{"全服语音-禁听禁说", ScopeGuildAllVoice, DenyFlags{ListenVoice: true, SpeakVoice: true}, ""},
		{"文字频道带语音维度", ScopeTextChannel, DenyFlags{SpeakVoice: true}, "INVALID_SCOPE_DENY"},
		{"全服文字带禁听", ScopeGuildAllText, DenyFlags{ViewText: true, ListenVoice: true}, "INVALID_SCOPE_DENY"},
		{"语音频道带文字维度", ScopeVoiceChannel, DenyFlags{SendText: true}, "INVALID_SCOPE_DENY"},
		{"全服语音带禁看", ScopeGuildAllVoice, DenyFlags{ViewText: true, ListenVoice: true}, "INVALID_SCOPE_DENY"},
		{"空 deny", ScopeTextChannel, DenyFlags{}, "INVALID_SCOPE_DENY"},
		{"未知作用域", Scope("WHATEVER"), DenyFlags{ViewText: true}, "INVALID_SCOPE_DENY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateScopeDeny(tc.scope, tc.deny)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("期望通过，实际返回 %v", err)
				}
				return
			}
			if err == nil || err.Code != tc.wantCode {
				t.Fatalf("期望错误码 %s，实际 %v", tc.wantCode, err)
			}
		})
	}
}

func TestApplyImplications(t *testing.T) {
	cases := []struct {
		name string
		in   DenyFlags
		want DenyFlags
	}{
		{"禁看蕴含禁发", DenyFlags{ViewText: true}, DenyFlags{ViewText: true, SendText: true}},
		{"禁听蕴含禁说", DenyFlags{ListenVoice: true}, DenyFlags{ListenVoice: true, SpeakVoice: true}},
		{"只禁发保持原样", DenyFlags{SendText: true}, DenyFlags{SendText: true}},
		{"只禁说保持原样", DenyFlags{SpeakVoice: true}, DenyFlags{SpeakVoice: true}},
		{"空保持原样", DenyFlags{}, DenyFlags{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyImplications(tc.in); got != tc.want {
				t.Fatalf("期望 %+v，实际 %+v", tc.want, got)
			}
		})
	}
}

// TestMaskWithRestrictions 表驱动：四个维度 × 单频道 / 全服作用域的收紧效果。
func TestMaskWithRestrictions(t *testing.T) {
	now := time.Now().UTC()
	guildID := uuid.New()
	textChannelID := uuid.New()
	voiceChannelID := uuid.New()
	otherTextChannelID := uuid.New()
	textChannel := &model.Channel{ID: textChannelID, GuildID: guildID, Type: model.ChannelText}
	otherTextChannel := &model.Channel{ID: otherTextChannelID, GuildID: guildID, Type: model.ChannelText}
	voiceChannel := &model.Channel{ID: voiceChannelID, GuildID: guildID, Type: model.ChannelVoice}

	allBits := rbac.ViewChannel | rbac.ReadMessageHistory | rbac.SendMessages | rbac.AddReactions | rbac.Connect | rbac.Speak

	build := func(scope Scope, channelID *uuid.UUID, deny DenyFlags) model.Restriction {
		return model.Restriction{
			ID: uuid.New(), GuildID: guildID, TargetUserID: uuid.New(),
			Scope: string(scope), ChannelID: channelID,
			DenyViewText: deny.ViewText, DenySendText: deny.SendText,
			DenyListenVoice: deny.ListenVoice, DenySpeakVoice: deny.SpeakVoice,
			Kind: string(KindSanction),
		}
	}

	cases := []struct {
		name        string
		restriction model.Restriction
		channel     *model.Channel
		wantCleared rbac.Permission
	}{
		{"单文字频道禁看-命中", build(ScopeTextChannel, &textChannelID, DenyFlags{ViewText: true}), textChannel, rbac.ViewChannel | rbac.ReadMessageHistory},
		{"单文字频道禁看-别的频道不受影响", build(ScopeTextChannel, &textChannelID, DenyFlags{ViewText: true}), otherTextChannel, 0},
		{"单文字频道禁发-命中", build(ScopeTextChannel, &textChannelID, DenyFlags{SendText: true}), textChannel, rbac.SendMessages | rbac.AddReactions},
		{"全服文字禁看-任意文字频道", build(ScopeGuildAllText, nil, DenyFlags{ViewText: true}), otherTextChannel, rbac.ViewChannel | rbac.ReadMessageHistory},
		{"全服文字禁发-任意文字频道", build(ScopeGuildAllText, nil, DenyFlags{SendText: true}), textChannel, rbac.SendMessages | rbac.AddReactions},
		{"全服文字禁看-语音频道不受影响", build(ScopeGuildAllText, nil, DenyFlags{ViewText: true}), voiceChannel, 0},
		{"单语音频道禁听-命中", build(ScopeVoiceChannel, &voiceChannelID, DenyFlags{ListenVoice: true}), voiceChannel, rbac.Connect},
		{"单语音频道禁说-命中", build(ScopeVoiceChannel, &voiceChannelID, DenyFlags{SpeakVoice: true}), voiceChannel, rbac.Speak},
		{"单语音频道禁说-文字频道不受影响", build(ScopeVoiceChannel, &voiceChannelID, DenyFlags{SpeakVoice: true}), textChannel, 0},
		{"全服语音禁听-任意语音频道", build(ScopeGuildAllVoice, nil, DenyFlags{ListenVoice: true}), voiceChannel, rbac.Connect},
		{"全服语音禁说-任意语音频道", build(ScopeGuildAllVoice, nil, DenyFlags{SpeakVoice: true}), voiceChannel, rbac.Speak},
		{"服务器级仅应用全服作用域-全服文字", build(ScopeGuildAllText, nil, DenyFlags{ViewText: true, SendText: true}), nil, rbac.ViewChannel | rbac.ReadMessageHistory | rbac.SendMessages | rbac.AddReactions},
		{"服务器级仅应用全服作用域-全服语音", build(ScopeGuildAllVoice, nil, DenyFlags{ListenVoice: true, SpeakVoice: true}), nil, rbac.Connect | rbac.Speak},
		{"服务器级不应用单频道限制", build(ScopeTextChannel, &textChannelID, DenyFlags{ViewText: true}), nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskWithRestrictions(allBits, []model.Restriction{tc.restriction}, tc.channel, now)
			want := allBits &^ tc.wantCleared
			if got != want {
				t.Fatalf("期望 %064b，实际 %064b", want, got)
			}
		})
	}
}

// TestMaskWithRestrictionsLifecycle 已解除 / 已过期的限制不再收紧；多条限制取并集。
func TestMaskWithRestrictionsLifecycle(t *testing.T) {
	now := time.Now().UTC()
	guildID := uuid.New()
	channelID := uuid.New()
	channel := &model.Channel{ID: channelID, GuildID: guildID, Type: model.ChannelText}
	bits := rbac.ViewChannel | rbac.ReadMessageHistory | rbac.SendMessages | rbac.AddReactions

	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	lifted := model.Restriction{Scope: string(ScopeGuildAllText), DenySendText: true, LiftedAt: &past}
	expired := model.Restriction{Scope: string(ScopeGuildAllText), DenySendText: true, ExpiresAt: &past}
	activeSend := model.Restriction{Scope: string(ScopeGuildAllText), DenySendText: true, ExpiresAt: &future}
	activeView := model.Restriction{Scope: string(ScopeTextChannel), ChannelID: &channelID, DenyViewText: true}

	if got := MaskWithRestrictions(bits, []model.Restriction{lifted, expired}, channel, now); got != bits {
		t.Fatalf("已解除/已过期的限制不应收紧，实际 %064b", got)
	}
	got := MaskWithRestrictions(bits, []model.Restriction{activeSend, activeView}, channel, now)
	want := bits &^ (rbac.SendMessages | rbac.AddReactions | rbac.ViewChannel | rbac.ReadMessageHistory)
	if got != want {
		t.Fatalf("多条限制应取并集收紧，期望 %064b，实际 %064b", want, got)
	}
}

func TestDenyUnionFor(t *testing.T) {
	now := time.Now().UTC()
	voiceChannelID := uuid.New()
	otherVoiceChannelID := uuid.New()

	speakHere := model.Restriction{Scope: string(ScopeVoiceChannel), ChannelID: &voiceChannelID, DenySpeakVoice: true}
	listenAll := model.Restriction{Scope: string(ScopeGuildAllVoice), DenyListenVoice: true, DenySpeakVoice: true}

	got := DenyUnionFor([]model.Restriction{speakHere}, &voiceChannelID, model.ChannelVoice, now)
	if !got.SpeakVoice || got.ListenVoice {
		t.Fatalf("期望仅禁说，实际 %+v", got)
	}
	got = DenyUnionFor([]model.Restriction{speakHere}, &otherVoiceChannelID, model.ChannelVoice, now)
	if got != (DenyFlags{}) {
		t.Fatalf("其他频道不应命中，实际 %+v", got)
	}
	got = DenyUnionFor([]model.Restriction{speakHere, listenAll}, &voiceChannelID, model.ChannelVoice, now)
	if !got.SpeakVoice || !got.ListenVoice {
		t.Fatalf("期望并集禁听+禁说，实际 %+v", got)
	}
	// channelID 为 nil：只看该媒体类型的全服限制。
	got = DenyUnionFor([]model.Restriction{speakHere, listenAll}, nil, model.ChannelVoice, now)
	if !got.ListenVoice || !got.SpeakVoice {
		t.Fatalf("服务器级应命中全服语音限制，实际 %+v", got)
	}
	got = DenyUnionFor([]model.Restriction{listenAll}, nil, model.ChannelText, now)
	if got != (DenyFlags{}) {
		t.Fatalf("文字类型不应命中语音限制，实际 %+v", got)
	}
}

func TestCheckTarget(t *testing.T) {
	self := uuid.New()
	other := uuid.New()
	cases := []struct {
		name     string
		actor    Actor
		target   Target
		wantCode string
	}{
		{"禁止自限", Actor{UserID: self, SystemAdmin: true}, Target{UserID: self}, "CANNOT_RESTRICT_SELF"},
		{"系统管可限所有者", Actor{UserID: self, SystemAdmin: true}, Target{UserID: other, IsOwner: true}, ""},
		{"服管不可限所有者", Actor{UserID: self, HighestRole: 10}, Target{UserID: other, IsOwner: true, HighestRole: 0}, "CANNOT_RESTRICT_TARGET"},
		{"所有者可限任意成员", Actor{UserID: self, Owner: true, HighestRole: 0}, Target{UserID: other, HighestRole: 99}, ""},
		{"层级不足-相等", Actor{UserID: self, HighestRole: 5}, Target{UserID: other, HighestRole: 5}, "CANNOT_RESTRICT_TARGET"},
		{"层级不足-更低", Actor{UserID: self, HighestRole: 3}, Target{UserID: other, HighestRole: 5}, "CANNOT_RESTRICT_TARGET"},
		{"层级足够", Actor{UserID: self, HighestRole: 5}, Target{UserID: other, HighestRole: 3}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTarget(tc.actor, tc.target)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("期望通过，实际 %v", err)
				}
				return
			}
			if err == nil || err.Code != tc.wantCode {
				t.Fatalf("期望错误码 %s，实际 %v", tc.wantCode, err)
			}
		})
	}
}

func TestValidateDuration(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}
	cases := []struct {
		name        string
		kind        Kind
		expiresAt   *time.Time
		systemAdmin bool
		wantCode    string
	}{
		{"临时 1 小时合法", KindSanction, at(time.Hour), false, ""},
		{"下限 60s 合法", KindSanction, at(MinDuration), false, ""},
		{"上限 28 天合法", KindSanction, at(MaxDuration), false, ""},
		{"短于 60s 拒绝", KindSanction, at(30 * time.Second), false, "DURATION_OUT_OF_RANGE"},
		{"超过 28 天拒绝", KindSanction, at(MaxDuration + time.Hour), false, "DURATION_OUT_OF_RANGE"},
		{"过去时间拒绝", KindSanction, at(-time.Minute), false, "DURATION_OUT_OF_RANGE"},
		{"长期 SANCTION 普通管理拒绝", KindSanction, nil, false, "DURATION_OUT_OF_RANGE"},
		{"长期 SANCTION 系统管允许", KindSanction, nil, true, ""},
		{"长期 CHANNEL_BAN 允许", KindChannelBan, nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDuration(tc.kind, tc.expiresAt, now, tc.systemAdmin)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("期望通过，实际 %v", err)
				}
				return
			}
			if err == nil || err.Code != tc.wantCode {
				t.Fatalf("期望错误码 %s，实际 %v", tc.wantCode, err)
			}
		})
	}
}
