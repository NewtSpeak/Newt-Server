package auditundo

import (
	"testing"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

func TestEffectiveUndoStatus(t *testing.T) {
	id := uuid.New()
	undoneBy := uuid.New()

	cases := []struct {
		name string
		log  model.AuditLog
		want string
	}{
		{
			name: "available reversible with handler",
			log:  model.AuditLog{Action: "moderation.ban", Reversible: true, UndoStatus: model.AuditUndoAvailable},
			want: model.AuditUndoAvailable,
		},
		{
			name: "undone by flag",
			log:  model.AuditLog{Action: "moderation.ban", UndoStatus: model.AuditUndoUndone, UndoneByID: &undoneBy},
			want: model.AuditUndoUndone,
		},
		{
			name: "undo entry irreversible",
			log:  model.AuditLog{Action: "audit.undo", UndoOfID: &id},
			want: model.AuditUndoIrreversible,
		},
		{
			name: "kick irreversible catalog",
			log:  model.AuditLog{Action: "moderation.kick", UndoStatus: model.AuditUndoIrreversible},
			want: model.AuditUndoIrreversible,
		},
		{
			name: "legacy reversible without status but has handler",
			log:  model.AuditLog{Action: "moderation.ban", Reversible: false, UndoStatus: model.AuditUndoNone},
			want: model.AuditUndoAvailable, // catalog + handler
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveUndoStatus(tc.log)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestHasHandlers(t *testing.T) {
	required := []string{
		"moderation.ban",
		"moderation.unban",
		"restriction.create",
		"restriction.lift",
		"rbac.role_update",
		"guild.update",
	}
	for _, action := range required {
		if !Has(action) {
			t.Fatalf("missing handler for %s", action)
		}
	}
}
