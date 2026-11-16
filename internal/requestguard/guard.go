// Package requestguard coordinates conservative upstream admission across replicas.
// It stores only hashes, counters and deadlines, never credentials or request text.
package requestguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/poollease"
)

const Window = 5 * time.Minute
const Interval = 2 * time.Second
const MaxAttempts = 2

type fingerprintKey struct{}

// WithFingerprint preserves the original logical request across translation and
// internal compaction calls. A fingerprint is not an assertion of idempotency.
func WithFingerprint(ctx context.Context, model, operation string, payload []byte) context.Context {
	if ctx.Value(fingerprintKey{}) != nil {
		return ctx
	}
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(operation))
	h.Write([]byte{0})
	h.Write(payload)
	return context.WithValue(ctx, fingerprintKey{}, hex.EncodeToString(h.Sum(nil)))
}

func hash(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }

type Attempt struct {
	Until time.Time `json:"until"`
	Count int       `json:"count"`
}
type State struct {
	PauseUntil time.Time            `json:"pause_until"`
	Accounts   map[string]time.Time `json:"accounts"`
	Requests   map[string]Attempt   `json:"requests"`
}

type Guard struct {
	Root string
	Now  func() time.Time
}

func (g Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

type Denied struct {
	Reason string
	Wait   time.Duration
}

func (e *Denied) Error() string   { return "upstream protection: " + e.Reason }
func (e *Denied) StatusCode() int { return http.StatusTooManyRequests }
func (e *Denied) Headers() http.Header {
	h := http.Header{}
	h.Set("Retry-After", strconv.Itoa(max(1, int(e.Wait.Seconds()+0.999))))
	return h
}
func (e *Denied) RetryAfter() *time.Duration { d := e.Wait; return &d }

func (g Guard) directory() error {
	if !filepath.IsAbs(g.Root) {
		return errors.New("protection state path must be absolute")
	}
	if err := os.MkdirAll(g.Root, 0700); err != nil {
		return errors.New("protection state unavailable")
	}
	i, err := os.Lstat(g.Root)
	if err != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid protection state directory")
	}
	return nil
}

func acquire(ctx context.Context, path string) (*poollease.Lease, error) {
	l := poollease.New(path)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ok, err := l.TryAcquire()
		if err != nil {
			return nil, errors.New("protection lock unavailable")
		}
		if ok {
			return l, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (g Guard) change(ctx context.Context, fn func(*State) error) error {
	if err := g.directory(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	l, err := acquire(ctx, filepath.Join(g.Root, "state.lock"))
	if err != nil {
		return err
	}
	defer l.Close()
	p := filepath.Join(g.Root, "state.json")
	s := State{Accounts: map[string]time.Time{}, Requests: map[string]Attempt{}}
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("protection state unreadable")
	}
	if err == nil {
		if len(b) > 2<<20 || json.Unmarshal(b, &s) != nil || s.Accounts == nil || s.Requests == nil {
			return errors.New("invalid protection state")
		}
		for k, v := range s.Requests {
			if len(k) != 64 || v.Count < 1 || v.Count > MaxAttempts || v.Until.IsZero() {
				return errors.New("invalid protection request record")
			}
		}
		for k, v := range s.Accounts {
			if len(k) != 64 || v.IsZero() {
				return errors.New("invalid protection account record")
			}
		}
	}
	now := g.now()
	for k, v := range s.Requests {
		if !v.Until.After(now) {
			delete(s.Requests, k)
		}
	}
	for k, v := range s.Accounts {
		if !v.After(now) {
			delete(s.Accounts, k)
		}
	}
	if err := fn(&s); err != nil {
		return err
	}
	if len(s.Requests) > 4096 || len(s.Accounts) > 4096 {
		return errors.New("protection state capacity reached")
	}
	b, err = json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(g.Root, ".state-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, p)
}

// Pause is monotonic, survives restarts, and affects only this provider's guard.
func (g Guard) Pause(duration time.Duration) error {
	return g.change(context.Background(), func(s *State) error {
		until := g.now().Add(duration)
		if until.After(s.PauseUntil) {
			s.PauseUntil = until
		}
		return nil
	})
}

type Ticket struct {
	guard            Guard
	account, request string
	lease            *poollease.Lease
	once             sync.Once
}

func (t *Ticket) Close() {
	if t != nil {
		t.once.Do(func() { _ = t.lease.Close() })
	}
}

func (g Guard) Begin(ctx context.Context, account, fingerprint, operation string) (*Ticket, error) {
	if account == "" || fingerprint == "" {
		return nil, errors.New("protection request identity unavailable")
	}
	if err := g.directory(); err != nil {
		return nil, err
	}
	account = hash(account)
	request := hash(account + ":" + fingerprint + ":" + operation)
	// A waiting request can cancel; an admitted stream has no duration cutoff.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := acquire(waitCtx, filepath.Join(g.Root, "account-"+account+".lock"))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Denied{"account queue busy", time.Second * 5}
	}
	t := &Ticket{guard: g, account: account, request: request, lease: l}
	err = g.change(ctx, func(s *State) error {
		now := g.now()
		if s.PauseUntil.After(now) {
			return &Denied{"provider paused after a confirmed service restriction", s.PauseUntil.Sub(now)}
		}
		if until := s.Accounts[account]; until.After(now) {
			return &Denied{"account cooling down", until.Sub(now)}
		}
		a := s.Requests[request]
		if a.Count >= MaxAttempts {
			return &Denied{"identical request attempt budget exhausted", a.Until.Sub(now)}
		}
		if a.Count == 0 {
			a.Until = now.Add(Window)
		}
		a.Count++
		s.Requests[request] = a
		s.Accounts[account] = now.Add(Interval)
		return nil
	})
	if err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

func (t *Ticket) Result(status int, headers http.Header, body []byte) error {
	wait := Interval
	haveHint := false
	if status == 429 {
		wait = 60 * time.Second
	}
	if v := headers.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			wait = time.Duration(n) * time.Second
			haveHint = true
		} else if at, err := http.ParseTime(v); err == nil && at.After(t.guard.now()) {
			wait = at.Sub(t.guard.now())
			haveHint = true
		}
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Reason     string `json:"reason"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	tos := false
	for _, d := range envelope.Error.Details {
		if status == 403 && d.Reason == "TOS_VIOLATION" {
			tos = true
		}
		if status == 429 {
			if duration, err := time.ParseDuration(d.RetryDelay); err == nil && duration > 0 && (!haveHint || duration > wait) {
				wait = duration
				haveHint = true
			}
		}
	}
	region := status == 400 && strings.Contains(strings.ToLower(envelope.Error.Message), "user location is not supported")
	if region {
		wait = time.Hour
	}
	return t.guard.change(context.Background(), func(s *State) error {
		if status >= 400 || status == 0 {
			until := t.guard.now().Add(wait)
			if until.After(s.Accounts[t.account]) {
				s.Accounts[t.account] = until
			}
		}
		if status == 400 || status == 403 || status == 429 {
			a := s.Requests[t.request]
			a.Count = MaxAttempts
			if a.Until.IsZero() {
				a.Until = t.guard.now().Add(Window)
			}
			s.Requests[t.request] = a
		}
		if tos {
			until := t.guard.now().Add(6 * time.Hour)
			if until.After(s.PauseUntil) {
				s.PauseUntil = until
			}
		}
		return nil
	})
}

type Transport struct {
	Base    http.RoundTripper
	Guard   Guard
	Account string
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	// OAuth refresh and discovery have independent coordination. Only inference
	// and token counting consume this budget; no request body is read or changed.
	path := strings.ToLower(r.URL.Path)
	if !strings.Contains(path, "generatecontent") && !strings.Contains(path, "counttokens") {
		return t.Base.RoundTrip(r)
	}
	fingerprint, _ := r.Context().Value(fingerprintKey{}).(string)
	if fingerprint == "" && r.GetBody != nil {
		// Management HttpRequest callers may not have the executor envelope.
		// Hash a replayable copy without consuming or replacing the sent body.
		copyBody, err := r.GetBody()
		if err != nil {
			return nil, errors.New("protection request copy unavailable")
		}
		h := sha256.New()
		_, err = io.Copy(h, copyBody)
		closeErr := copyBody.Close()
		if err != nil || closeErr != nil {
			return nil, errors.New("protection request fingerprint unavailable")
		}
		fingerprint = hex.EncodeToString(h.Sum(nil))
	}
	operation := "generate"
	if strings.Contains(path, "counttokens") {
		operation = "count"
	}
	waitCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	var ticket *Ticket
	var err error
	for {
		ticket, err = t.Guard.Begin(waitCtx, t.Account, fingerprint, operation)
		var denied *Denied
		if !errors.As(err, &denied) || denied.Reason != "account cooling down" || denied.Wait > Interval {
			break
		}
		timer := time.NewTimer(denied.Wait)
		select {
		case <-timer.C:
		case <-waitCtx.Done():
			timer.Stop()
			cancel()
			if r.Context().Err() != nil {
				return nil, r.Context().Err()
			}
			return nil, &Denied{"account queue busy", 5 * time.Second}
		}
	}
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
			return nil, &Denied{"account queue busy", 5 * time.Second}
		}
		return nil, err
	}
	resp, err := t.Base.RoundTrip(r)
	if err != nil {
		_ = ticket.Result(0, nil, nil)
		ticket.Close()
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		ticket.Close()
		return nil, errors.New("upstream response missing body")
	}
	// Keep the lease through the complete stream, including idle intervals.
	b := &guardBody{ReadCloser: resp.Body, ticket: ticket, status: resp.StatusCode, headers: resp.Header.Clone()}
	resp.Body = b
	return resp, nil
}

type guardBody struct {
	io.ReadCloser
	ticket  *Ticket
	status  int
	headers http.Header
	prefix  []byte
	once    sync.Once
}

func (b *guardBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.status >= 400 && len(b.prefix) < 65536 {
		b.prefix = append(b.prefix, p[:min(n, 65536-len(b.prefix))]...)
	}
	return n, err
}
func (b *guardBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		if e := b.ticket.Result(b.status, b.headers, b.prefix); e != nil && err == nil {
			err = fmt.Errorf("protection state result: %w", e)
		}
		b.ticket.Close()
	})
	return err
}
