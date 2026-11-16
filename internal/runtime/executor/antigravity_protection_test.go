package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestguard"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func protectionAuth(url string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "test-account", Provider: "antigravity", Attributes: map[string]string{"base_url": url}, Metadata: map[string]any{"email": "fixture@example.invalid", "access_token": "synthetic-token", "project_id": "synthetic-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
}
func protectionRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"fixture"}]}]}}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}
}

func TestAntigravityProtectionActualTransportBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"synthetic failure"}}`))
	}))
	defer server.Close()
	cfg := &config.Config{AuthDir: t.TempDir(), Antigravity: config.AntigravityConfig{RequestProtection: true}}
	auth := protectionAuth(server.URL)
	req, opts := protectionRequest()
	for i := 0; i < 3; i++ {
		_, err := NewAntigravityExecutor(cfg).Execute(context.Background(), auth, req, opts)
		if err == nil {
			t.Fatal("unexpected success")
		}
		// Advance only the fixture's short start/cooldown deadlines, never the
		// five-minute request budget; no wall-clock sleeps or relaxed limits.
		p := filepath.Join(cfg.AuthDir, ".yeutech-antigravity-protection", "state.json")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var s requestguard.State
		if err = json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		for k := range s.Accounts {
			s.Accounts[k] = time.Now().Add(-time.Second)
		}
		b, err = json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("three independently constructed executors made %d upstream attempts, want 2", calls.Load())
	}
}

func TestAntigravityProtectionActualTransportTOSPause(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		w.Write([]byte(antigravityTOSBody))
	}))
	defer server.Close()
	cfg := &config.Config{AuthDir: t.TempDir(), Antigravity: config.AntigravityConfig{RequestProtection: true}}
	req, opts := protectionRequest()
	auth := protectionAuth(server.URL)
	_, err := NewAntigravityExecutor(cfg).Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("TOS error lost")
	}
	for _, id := range []string{"sibling-1", "sibling-2"} {
		other := protectionAuth(server.URL)
		other.ID = id
		other.Metadata["email"] = id + "@example.invalid"
		_, err = NewAntigravityExecutor(cfg).Execute(context.Background(), other, req, opts)
		if err == nil {
			t.Fatal("shared TOS pause bypassed")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("TOS pause allowed %d calls", calls.Load())
	}
}

func TestAntigravityProtectionDefaultOff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	cfg := &config.Config{AuthDir: t.TempDir()}
	req, opts := protectionRequest()
	for i := 0; i < 3; i++ {
		NewAntigravityExecutor(cfg).Execute(context.Background(), protectionAuth(server.URL), req, opts)
	}
	if calls.Load() != 3 {
		t.Fatal("default behavior changed")
	}
	if _, err := os.Stat(filepath.Join(cfg.AuthDir, ".yeutech-antigravity-protection")); !os.IsNotExist(err) {
		t.Fatal("disabled protection wrote state")
	}
}
