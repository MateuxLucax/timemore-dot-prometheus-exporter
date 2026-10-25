package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/brew"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func espresso(t *testing.T) *brew.Brew {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contract", "valid", "espresso.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := brew.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMigrationsAreVersionedAndReopenable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	s := open(t, path)
	if v, err := s.SchemaVersion(t.Context()); err != nil || v != len(migrations) {
		t.Fatalf("version = %d, %v; want %d", v, err, len(migrations))
	}
	if _, err := s.Upsert(t.Context(), espresso(t)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2 := open(t, path) // second open must not re-run migrations or lose data
	if n, _, _, err := s2.Counts(t.Context()); err != nil || n != 1 {
		t.Fatalf("after reopen: %d brews, %v", n, err)
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "c.db"))
	b := espresso(t)
	for i, wantExisted := range []bool{false, true, true} {
		existed, err := s.Upsert(t.Context(), b)
		if err != nil || existed != wantExisted {
			t.Fatalf("upsert %d: existed=%v err=%v, want %v", i, existed, err, wantExisted)
		}
	}
	brews, samples, _, err := s.Counts(t.Context())
	if err != nil || brews != 1 || samples != len(b.Samples) {
		t.Fatalf("counts = %d brews, %d samples (%v); want 1, %d", brews, samples, err, len(b.Samples))
	}

	// A resend with a different body replaces the brew and all of its samples.
	b.YieldG = 40
	b.Samples = b.Samples[:2]
	if _, err := s.Upsert(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(t.Context())
	if err != nil || len(list) != 1 || list[0].YieldG != 40 || list[0].SampleCount != 2 {
		t.Fatalf("after replace: %+v %v", list, err)
	}
	if _, samples, _, _ := s.Counts(t.Context()); samples != 2 {
		t.Fatalf("samples = %d, want 2", samples)
	}
}

func TestCheckpoint(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "c.db"))
	ctx := t.Context()
	b := espresso(t)
	s.Upsert(ctx, b)

	p, err := s.Pending(ctx, 10)
	if err != nil || len(p) != 1 || p[0].Version != 1 || len(p[0].Brew.Samples) != 4 || *p[0].Brew.TimeToFirstDropS != 6.2 {
		t.Fatalf("pending = %+v, %v", p, err)
	}
	// Replaced after it was read but before it was marked: stays pending.
	s.Upsert(ctx, b)
	if err := s.MarkSent(ctx, b.ID, p[0].Version, ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Pending(ctx, 10); len(p) != 1 || p[0].Version != 2 {
		t.Fatalf("replaced brew must stay pending: %+v", p)
	}
	s.MarkSent(ctx, b.ID, 2, "")
	if p, _ := s.Pending(ctx, 10); len(p) != 0 {
		t.Fatalf("pending after sent = %+v", p)
	}
	// Replaced after it was sent: pending again.
	s.Upsert(ctx, b)
	if p, _ := s.Pending(ctx, 10); len(p) != 1 {
		t.Fatalf("replaced after send must be pending: %+v", p)
	}
	s.MarkSent(ctx, b.ID, 3, "")
	if n, err := s.ResetSent(ctx); err != nil || n != 1 {
		t.Fatalf("reset = %d, %v", n, err)
	}
	if p, _ := s.Pending(ctx, 10); len(p) != 1 {
		t.Fatalf("after reset: %+v", p)
	}
}
