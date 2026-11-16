package auth

import (
	"testing"
	"time"
)

func TestClearLocalAntigravityCreditsHintPreservesNewerRefresh(t *testing.T) {
	const id = "credits-clear-snapshot"
	t.Cleanup(func() { antigravityCreditsHintByAuth.Delete(id) })
	stale := AntigravityCreditsHint{Known: true, Available: false, UpdatedAt: time.Now().Add(-time.Hour)}
	fresh := AntigravityCreditsHint{Known: true, Available: true, CreditAmount: 2516, UpdatedAt: time.Now()}
	antigravityCreditsHintByAuth.Store(id, stale)
	observed, _ := antigravityCreditsHintByAuth.Load(id)
	// Deterministically interleave the refresh between the old read and clear.
	antigravityCreditsHintByAuth.Store(id, fresh)
	if ClearLocalAntigravityCreditsHintIfUnchanged(id, observed.(AntigravityCreditsHint)) {
		t.Fatal("stale cleanup overwrote a newer credit refresh")
	}
	if got, _ := antigravityCreditsHintByAuth.Load(id); got != fresh {
		t.Fatalf("fresh credit hint changed: %#v", got)
	}
	antigravityCreditsHintByAuth.Store(id, stale)
	if !ClearLocalAntigravityCreditsHintIfUnchanged(id, stale) {
		t.Fatal("unchanged stale hint was not invalidated")
	}
	got, _ := antigravityCreditsHintByAuth.Load(id)
	if hint := got.(AntigravityCreditsHint); hint.Known || hint.UpdatedAt.IsZero() {
		t.Fatalf("invalidated hint = %#v", hint)
	}
}
