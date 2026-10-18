package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceSnapshotCannotOverwriteNewerPersistedAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	oldData := []byte(`{"prefix":"old"}`)
	newData := []byte(`{"prefix":"new"}`)
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(oldData)
	m := NewManager(nil, nil, nil)
	a, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "codex", Prefix: "old"})
	if err != nil {
		t.Fatal(err)
	}
	stale := a.Clone()
	stale.Generation = 0
	stale.SetSourceFileSnapshot(path, oldData)
	// A management request persists and commits a newer prefix before the
	// old watcher observation reaches the auth mutation boundary.
	write(newData)
	a.Prefix = "new"
	current, err := m.Update(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Update(t.Context(), stale)
	if err != nil {
		t.Fatal(err)
	}
	if result.Prefix != "new" || result.Generation != current.Generation {
		t.Fatalf("stale file observation committed: prefix=%q generation=%d, want new/%d", result.Prefix, result.Generation, current.Generation)
	}
	// Current file observations still apply and do not leak the fence into
	// subsequent management edits, which are not watcher snapshots.
	fresh := stale.Clone()
	fresh.Prefix = "new"
	fresh.SetSourceFileSnapshot(path, newData)
	result, err = m.Update(t.Context(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if result.sourceFileSnapshot != nil {
		t.Fatal("committed auth retained a pending source fence")
	}
	result.Prefix = "next"
	result, err = m.Update(t.Context(), result)
	if err != nil || result.Prefix != "next" {
		t.Fatalf("management edit rejected: %v", err)
	}
}

func TestSourceSnapshotRejectsChangedOrRemovedRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	a := &Auth{ID: t.Name(), Provider: "codex"}
	a.SetSourceFileSnapshot(path, []byte("original"))
	m := NewManager(nil, nil, nil)
	for _, exists := range []bool{false, true} {
		if exists {
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := m.Register(t.Context(), a.Clone()); err == nil {
			t.Fatal("stale file snapshot registered")
		}
		if _, ok := m.GetByID(a.ID); ok {
			t.Fatal("stale file snapshot left an auth")
		}
	}
}

func TestSourceSnapshotAcceptsCanonicalReformat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	a := &Auth{ID: t.Name(), Provider: "codex"}
	a.SetSourceFileSnapshot(path, []byte(`{"type":"codex","prefix":"same"}`))
	if err := os.WriteFile(path, []byte("{\n \"prefix\": \"same\", \"type\": \"codex\"\n}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.sourceFileSnapshotCurrent() {
		t.Fatal("format-only changes should preserve the source fence")
	}
}
