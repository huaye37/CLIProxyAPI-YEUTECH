//go:build unix

package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPoolStaticAPIKey401DoesNotMarkUnsafe(t *testing.T) {
	if os.Getenv("YEUTECH_TEST_STATIC_401_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPoolStaticAPIKey401DoesNotMarkUnsafe$")
		cmd.Env = append(os.Environ(),
			"YEUTECH_POOL_REFRESH_LEADER_FILE="+filepath.Join(t.TempDir(), "leader.lock"),
			"YEUTECH_POOL_REFRESH_GATE_FILE=", "YEUTECH_POOL_LEADERSHIP_FILE=",
			"YEUTECH_TEST_STATIC_401_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("pool test child: %v\n%s", err, output)
		}
		return
	}

	ctx := context.Background()
	root := poolTestRoot(t)
	store, err := NewPoolCooldownStateStore(root, "", "blue")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	key := &Auth{ID: "static-gemini", Provider: "gemini", Status: StatusActive,
		Attributes: map[string]string{AttributeSource: AuthSourceConfig, AttributeAPIKey: "fixture"}}
	if !IsConfigAPIKeyAuth(key) {
		t.Fatal("test key must be classified as a config API key")
	}
	if _, err := manager.Register(ctx, key); err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(ctx, Result{AuthID: key.ID, Provider: key.Provider, Model: "fixture",
		Error: &Error{HTTPStatus: http.StatusUnauthorized}})
	if _, err := os.Stat(filepath.Join(root, ".pool-unsafe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("static API key 401 must not mark the pool unsafe: %v", err)
	}
	current, ok := manager.GetByID(key.ID)
	if !ok || current == nil || current.Status == StatusActive {
		t.Fatal("static API key failure was ignored")
	}

	executor := &compactTestExecutor{responseBody: []byte("healthy")}
	manager.RegisterExecutor(executor)
	other := &Auth{ID: "other-static-key", Provider: executor.Identifier(), Status: StatusActive,
		Attributes: map[string]string{AttributeSource: AuthSourceConfig, AttributeAPIKey: "other-fixture"}}
	if _, err := manager.Register(ctx, other); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(other.ID, other.Provider, []*registry.ModelInfo{{ID: "healthy-model"}})
	defer registry.GetGlobalRegistry().UnregisterClient(other.ID)
	// Adding the new device-authorized provider must not disable the NAS pool.
	if _, err := manager.Register(ctx, &Auth{ID: "copilot-fixture", Provider: "copilot", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncPoolCooldown(ctx); err != nil {
		t.Fatal(err)
	}
	manager.poolSyncOK.Store(true)
	if !manager.PoolCooldownReady() {
		t.Fatal("unrelated static auth failure must not remove pool readiness")
	}
	response, err := manager.Execute(ctx, []string{other.Provider}, cliproxyexecutor.Request{Model: "healthy-model"}, cliproxyexecutor.Options{})
	if err != nil || string(response.Payload) != "healthy" || executor.calls != 1 {
		t.Fatalf("unaffected provider did not serve request: %v", err)
	}

	oauth := &Auth{ID: "oauth-codex", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{"refresh_token": "fixture"}}
	if _, err := manager.Register(ctx, oauth); err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(ctx, Result{AuthID: oauth.ID, Provider: oauth.Provider, Model: "fixture",
		Error: &Error{HTTPStatus: http.StatusUnauthorized}})
	if _, err := os.Stat(filepath.Join(root, ".pool-unsafe")); err != nil {
		t.Fatalf("OAuth 401 with unknown token identity must still mark the pool unsafe: %v", err)
	}
}
