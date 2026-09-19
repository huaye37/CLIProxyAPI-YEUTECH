package websubscription

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNoAccountAvailable = errors.New("no web subscription account available")

type Account struct {
	ID       string
	DriverID string
	Enabled  bool
	Weight   int
	Models   []string
}

type FailureClass string

const (
	FailureNone           FailureClass = ""
	FailureRateLimit      FailureClass = "rate_limit"
	FailureAuthentication FailureClass = "authentication"
	FailureTransient      FailureClass = "transient"
	FailureRequest        FailureClass = "request"
)

type AccountOutcome struct {
	Failure FailureClass
	RetryAt time.Time
	Reason  string
}

type AccountLease struct {
	AccountID string
	DriverID  string
	Model     string
	sequence  uint64
	owner     *AccountScheduler
	once      sync.Once
}

func (l *AccountLease) Release(outcome AccountOutcome) {
	if l == nil || l.owner == nil {
		return
	}
	l.once.Do(func() { l.owner.release(*l, outcome) })
}

type accountState struct {
	account        Account
	inFlight       int
	cooldownUntil  map[string]time.Time
	disabledReason string
}

type AccountScheduler struct {
	mu       sync.Mutex
	accounts map[string]*accountState
	cursors  map[string]int
	now      func() time.Time
	sequence uint64
}

func NewAccountScheduler() *AccountScheduler {
	return newAccountScheduler(time.Now)
}

func newAccountScheduler(now func() time.Time) *AccountScheduler {
	return &AccountScheduler{
		accounts: make(map[string]*accountState),
		cursors:  make(map[string]int),
		now:      now,
	}
}

func (s *AccountScheduler) Upsert(account Account) error {
	account.ID = strings.TrimSpace(account.ID)
	account.DriverID = strings.TrimSpace(account.DriverID)
	if account.ID == "" || account.DriverID == "" {
		return errors.New("account ID and driver ID are required")
	}
	if account.Weight <= 0 {
		account.Weight = 1
	}
	account.Models = normalizeModels(account.Models)

	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.accounts[account.ID]
	if state == nil {
		state = &accountState{cooldownUntil: make(map[string]time.Time)}
		s.accounts[account.ID] = state
	}
	state.account = account
	if account.Enabled {
		state.disabledReason = ""
	}
	return nil
}

func (s *AccountScheduler) Acquire(driverID, model string) (*AccountLease, error) {
	driverID = strings.TrimSpace(driverID)
	model = strings.TrimSpace(model)
	if driverID == "" || model == "" {
		return nil, errors.New("driver ID and model are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	candidates := make([]*accountState, 0, len(s.accounts))
	for _, state := range s.accounts {
		if !state.account.Enabled || state.account.DriverID != driverID || !supportsModel(state.account.Models, model) {
			continue
		}
		if until := state.cooldownUntil[model]; until.After(now) {
			continue
		}
		candidates = append(candidates, state)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w for driver %q and model %q", ErrNoAccountAvailable, driverID, model)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].account.ID < candidates[j].account.ID })

	route := driverID + "\x00" + model
	expanded := make([]*accountState, 0, len(candidates))
	for _, candidate := range candidates {
		for count := 0; count < candidate.account.Weight; count++ {
			expanded = append(expanded, candidate)
		}
	}
	start := s.cursors[route] % len(expanded)
	selected := expanded[start]
	bestLoad := normalizedLoad(selected)
	for offset := 1; offset < len(expanded); offset++ {
		candidate := expanded[(start+offset)%len(expanded)]
		load := normalizedLoad(candidate)
		if load < bestLoad {
			selected = candidate
			bestLoad = load
		}
	}
	s.cursors[route] = (start + 1) % len(expanded)
	selected.inFlight++
	s.sequence++
	return &AccountLease{AccountID: selected.account.ID, DriverID: driverID, Model: model, sequence: s.sequence, owner: s}, nil
}

func (s *AccountScheduler) Snapshot() []AccountSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	result := make([]AccountSnapshot, 0, len(s.accounts))
	for _, state := range s.accounts {
		cooldowns := make(map[string]time.Time)
		for model, until := range state.cooldownUntil {
			if until.After(now) {
				cooldowns[model] = until
			}
		}
		result = append(result, AccountSnapshot{
			ID:             state.account.ID,
			DriverID:       state.account.DriverID,
			Enabled:        state.account.Enabled,
			InFlight:       state.inFlight,
			DisabledReason: state.disabledReason,
			Cooldowns:      cooldowns,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

type AccountSnapshot struct {
	ID             string
	DriverID       string
	Enabled        bool
	InFlight       int
	DisabledReason string
	Cooldowns      map[string]time.Time
}

func (s *AccountScheduler) release(lease AccountLease, outcome AccountOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.accounts[lease.AccountID]
	if state == nil || state.account.DriverID != lease.DriverID {
		return
	}
	if state.inFlight > 0 {
		state.inFlight--
	}
	switch outcome.Failure {
	case FailureAuthentication:
		state.account.Enabled = false
		state.disabledReason = normalizeFailureReason(outcome.Reason, "authentication_failed")
	case FailureRateLimit, FailureTransient:
		if outcome.RetryAt.After(s.now()) {
			state.cooldownUntil[lease.Model] = outcome.RetryAt
		}
	case FailureNone:
		delete(state.cooldownUntil, lease.Model)
	case FailureRequest:
		// Caller request failures do not penalize an account.
	}
}

func normalizedLoad(state *accountState) float64 {
	return float64(state.inFlight) / float64(state.account.Weight)
}

func supportsModel(models []string, model string) bool {
	if len(models) == 0 {
		return true
	}
	for _, candidate := range models {
		if candidate == model {
			return true
		}
	}
	return false
}

func normalizeModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

func normalizeFailureReason(reason, fallback string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fallback
	}
	return reason
}
