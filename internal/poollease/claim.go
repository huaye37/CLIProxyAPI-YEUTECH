package poollease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var ErrRefreshUncertain = errors.New("credential refresh outcome is unknown; do not replay the previous grant")

type refreshClaim struct {
	AccessDigest  string `json:"access_digest"`
	RefreshDigest string `json:"refresh_digest"`
}

type Claim struct{ path string }

type managedRefreshKey struct{}

func WithManagedRefresh(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, managedRefreshKey{}, true)
}

func IsManagedRefresh(ctx context.Context) bool {
	return ctx != nil && ctx.Value(managedRefreshKey{}) == true
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// BeginRefresh writes a durable, credential-free fence before a potentially
// rotating refresh grant. An existing fence is not automatically replayed.
func BeginRefresh(authID, access, refresh string) (*Claim, error) {
	defaultOnce.Do(initializeDefault)
	if defaultPath == "" || refresh == "" {
		return nil, nil
	}
	if authID == "" || !MayRefresh() {
		return nil, ErrNotLeader
	}
	dir := filepath.Join(filepath.Dir(defaultPath), ".pool-refresh-claims")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, digest(authID)+".json")
	value := refreshClaim{AccessDigest: digest(access), RefreshDigest: digest(refresh)}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrRefreshUncertain
	}
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return &Claim{path: path}, nil
}

// RecoverChangedRefresh clears an uncertain fence only after the caller has
// verified these values against the authoritative saved credential.
func RecoverChangedRefresh(authID, access, refresh string) (bool, error) {
	defaultOnce.Do(initializeDefault)
	if defaultPath == "" || authID == "" || !MayRefresh() {
		return false, ErrNotLeader
	}
	path := filepath.Join(filepath.Dir(defaultPath), ".pool-refresh-claims", digest(authID)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if len(data) > 4096 {
		return false, ErrRefreshUncertain
	}
	var claim refreshClaim
	if err := json.Unmarshal(data, &claim); err != nil || len(claim.AccessDigest) != 64 || len(claim.RefreshDigest) != 64 {
		return false, ErrRefreshUncertain
	}
	if claim.AccessDigest == digest(access) && claim.RefreshDigest == digest(refresh) {
		return false, ErrRefreshUncertain
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Claim) Complete() error {
	if c == nil {
		return nil
	}
	return os.Remove(c.path)
}
