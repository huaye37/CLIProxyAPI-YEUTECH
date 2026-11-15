//go:build unix

package poollease

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialLeaderLeaseAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leader.lock")
	lease := New(path)
	if ok, err := lease.TryAcquire(); err != nil || !ok {
		t.Fatalf("first leader: %v %v", ok, err)
	}
	defer lease.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCredentialLeaseChild$")
	cmd.Env = append(os.Environ(), "POOL_LEASE_TEST_PATH="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "waiting\n" {
		t.Fatalf("child acquired locked lease: %q %v", line, err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = stdin.Write([]byte("retry\n")); err != nil {
		t.Fatal(err)
	}
	line, err = reader.ReadString('\n')
	if err != nil || line != "leader\n" {
		t.Fatalf("child did not acquire released lease: %q %v", line, err)
	}
	_ = stdin.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedRefreshLeavesDurableCredentialFreeFence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leader.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRefreshClaimChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+path, "POOL_CLAIM_CHILD=create")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claim child: %v %s", err, output)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".pool-refresh-claims"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("claim missing: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".pool-refresh-claims", entries[0].Name()))
	if err != nil || strings.Contains(string(data), "synthetic-access") || strings.Contains(string(data), "synthetic-refresh") || strings.Contains(string(data), "synthetic-account") {
		t.Fatal("claim contains credentials or identity", err)
	}
	cmd = exec.Command(os.Args[0], "-test.run=^TestRefreshClaimChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+path, "POOL_CLAIM_CHILD=retry")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("retry child: %v %s", err, output)
	}
	cmd = exec.Command(os.Args[0], "-test.run=^TestRefreshClaimChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+path, "POOL_CLAIM_CHILD=recover")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery child: %v %s", err, output)
	}
}

func TestRefreshClaimChild(t *testing.T) {
	mode := os.Getenv("POOL_CLAIM_CHILD")
	if mode == "" {
		return
	}
	if mode == "recover" {
		if _, err := RecoverChangedRefresh("synthetic-account", "synthetic-access", "synthetic-refresh"); !errors.Is(err, ErrRefreshUncertain) {
			t.Fatalf("unchanged identity accepted: %v", err)
		}
		if ok, err := RecoverChangedRefresh("synthetic-account", "new-access", "new-refresh"); err != nil || !ok {
			t.Fatalf("changed identity recovery failed: %v", err)
		}
		claim, err := BeginRefresh("synthetic-account", "new-access", "new-refresh")
		if err != nil || claim == nil {
			t.Fatalf("new claim failed: %v", err)
		}
		if err := claim.Complete(); err != nil {
			t.Fatal(err)
		}
		return
	}
	claim, err := BeginRefresh("synthetic-account", "synthetic-access", "synthetic-refresh")
	if mode == "create" {
		if err != nil || claim == nil {
			t.Fatalf("create: %v", err)
		}
		return // Simulate a crash before persistence or claim completion.
	}
	if !errors.Is(err, ErrRefreshUncertain) || claim != nil {
		t.Fatalf("unsafe retry: %v", err)
	}
}

func TestBootstrapRefreshGateChild(t *testing.T) {
	gate := os.Getenv("POOL_GATE_TEST")
	if gate == "" {
		return
	}
	if MayRefresh() {
		t.Fatal("disabled refresh gate allowed refresh")
	}
	if err := os.WriteFile(gate, []byte(`{"enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !MayRefresh() {
		t.Fatal("enabled refresh gate did not allow leader")
	}
}

func TestBootstrapRefreshGate(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate.json")
	if err := os.WriteFile(gate, []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBootstrapRefreshGateChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+filepath.Join(dir, "leader.lock"),
		"YEUTECH_POOL_REFRESH_GATE_FILE="+gate, "POOL_GATE_TEST="+gate)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gate child: %v %s", err, output)
	}
}

func TestLeadershipDrainChild(t *testing.T) {
	path := os.Getenv("POOL_LEADERSHIP_TEST")
	if path == "" {
		return
	}
	finish, ok := EnterRefresh()
	if !ok {
		t.Fatal("leader could not start refresh")
	}
	if err := os.WriteFile(path, []byte(`{"eligible":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := EnterRefresh(); ok {
		t.Fatal("draining leader accepted a new refresh")
	}
	if !MayRefresh() {
		t.Fatal("in-flight refresh lost permission to persist credentials")
	}
	if _, n := Snapshot(); n != 1 {
		t.Fatalf("in-flight count: %d", n)
	}
	finish()
	if leader, n := Snapshot(); leader || n != 0 {
		t.Fatalf("leader did not relinquish: %v %d", leader, n)
	}
	contender := New(os.Getenv("YEUTECH_POOL_REFRESH_LEADER_FILE"))
	defer contender.Close()
	if ok, err := contender.TryAcquire(); !ok || err != nil {
		t.Fatalf("handoff failed: %v %v", ok, err)
	}
}

func TestLeadershipDrain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leadership.json")
	if err := os.WriteFile(path, []byte(`{"eligible":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeadershipDrainChild$")
	cmd.Env = append(os.Environ(), "YEUTECH_POOL_REFRESH_LEADER_FILE="+filepath.Join(dir, "leader.lock"), "YEUTECH_POOL_LEADERSHIP_FILE="+path, "POOL_LEADERSHIP_TEST="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("leadership child: %v %s", err, output)
	}
}

func TestCredentialLeaseChild(t *testing.T) {
	path := os.Getenv("POOL_LEASE_TEST_PATH")
	if path == "" {
		return
	}
	lease := New(path)
	defer lease.Close()
	if ok, err := lease.TryAcquire(); err != nil || ok {
		t.Fatalf("unexpected initial lease: %v %v", ok, err)
	}
	_, _ = os.Stdout.WriteString("waiting\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	if ok, err := lease.TryAcquire(); err != nil || !ok {
		t.Fatalf("retry failed: %v %v", ok, err)
	}
	_, _ = os.Stdout.WriteString("leader\n")
}

func TestReplacedLeaseFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leader.lock")
	lease := New(path)
	defer lease.Close()
	if ok, err := lease.TryAcquire(); err != nil || !ok {
		t.Fatal("initial lease unavailable", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := lease.TryAcquire(); err == nil || ok {
		t.Fatal("replacement did not fail closed")
	}
	if ok, err := lease.TryAcquire(); err == nil || ok {
		t.Fatal("lost lease was reacquired")
	}
}
