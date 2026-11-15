package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func poolTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".pool-id"), []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPoolCooldownStoreCombinesIndependentSnapshotsAndReset(t *testing.T) {
	root := poolTestRoot(t)
	blue, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	green, err := NewPoolCooldownStateStore(root, "", "green")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	a := CooldownStateRecord{AuthID: "synthetic-a", Model: "one", UpdatedAt: now, NextRetryAfter: now.Add(time.Hour)}
	b := CooldownStateRecord{AuthID: "synthetic-b", Model: "two", UpdatedAt: now, NextRetryAfter: now.Add(time.Hour)}
	if err := blue.Save(context.Background(), []CooldownStateRecord{a}); err != nil {
		t.Fatal(err)
	}
	if err := green.Save(context.Background(), []CooldownStateRecord{b}); err != nil {
		t.Fatal(err)
	}
	records, err := blue.Load(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("combined records: %d %v", len(records), err)
	}
	if err := green.Reset("synthetic-a", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	records, err = blue.Load(context.Background())
	if err != nil || len(records) != 1 || records[0].AuthID != "synthetic-b" {
		t.Fatalf("reset records: %+v %v", records, err)
	}
	a.UpdatedAt = now.Add(2 * time.Second)
	if err := blue.Save(context.Background(), []CooldownStateRecord{a}); err != nil {
		t.Fatal(err)
	}
	records, err = green.Load(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("new cooldown disappeared: %+v %v", records, err)
	}
}

func TestPoolCooldownStoreRejectsUnknownSlot(t *testing.T) {
	if _, err := NewPoolCooldownStateStore(t.TempDir(), "", "other"); err == nil {
		t.Fatal("unknown slot accepted")
	}
}

func TestPoolCooldownStoreDoesNotPersistRawError(t *testing.T) {
	root := poolTestRoot(t)
	store, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := CooldownStateRecord{AuthID: "synthetic", Model: "one", UpdatedAt: now,
		NextRetryAfter: now.Add(time.Minute), Reason: "secret-reason", Quota: QuotaState{Reason: "secret-quota"}, LastError: &Error{Code: "secret-code", Message: "secret-error-body", Metadata: map[string]string{"raw": "secret-metadata"}},
		IndependentState: &ModelState{StatusMessage: "secret-status", LastError: &Error{Message: "secret-independent"}}}
	if err := store.Save(context.Background(), []CooldownStateRecord{record}); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := filepath.WalkDir(root, func(path string, info os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content += string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "secret-") {
		t.Fatal("raw error leaked to shared pool state")
	}
	if record.LastError.Message != "secret-error-body" {
		t.Fatal("source record was mutated")
	}
}

func TestPoolCooldownSyncPropagatesAndAppliesReset(t *testing.T) {
	root := poolTestRoot(t)
	blue, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	green, err := NewPoolCooldownStateStore(root, "", "green")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(green)
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "synthetic", Provider: "xai"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	record := CooldownStateRecord{Provider: "xai", AuthID: "synthetic", Model: "grok-4", Status: "cooling",
		UpdatedAt: now, NextRetryAfter: now.Add(time.Hour), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}, LastError: &Error{HTTPStatus: 429}}
	if err := blue.Save(context.Background(), []CooldownStateRecord{record}); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldownStore(context.Background(), green); err != nil {
		t.Fatal(err)
	}
	value, _ := manager.GetByID("synthetic")
	if state := value.ModelStates["grok-4"]; state == nil || !state.Unavailable {
		t.Fatal("peer cooldown not applied")
	}
	if err := blue.Reset("synthetic", time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldownStore(context.Background(), green); err != nil {
		t.Fatal(err)
	}
	value, _ = manager.GetByID("synthetic")
	if state := value.ModelStates["grok-4"]; state == nil || state.Unavailable {
		t.Fatal("peer reset not applied")
	}
}

func TestPoolResetPreservesNewerCooldownOnAnotherModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Now().UTC()
	value := &Auth{ID: "synthetic", Provider: "xai", ModelStates: map[string]*ModelState{
		"old": {Status: StatusError, Unavailable: true, UpdatedAt: now.Add(-time.Second), NextRetryAfter: now.Add(time.Hour)},
		"new": {Status: StatusError, Unavailable: true, UpdatedAt: now.Add(time.Second), NextRetryAfter: now.Add(time.Hour)},
	}}
	if _, err := manager.Register(WithSkipPersist(context.Background()), value); err != nil {
		t.Fatal(err)
	}
	manager.applyPoolReset("synthetic", now)
	got, _ := manager.GetByID("synthetic")
	if got.ModelStates["old"].Unavailable {
		t.Fatal("old cooldown survived reset")
	}
	if !got.ModelStates["new"].Unavailable {
		t.Fatal("newer cooldown lost during reset")
	}
}

func TestPoolTerminalIsTokenBoundAndExplicitClearWinsOlderResult(t *testing.T) {
	root := poolTestRoot(t)
	store, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.PublishTerminal("synthetic", "gemini-test", "old-token", "", "tos_violation", now); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	value := &Auth{ID: "synthetic", Provider: "antigravity", Metadata: map[string]any{"access_token": "new-token"}}
	if _, err := manager.Register(WithSkipPersist(context.Background()), value); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldownStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	got, _ := manager.GetByID("synthetic")
	if got.Disabled {
		t.Fatal("old token suspension disabled a replacement credential")
	}
	if err := store.PublishTerminal("synthetic", "gemini-test", "new-token", "", "tos_violation", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldownStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	got, _ = manager.GetByID("synthetic")
	if !got.Disabled || got.StatusMessage != "tos_violation" {
		t.Fatal("matching terminal event was not applied")
	}
	if err := store.ClearTerminal("synthetic", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishTerminal("synthetic", "gemini-test", "new-token", "", "tos_violation", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	records, err := store.Terminals()
	if err != nil || len(records) != 0 {
		t.Fatalf("old event overrode explicit restoration: %v %v", records, err)
	}
}

func TestPoolTerminalKeepsTOSAcrossSameGrantWithoutCondemningRefreshed401(t *testing.T) {
	store, err := NewPoolCooldownStateStore(poolTestRoot(t), "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.PublishTerminal("synthetic", "model", "old-access", "same-refresh", "unauthorized", now); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	value := &Auth{ID: "synthetic", Provider: "antigravity", Metadata: map[string]any{"access_token": "new-access", "refresh_token": "same-refresh"}}
	if _, err := manager.Register(WithSkipPersist(context.Background()), value); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldownStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	got, _ := manager.GetByID("synthetic")
	if got.Disabled || got.Unavailable {
		t.Fatal("old 401 condemned a refreshed access token")
	}
	if err := store.PublishTerminal("synthetic", "model", "old-access", "same-refresh", "tos_violation", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishTerminal("synthetic", "model", "old-access", "same-refresh", "unauthorized", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldownStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	got, _ = manager.GetByID("synthetic")
	if !got.Disabled || got.StatusMessage != "tos_violation" {
		t.Fatal("same-grant TOS was lost")
	}
}

func TestUnidentifiableTerminalSuspendsWholePool(t *testing.T) {
	root := poolTestRoot(t)
	blue, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	green, err := NewPoolCooldownStateStore(root, "", "green")
	if err != nil {
		t.Fatal(err)
	}
	if err := blue.PublishTerminal("synthetic", "model", "", "", "tos_violation", time.Now()); err == nil {
		t.Fatal("missing token accepted")
	}
	if blue.Ready() || green.Ready() {
		t.Fatal("pool continued after unidentified terminal error")
	}
}

func TestPoolCooldownStoreFailsClosedWhenSharedIdentityChanges(t *testing.T) {
	root := poolTestRoot(t)
	store, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".pool-id"), []byte("fedcba9876543210fedcba9876543210"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("changed pool identity accepted")
	}
	if err := store.Save(context.Background(), nil); err == nil {
		t.Fatal("wrote to changed pool")
	}
}

func TestPoolCooldownStoreWaitsForBootInitializerWithoutRebinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	lazy := NewLazyPoolCooldownStateStore(root, "", "blue").(*LazyPoolCooldownStateStore)
	if _, err := lazy.PoolStore(); err == nil { t.Fatal("missing boot state accepted") }
	if err := os.Mkdir(root, 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(root, ".pool-id"), []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil { t.Fatal(err) }
	store, err := lazy.PoolStore()
	if err != nil || !store.Ready() { t.Fatal("boot state did not recover", err) }
	if err := os.WriteFile(filepath.Join(root, ".pool-id"), []byte("fedcba9876543210fedcba9876543210"), 0o600); err != nil { t.Fatal(err) }
	bound, err := lazy.PoolStore()
	if err != nil || bound != store || bound.Ready() { t.Fatal("changed identity was rebound") }
}
