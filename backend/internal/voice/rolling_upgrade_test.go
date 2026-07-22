package voice

import (
	"testing"

	"github.com/google/uuid"
)

func TestPickLeastLoadedEven(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	targets := []uuid.UUID{a, b, c}
	base := map[uuid.UUID]int{a: 10, b: 10, c: 10}
	assigned := map[uuid.UUID]int{}
	counts := map[uuid.UUID]int{}
	for i := 0; i < 30; i++ {
		got, ok := pickLeastLoaded(targets, assigned, func(id uuid.UUID) int { return base[id] })
		if !ok {
			t.Fatal("expected pick")
		}
		assigned[got]++
		counts[got]++
	}
	// 应大致均分 10/10/10
	for _, id := range targets {
		if counts[id] != 10 {
			t.Fatalf("node %s got %d, want 10 (spread=%v)", id, counts[id], counts)
		}
	}
}

func TestPickLeastLoadedPrefersLighter(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	targets := []uuid.UUID{a, b}
	base := map[uuid.UUID]int{a: 100, b: 1}
	assigned := map[uuid.UUID]int{}
	got, _ := pickLeastLoaded(targets, assigned, func(id uuid.UUID) int { return base[id] })
	if got != b {
		t.Fatalf("want lighter node b, got %s", got)
	}
}
