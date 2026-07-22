package message

import (
	"testing"

	"github.com/google/uuid"
)

func TestRoleSetsIntersect(t *testing.T) {
	moderator := uuid.New()
	member := uuid.New()
	tests := []struct {
		name   string
		exempt []uuid.UUID
		held   []uuid.UUID
		want   bool
	}{
		{name: "未配置角色时所有成员受限", held: []uuid.UUID{moderator}, want: false},
		{name: "持有非豁免角色仍受限", exempt: []uuid.UUID{moderator}, held: []uuid.UUID{member}, want: false},
		{name: "持有指定角色时豁免", exempt: []uuid.UUID{moderator}, held: []uuid.UUID{member, moderator}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := roleSetsIntersect(test.exempt, test.held); got != test.want {
				t.Fatalf("roleSetsIntersect() = %v，期待 %v", got, test.want)
			}
		})
	}
}
