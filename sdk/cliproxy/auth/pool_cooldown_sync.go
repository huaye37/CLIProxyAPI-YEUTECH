package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/poollease"
)

type poolTerminalReplayKey struct{}

func (m *Manager) poolStore() (*PoolCooldownStateStore, error) {
	if m == nil { return nil, errors.New("pool cooldown manager unavailable") }
	m.mu.RLock()
	provider, ok := m.cooldownStore.(interface { PoolStore() (*PoolCooldownStateStore, error) })
	m.mu.RUnlock()
	if !ok || provider == nil { return nil, errors.New("pool cooldown coordination unavailable") }
	return provider.PoolStore()
}

// PoolCooldownReady reports only coordination readiness, not quota or account
// state. A missing shared state store must never be treated as an empty pool.
func (m *Manager) PoolCooldownReady() bool {
	_, ok := m.PoolCooldownIdentity()
	return ok
}

func (m *Manager) PoolCooldownIdentity() (string, bool) {
	if m == nil || !poollease.Enabled() {
		return "", false
	}
	store, err := m.poolStore()
	if err != nil || store == nil {
		return "", false
	}
	if !m.poolSyncOK.Load() || !m.poolCredentialSetSafe() {
		return store.Identity(), false
	}
	if !store.Ready() {
		m.persistCooldownStates(context.Background())
		if !store.Ready() {
			return store.Identity(), false
		}
	}
	_, err = store.Load(context.Background())
	if err == nil {
		_, err = store.Terminals()
	}
	return store.Identity(), err == nil
}

// StartPoolCooldownSync keeps the credential owner aware of terminal and quota
// changes even when user requests are currently routed to other replicas.
func (m *Manager) StartPoolCooldownSync(ctx context.Context) {
	if m == nil || !poollease.Enabled() || !m.poolSyncStarted.CompareAndSwap(false, true) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer m.poolSyncStarted.Store(false)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			err := m.syncPoolCooldown(ctx)
			m.poolSyncOK.Store(err == nil)
			select {
			case <-ctx.Done():
				m.poolSyncOK.Store(false)
				return
			case <-ticker.C:
			}
		}
	}()
}

// Pool mode is fail-closed for renewable providers that have not yet been
// audited for cross-process refresh and persistence behavior.
func (m *Manager) poolCredentialSetSafe() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, value := range m.auths {
		if value == nil || value.Disabled || value.Status == StatusDisabled || IsConfigAPIKeyAuth(value) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value.Provider)) {
		case "codex", "xai", "antigravity":
		default:
			return false
		}
	}
	return true
}

func (m *Manager) syncPoolCooldown(ctx context.Context) error {
	if !poollease.Enabled() {
		return nil
	}
	if m == nil {
		return errors.New("pool cooldown manager unavailable")
	}
	if !m.poolCredentialSetSafe() {
		return errors.New("pool credential type is not coordinated")
	}
	store, err := m.poolStore()
	if err != nil || store == nil {
		return errors.New("pool cooldown coordination unavailable")
	}
	if !store.Ready() {
		m.persistCooldownStates(ctx)
		if !store.Ready() {
			return errors.New("pool cooldown coordination is not writable")
		}
	}
	return m.syncPoolCooldownStore(ctx, store)
}

func (m *Manager) syncPoolCooldownStore(ctx context.Context, store *PoolCooldownStateStore) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.poolCooldownSyncMu.Lock()
	defer m.poolCooldownSyncMu.Unlock()
	resets, err := store.Resets()
	if err != nil {
		return err
	}
	terminals, err := store.Terminals()
	if err != nil {
		return err
	}
	for id, record := range terminals {
		current, exists := m.GetByID(id)
		if !exists || current == nil || current.Disabled || current.Status == StatusDisabled ||
			(record.Code == "unauthorized" && hasUnauthorizedAuthFailure(current)) {
			continue
		}
		token := authAccessToken(current)
		accessMatch := token != "" && poolTokenDigest(token) == record.TokenDigest
		refresh := authRefreshToken(current)
		refreshMatch := record.Code == "tos_violation" && refresh != "" && record.RefreshDigest != "" && poolTokenDigest(refresh) == record.RefreshDigest
		if !accessMatch && !refreshMatch {
			continue
		}
		replay := context.WithValue(ctx, poolTerminalReplayKey{}, true)
		status := http.StatusForbidden
		code := "tos_violation"
		if record.Code == "unauthorized" {
			status, code = http.StatusUnauthorized, ""
		}
		m.MarkResult(replay, Result{AuthID: id, Provider: current.Provider, Model: record.Model,
			CredentialScope: true, AccessToken: token, RefreshToken: refresh, Error: &Error{Code: code, HTTPStatus: status}})
	}
	if m.poolCooldownResets == nil {
		m.poolCooldownResets = make(map[string]time.Time)
	}
	for id, at := range resets {
		if !at.After(m.poolCooldownResets[id]) {
			continue
		}
		m.applyPoolReset(id, at)
		m.poolCooldownResets[id] = at
	}
	return m.RestoreCooldownStates(ctx)
}

// applyPoolReset preserves any model observation newer than the reset.
func (m *Manager) applyPoolReset(id string, reset time.Time) {
	var snapshot *Auth
	now := time.Now()
	m.mu.Lock()
	if current := m.auths[id]; current != nil {
		changed := false
		for _, state := range current.ModelStates {
			if state != nil && !state.UpdatedAt.After(reset) {
				resetModelState(state, now)
				changed = true
			}
		}
		if !current.UpdatedAt.After(reset) && !hasUnauthorizedAuthFailure(current) {
			current.Unavailable = false
			current.NextRetryAfter = time.Time{}
			current.forceCooldownUntil = time.Time{}
			current.credQuotaForceUntil = time.Time{}
			current.credentialRetryAfter = time.Time{}
			current.cooldownRecordedSeq = 0
			applyCooldownFields(&current.Quota, QuotaState{})
			changed = true
		}
		if changed {
			if !current.Disabled && current.Status != StatusDisabled && !hasModelError(current, now) && !hasUnauthorizedAuthFailure(current) {
				current.Status = StatusActive
				current.StatusMessage = ""
				current.LastError = nil
			}
			current.Generation++
			current.UpdatedAt = now
			snapshot = current.Clone()
		}
	}
	m.mu.Unlock()
	if snapshot != nil && m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
}
