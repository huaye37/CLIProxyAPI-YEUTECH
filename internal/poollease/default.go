package poollease

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

var ErrNotLeader = errors.New("pool credential state is owned by another proxy instance")

var (
	defaultOnce       sync.Once
	defaultPath       string
	defaultLease      *Lease
	defaultGate       string
	defaultLeadership string
	refreshMu         sync.Mutex
	refreshCount      atomic.Int64
)

func initializeDefault() {
	defaultPath = strings.TrimSpace(os.Getenv("YEUTECH_POOL_REFRESH_LEADER_FILE"))
	defaultGate = strings.TrimSpace(os.Getenv("YEUTECH_POOL_REFRESH_GATE_FILE"))
	defaultLeadership = strings.TrimSpace(os.Getenv("YEUTECH_POOL_LEADERSHIP_FILE"))
	if defaultPath != "" {
		defaultLease = New(defaultPath)
	}
}

// Enabled distinguishes ordinary single-instance deployments from a pool.
func Enabled() bool {
	defaultOnce.Do(initializeDefault)
	return defaultPath != ""
}

// MayRefresh fails closed if shared ownership cannot be acquired.
func MayRefresh() bool {
	defaultOnce.Do(initializeDefault)
	if defaultPath == "" {
		return true
	}
	if defaultLease == nil {
		return false
	}
	if !leadershipEligible() && refreshCount.Load() == 0 {
		return false
	}
	if defaultGate != "" {
		data, err := os.ReadFile(defaultGate)
		if err != nil || len(data) > 1024 {
			return false
		}
		var state struct {
			Enabled *bool `json:"enabled"`
		}
		if json.Unmarshal(data, &state) != nil || state.Enabled == nil || !*state.Enabled {
			return false
		}
	}
	owned, err := defaultLease.TryAcquire()
	return err == nil && owned
}

func leadershipEligible() bool {
	if defaultLeadership == "" {
		return true
	}
	data, err := os.ReadFile(defaultLeadership)
	if err != nil || len(data) > 1024 {
		return false
	}
	var state struct {
		Eligible *bool `json:"eligible"`
	}
	return json.Unmarshal(data, &state) == nil && state.Eligible != nil && *state.Eligible
}

// Snapshot releases ownership only after all refresh operations have finished.
func Snapshot() (bool, int) {
	defaultOnce.Do(initializeDefault)
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if defaultLease != nil && !leadershipEligible() && refreshCount.Load() == 0 {
		_ = defaultLease.Close()
	}
	return MayRefresh(), int(refreshCount.Load())
}

// EnterRefresh serializes the gate check with the in-flight count.
func EnterRefresh() (func(), bool) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if !leadershipEligible() || !MayRefresh() {
		return nil, false
	}
	refreshCount.Add(1)
	var once sync.Once
	return func() { once.Do(func() { refreshMu.Lock(); refreshCount.Add(-1); refreshMu.Unlock() }) }, true
}

func RefreshInFlight() int {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	return int(refreshCount.Load())
}
