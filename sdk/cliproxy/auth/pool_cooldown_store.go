package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// PoolCooldownStateStore keeps each instance's transient snapshots separate.
// Its root must be a shared private tmpfs, not the persistent auth directory.
type PoolCooldownStateStore struct {
	root     string
	authDir  string
	local    *FileCooldownStateStore
	identity string
	rootInfo os.FileInfo
	stateMu  sync.RWMutex
	writeErr error
}

type poolTerminalRecord struct {
	AuthID        string    `json:"auth_id"`
	Code          string    `json:"code,omitempty"`
	Model         string    `json:"model,omitempty"`
	TokenDigest   string    `json:"token_digest,omitempty"`
	RefreshDigest string    `json:"refresh_digest,omitempty"`
	ObservedAt    time.Time `json:"observed_at,omitempty"`
	ClearedAt     time.Time `json:"cleared_at,omitempty"`
}

func poolTokenDigest(token string) string {
	if token == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (s *PoolCooldownStateStore) terminalRecords() (map[string]poolTerminalRecord, error) {
	if err := s.checkIdentity(); err != nil {
		return nil, err
	}
	result := make(map[string]poolTerminalRecord)
	data, err := os.ReadFile(filepath.Join(s.root, "terminals.json"))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("pool terminal state too large")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("invalid pool terminal state")
	}
	for id, record := range result {
		if id == "" || record.AuthID != id {
			return nil, errors.New("invalid pool terminal record")
		}
		if record.ObservedAt.IsZero() {
			if record.ClearedAt.IsZero() || record.Code != "" || record.TokenDigest != "" || record.RefreshDigest != "" || record.Model != "" {
				return nil, errors.New("invalid pool terminal tombstone")
			}
		} else {
			if (record.Code != "tos_violation" && record.Code != "unauthorized") || (record.Code == "tos_violation" && record.Model == "") || len(record.TokenDigest) != 64 {
				return nil, errors.New("invalid pool terminal record")
			}
			if _, err := hex.DecodeString(record.TokenDigest); err != nil {
				return nil, errors.New("invalid pool terminal digest")
			}
			if record.RefreshDigest != "" {
				if len(record.RefreshDigest) != 64 {
					return nil, errors.New("invalid pool refresh digest")
				}
				if _, err := hex.DecodeString(record.RefreshDigest); err != nil {
					return nil, errors.New("invalid pool refresh digest")
				}
			}
		}
	}
	return result, nil
}

func (s *PoolCooldownStateStore) Terminals() (map[string]poolTerminalRecord, error) {
	records, err := s.terminalRecords()
	if err != nil {
		return nil, err
	}
	for id, record := range records {
		if record.ObservedAt.IsZero() || !record.ObservedAt.After(record.ClearedAt) {
			delete(records, id)
		}
	}
	return records, nil
}

func (s *PoolCooldownStateStore) PublishTerminal(authID, model, token, refresh, code string, at time.Time) error {
	if authID == "" || token == "" || at.IsZero() || (code != "tos_violation" && code != "unauthorized") || (code == "tos_violation" && model == "") {
		_ = s.markUnsafe()
		return s.noteWrite(errors.New("pool terminal token identity unavailable"))
	}
	path := filepath.Join(s.root, "terminals.json")
	err := codex.WithFileLock(path, func() error {
		if err := s.checkIdentity(); err != nil {
			return err
		}
		records, err := s.terminalRecords()
		if err != nil {
			return err
		}
		previous, ok := records[authID]
		refreshDigest := poolTokenDigest(refresh)
		sameCredential := previous.TokenDigest == poolTokenDigest(token) || (refreshDigest != "" && previous.RefreshDigest == refreshDigest)
		preserveTOS := previous.Code == "tos_violation" && previous.ObservedAt.After(previous.ClearedAt) && sameCredential && code != "tos_violation"
		if (!ok || at.After(previous.ClearedAt)) && !previous.ObservedAt.After(at) && !preserveTOS {
			records[authID] = poolTerminalRecord{AuthID: authID, Model: model, Code: code, TokenDigest: poolTokenDigest(token), RefreshDigest: refreshDigest, ObservedAt: at.UTC(), ClearedAt: previous.ClearedAt}
		}
		values := make(map[string]any, len(records))
		for id, value := range records {
			values[id] = value
		}
		return codex.WriteJSONFileAtomic(path, values)
	})
	if err != nil {
		_ = s.markUnsafe()
		return s.noteWrite(err)
	}
	return nil
}

func (s *PoolCooldownStateStore) ClearTerminal(authID string, at time.Time) error {
	if authID == "" || at.IsZero() {
		return s.noteWrite(errors.New("invalid pool terminal clear"))
	}
	path := filepath.Join(s.root, "terminals.json")
	err := codex.WithFileLock(path, func() error {
		records, err := s.terminalRecords()
		if err != nil {
			return err
		}
		item := records[authID]
		item.AuthID = authID
		if at.After(item.ClearedAt) {
			item.ClearedAt = at.UTC()
		}
		records[authID] = item
		values := make(map[string]any, len(records))
		for id, value := range records {
			values[id] = value
		}
		return codex.WriteJSONFileAtomic(path, values)
	})
	if err != nil {
		return s.noteWrite(err)
	}
	return nil
}

type unavailablePoolCooldownStore struct{ message string }

func NewUnavailablePoolCooldownStore(message string) CooldownStateStore {
	return &unavailablePoolCooldownStore{message: message}
}
func (s *unavailablePoolCooldownStore) Load(context.Context) ([]CooldownStateRecord, error) {
	return nil, errors.New(s.message)
}
func (s *unavailablePoolCooldownStore) Save(context.Context, []CooldownStateRecord) error {
	return errors.New(s.message)
}

// LazyPoolCooldownStateStore permits the shared tmpfs initializer to start
// after the model container on host reboot. Once bound, identity never changes.
type LazyPoolCooldownStateStore struct {
	mu sync.Mutex
	root, authDir, slot string
	bound *PoolCooldownStateStore
}

func NewLazyPoolCooldownStateStore(root, authDir, slot string) CooldownStateStore {
	return &LazyPoolCooldownStateStore{root: root, authDir: authDir, slot: slot}
}

func (s *LazyPoolCooldownStateStore) PoolStore() (*PoolCooldownStateStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bound != nil { return s.bound, nil }
	store, err := NewPoolCooldownStateStore(s.root, s.authDir, s.slot)
	if err != nil { return nil, err }
	s.bound = store
	return store, nil
}

func (s *LazyPoolCooldownStateStore) Load(ctx context.Context) ([]CooldownStateRecord, error) {
	store, err := s.PoolStore()
	if err != nil { return nil, err }
	return store.Load(ctx)
}

func (s *LazyPoolCooldownStateStore) Save(ctx context.Context, records []CooldownStateRecord) error {
	store, err := s.PoolStore()
	if err != nil { return err }
	return store.Save(ctx, records)
}

func (s *PoolCooldownStateStore) PoolStore() (*PoolCooldownStateStore, error) { return s, nil }

func NewPoolCooldownStateStore(root, authDir, slot string) (*PoolCooldownStateStore, error) {
	if root == "" || !filepath.IsAbs(root) || (slot != "blue" && slot != "green" && slot != "amber") {
		return nil, errors.New("invalid pool cooldown store configuration")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("pool cooldown root unavailable")
	}
	data, err := os.ReadFile(filepath.Join(root, ".pool-id"))
	if err != nil {
		return nil, errors.New("pool cooldown identity unavailable")
	}
	identity := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(identity)
	if err != nil || len(decoded) != 16 || identity != strings.ToLower(identity) {
		return nil, errors.New("invalid pool cooldown identity")
	}
	rootProbe, err := os.CreateTemp(root, ".pool-root-probe-")
	if err != nil {
		return nil, err
	}
	rootProbeName := rootProbe.Name()
	if err := rootProbe.Close(); err != nil {
		_ = os.Remove(rootProbeName)
		return nil, err
	}
	if err := os.Remove(rootProbeName); err != nil {
		return nil, err
	}
	local := NewFileCooldownStateStoreWithAuthDir(filepath.Join(root, slot), authDir)
	local.poolPrivate = true
	if err := os.MkdirAll(local.dir, 0o700); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(local.dir, ".write-probe-")
	if err != nil {
		return nil, err
	}
	probeName := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probeName)
		return nil, err
	}
	if err := os.Remove(probeName); err != nil {
		return nil, err
	}
	return &PoolCooldownStateStore{root: root, authDir: authDir, local: local, identity: identity, rootInfo: info}, nil
}

func (s *PoolCooldownStateStore) Identity() string { return s.identity }

func (s *PoolCooldownStateStore) Ready() bool {
	s.stateMu.RLock()
	err := s.writeErr
	s.stateMu.RUnlock()
	if err != nil || s.checkIdentity() != nil {
		return false
	}
	_, err = os.Lstat(filepath.Join(s.root, ".pool-unsafe"))
	return errors.Is(err, os.ErrNotExist)
}

// A terminal credential error without a verifiable token identity must stop
// the whole pool until an operator reconciles state; no raw error is stored.
func (s *PoolCooldownStateStore) markUnsafe() error {
	if err := s.checkIdentity(); err != nil {
		return err
	}
	path := filepath.Join(s.root, ".pool-unsafe")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

func (s *PoolCooldownStateStore) noteWrite(err error) error {
	s.stateMu.Lock()
	s.writeErr = err
	s.stateMu.Unlock()
	return err
}

func (s *PoolCooldownStateStore) checkIdentity() error {
	info, err := os.Lstat(s.root)
	if err != nil || !info.IsDir() || !os.SameFile(info, s.rootInfo) {
		return errors.New("pool cooldown root changed")
	}
	data, err := os.ReadFile(filepath.Join(s.root, ".pool-id"))
	if err != nil || strings.TrimSpace(string(data)) != s.identity {
		return errors.New("pool cooldown identity changed")
	}
	return nil
}

func (s *PoolCooldownStateStore) Save(ctx context.Context, records []CooldownStateRecord) error {
	if err := s.checkIdentity(); err != nil {
		return s.noteWrite(err)
	}
	clean := make([]CooldownStateRecord, len(records))
	for index, record := range records {
		if record.Reason != "quota" && record.Reason != "credential_quota" {
			record.Reason = ""
		}
		if record.Quota.Reason != "quota" && record.Quota.Reason != "credential_quota" {
			record.Quota.Reason = ""
		}
		record.CooldownSignals = sanitizePersistedClaudeCooldownSignals(record.CooldownSignals)
		record.IndependentCooldownSignals = sanitizePersistedClaudeCooldownSignals(record.IndependentCooldownSignals)
		if record.LastError != nil {
			record.LastError = record.LastError.Clone()
			record.LastError.Message = ""
			record.LastError.Metadata = nil
			if record.LastError.Code != ErrorCodeForceCooldown {
				record.LastError.Code = ""
			}
		}
		if record.IndependentState != nil {
			record.IndependentState = record.IndependentState.Clone()
			record.IndependentState.StatusMessage = ""
			if record.IndependentState.Quota.Reason != "quota" && record.IndependentState.Quota.Reason != "credential_quota" {
				record.IndependentState.Quota.Reason = ""
			}
			if record.IndependentState.LastError != nil {
				record.IndependentState.LastError.Message = ""
				record.IndependentState.LastError.Metadata = nil
				if record.IndependentState.LastError.Code != ErrorCodeForceCooldown {
					record.IndependentState.LastError.Code = ""
				}
			}
			record.IndependentState.Quota.Signals = nil
		}
		record.Quota = record.Quota.Clone()
		record.Quota.Signals = nil
		clean[index] = record
	}
	return s.noteWrite(s.local.Save(ctx, clean))
}

func (s *PoolCooldownStateStore) Load(ctx context.Context) ([]CooldownStateRecord, error) {
	resets, err := s.Resets()
	if err != nil {
		return nil, err
	}
	var result []CooldownStateRecord
	for _, slot := range []string{"blue", "green", "amber"} {
		store := NewFileCooldownStateStoreWithAuthDir(filepath.Join(s.root, slot), s.authDir)
		records, err := store.Load(ctx)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if reset, ok := resets[record.AuthID]; ok && (record.UpdatedAt.IsZero() || !record.UpdatedAt.After(reset)) {
				continue
			}
			result = append(result, record)
		}
	}
	return result, nil
}

func (s *PoolCooldownStateStore) Resets() (map[string]time.Time, error) {
	if err := s.checkIdentity(); err != nil {
		return nil, err
	}
	result := make(map[string]time.Time)
	data, err := os.ReadFile(filepath.Join(s.root, "resets.json"))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("pool cooldown reset state too large")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("invalid pool cooldown reset state: %w", err)
	}
	return result, nil
}

func (s *PoolCooldownStateStore) Reset(authID string, at time.Time) error {
	if authID == "" || at.IsZero() {
		return errors.New("invalid pool cooldown reset")
	}
	path := filepath.Join(s.root, "resets.json")
	err := codex.WithFileLock(path, func() error {
		if err := s.checkIdentity(); err != nil {
			return err
		}
		resets, err := s.Resets()
		if err != nil {
			return err
		}
		if previous := resets[authID]; previous.After(at) {
			at = previous
		}
		resets[authID] = at.UTC()
		values := make(map[string]any, len(resets))
		for id, value := range resets {
			values[id] = value
		}
		return codex.WriteJSONFileAtomic(path, values)
	})
	if err != nil {
		return s.noteWrite(err)
	}
	return nil
}
