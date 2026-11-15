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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPoolNonLeaderCannotOverwriteOrDeleteCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leader.lock")
	credential := filepath.Join(dir, "synthetic.json")
	if err := os.WriteFile(credential, []byte(`{"access_token":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lease := poollease.New(path)
	if ok, err := lease.TryAcquire(); err != nil || !ok {
		t.Fatal("leader unavailable", err)
	}
	defer lease.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPoolFileStoreChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+path, "POOL_FILESTORE_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	data, err := os.ReadFile(credential)
	if err != nil || string(data) != `{"access_token":"original"}` {
		t.Fatalf("credential changed: %v", err)
	}
}

func TestPoolFileStoreChild(t *testing.T) {
	dir := os.Getenv("POOL_FILESTORE_DIR")
	if dir == "" {
		return
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	value := &cliproxyauth.Auth{ID: "synthetic.json", Metadata: map[string]any{"access_token": "stale"}}
	if _, err := store.Save(context.Background(), value); !errors.Is(err, poollease.ErrNotLeader) {
		t.Fatalf("save: %v", err)
	}
	if err := store.Delete(context.Background(), "synthetic.json"); !errors.Is(err, poollease.ErrNotLeader) {
		t.Fatalf("delete: %v", err)
	}
}

func TestPoolStorageAtomicFailurePreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := savePoolStorageAtomic(path, func(temp string) error {
		if err := os.WriteFile(temp, []byte("partial"), 0o600); err != nil {
			return err
		}
		return errors.New("synthetic failure")
	})
	if err == nil {
		t.Fatal("failure was hidden")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("previous credential changed", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files retained: %v %v", entries, err)
	}
	if err := savePoolStorageAtomic(path, func(temp string) error { return os.WriteFile(temp, []byte("replacement"), 0o600) }); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatal("replacement missing", err)
	}
}
