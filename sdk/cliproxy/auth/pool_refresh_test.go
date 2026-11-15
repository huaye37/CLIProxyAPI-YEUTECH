//go:build unix

package auth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/poollease"
)

func TestPoolNonLeaderDoesNotSendCredentialRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leader.lock")
	lease := poollease.New(path)
	if ok, err := lease.TryAcquire(); err != nil || !ok {
		t.Fatal("leader unavailable", err)
	}
	defer lease.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPoolNonLeaderRefreshChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+path, "POOL_REFRESH_CHILD=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
}

func TestPoolNonLeaderRefreshChild(t *testing.T) {
	if os.Getenv("POOL_REFRESH_CHILD") != "1" {
		return
	}
	manager := NewManager(nil, nil, nil)
	executor := &unauthorizedRefreshExecutor{id: "xai"}
	manager.RegisterExecutor(executor)
	value := &Auth{ID: "synthetic", Provider: "xai", Status: StatusActive, Metadata: map[string]any{"refresh_token": "synthetic"}}
	if _, err := manager.Register(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	_, err := manager.ForceRefreshAuth(context.Background(), value.ID)
	if failure, ok := err.(*Error); !ok || failure.Code != "pool_refresh_not_leader" || failure.HTTPStatus != 503 {
		t.Fatalf("wrong error: %v", err)
	}
	if executor.RefreshCalls() != 0 {
		t.Fatal("worker sent a refresh grant")
	}
	store := NewFileCooldownStateStore(filepath.Dir(os.Getenv("YEUTECH_POOL_REFRESH_LEADER_FILE")))
	if err := store.Save(context.Background(), []CooldownStateRecord{{AuthID: "synthetic"}}); !errors.Is(err, poollease.ErrNotLeader) {
		t.Fatalf("non-leader wrote shared cooldown state: %v", err)
	}
}
