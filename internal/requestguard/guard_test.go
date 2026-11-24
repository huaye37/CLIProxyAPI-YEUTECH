package requestguard

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/poollease"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func poolLeaseForTest(g Guard, account string) *poollease.Lease {
	return poollease.New(filepath.Join(g.Root, "account-"+hash(account)+".lock"))
}

func fixture(t *testing.T) (Guard, *time.Time) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	return Guard{Root: filepath.Join(t.TempDir(), "state"), Now: func() time.Time { return now }}, &now
}
func begin(t *testing.T, g Guard, account, request string) *Ticket {
	t.Helper()
	x, e := g.Begin(context.Background(), account, request, "generate")
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func denied(t *testing.T, err error, reason string) {
	t.Helper()
	var d *Denied
	if !errors.As(err, &d) || !strings.Contains(d.Reason, reason) {
		t.Fatalf("wanted %s, got %v", reason, err)
	}
}

func TestSharedAttemptsIntervalAndWindow(t *testing.T) {
	g, now := fixture(t)
	a := begin(t, g, "email", "request")
	a.Close()
	_, err := g.Begin(context.Background(), "email", "different", "generate")
	denied(t, err, "cooling")
	*now = now.Add(3 * time.Second)
	other := Guard{Root: g.Root, Now: g.Now}
	a = begin(t, other, "email", "request")
	a.Close()
	*now = now.Add(3 * time.Second)
	_, err = other.Begin(context.Background(), "email", "request", "generate")
	denied(t, err, "budget")
	a = begin(t, other, "other-email", "request")
	a.Close()
	*now = now.Add(Window)
	a = begin(t, other, "email", "request")
	a.Close()
}

func TestTerminalPauseAndRetryInfo(t *testing.T) {
	g, now := fixture(t)
	a := begin(t, g, "a", "one")
	err := a.Result(429, http.Header{"Retry-After": []string{"120"}}, []byte(`{"error":{"details":[{"retryDelay":"300s"}]}}`))
	a.Close()
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(3 * time.Second)
	_, err = g.Begin(context.Background(), "a", "two", "generate")
	denied(t, err, "cooling")
	a = begin(t, g, "b", "one")
	if err = a.Result(403, nil, []byte(`{"error":{"details":[{"reason":"TOS_VIOLATION"}]}}`)); err != nil {
		t.Fatal(err)
	}
	a.Close()
	_, err = (Guard{Root: g.Root, Now: g.Now}).Begin(context.Background(), "c", "new", "generate")
	denied(t, err, "provider paused")
	*now = now.Add(6*time.Hour + time.Second)
	a = begin(t, g, "c", "new")
	a.Close()
}

func TestRegionRefusalStopsAccountNotOtherAccounts(t *testing.T) {
	g, now := fixture(t)
	a := begin(t, g, "a", "one")
	if err := a.Result(400, nil, []byte(`{"error":{"message":"User location is not supported for the API use."}}`)); err != nil {
		t.Fatal(err)
	}
	a.Close()
	*now = now.Add(time.Minute)
	_, err := g.Begin(context.Background(), "a", "other", "generate")
	denied(t, err, "cooling")
	a = begin(t, g, "b", "one")
	a.Close()
}

func TestCorruptStateFailsClosedAndContainsNoSecrets(t *testing.T) {
	g, _ := fixture(t)
	a := begin(t, g, "secret@example.com", "private-prompt")
	a.Close()
	b, e := os.ReadFile(filepath.Join(g.Root, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte("secret")) || bytes.Contains(b, []byte("private")) {
		t.Fatal("private data persisted")
	}
	if e = os.WriteFile(filepath.Join(g.Root, "state.json"), []byte(`broken`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = g.Begin(context.Background(), "other", "new", "generate"); e == nil {
		t.Fatal("corrupt state accepted")
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportStreamPayloadAndCancellation(t *testing.T) {
	g, _ := fixture(t)
	calls := 0
	rt := &Transport{Guard: g, Account: "a", Base: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if string(b) != "unchanged" {
			t.Fatal("payload changed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: text\n\n"))}, nil
	})}
	ctx := WithFingerprint(context.Background(), "model", "", []byte("original"))
	r, _ := http.NewRequestWithContext(ctx, "POST", "https://example.invalid/v1internal:streamGenerateContent", strings.NewReader("unchanged"))
	resp, e := rt.RoundTrip(r)
	if e != nil {
		t.Fatal(e)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	r2, _ := http.NewRequestWithContext(cancelCtx, "POST", r.URL.String(), strings.NewReader("unchanged"))
	if _, e = rt.RoundTrip(r2); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancelled request: %v", e)
	}
	b, e := io.ReadAll(resp.Body)
	if e != nil || string(b) != "data: text\n\n" {
		t.Fatal("stream changed", e)
	}
	// EOF does not relinquish the account until the executor has consumed and
	// classified the final event and closes the response body.
	l := poolLeaseForTest(g, "a")
	if ok, e := l.TryAcquire(); e != nil || ok {
		t.Fatal("EOF released active lease", e)
	}
	l.Close()
	if e = resp.Body.Close(); e != nil {
		t.Fatal(e)
	}
	l = poolLeaseForTest(g, "a")
	if ok, e := l.TryAcquire(); e != nil || !ok {
		t.Fatal("close did not release", e)
	}
	l.Close()
	if calls != 1 {
		t.Fatalf("unexpected upstream calls: %d", calls)
	}
}

func TestFingerprintSurvivesInternalConversion(t *testing.T) {
	ctx := WithFingerprint(context.Background(), "model", "", []byte("original"))
	a := ctx.Value(fingerprintKey{})
	ctx = WithFingerprint(ctx, "other", "", []byte("translated"))
	if ctx.Value(fingerprintKey{}) != a {
		t.Fatal("conversion reset attempt identity")
	}
}

// Use separate guard instances pointing at one directory, like the three NAS
// containers. Keep the returned tickets open so the requests really overlap.
func TestSharedAccountAndProviderConcurrency(t *testing.T) {
	g, now := fixture(t)
	replica := func() Guard { return Guard{Root: g.Root, Now: g.Now} }
	a := begin(t, g, "shared", "first")
	defer a.Close()
	*now = now.Add(Interval)
	b := begin(t, replica(), "shared", "second")
	defer b.Close()
	*now = now.Add(Interval)
	blocked := func(account string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
		defer cancel()
		if tk, err := replica().Begin(ctx, account, "extra", "generate"); tk != nil || !errors.Is(err, context.DeadlineExceeded) {
			if tk != nil {
				tk.Close()
			}
			t.Fatalf("expected bounded wait for %s: %v", account, err)
		}
	}
	blocked("shared") // still one free provider slot, but no free account slot
	c := begin(t, replica(), "second-account", "third")
	defer c.Close()
	blocked("new-account") // no provider slots, even with a free account
	a.Close()
	d := begin(t, replica(), "shared", "replacement")
	defer d.Close()
	blocked("new-account")
	c.Close()
	begin(t, replica(), "new-account", "after-release").Close()
}

func TestAdmissionProcessHelper(t *testing.T) {
	root := os.Getenv("BOUNDED_GUARD_TEST_ROOT")
	if root == "" {
		return
	}
	g := Guard{Root: root, Now: func() time.Time { return time.Unix(1900000000, 0) }}
	tk, err := g.Begin(context.Background(), os.Getenv("BOUNDED_GUARD_TEST_ACCOUNT"), "child", "generate")
	if err != nil {
		t.Fatal(err)
	}
	defer tk.Close()
	fmt.Fprintln(os.Stdout, "holding")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestAdmissionAcrossThreeProcesses(t *testing.T) {
	g, now := fixture(t)
	a := begin(t, g, "shared", "parent")
	defer a.Close()
	start := func(account string) *exec.Cmd {
		t.Helper()
		child := exec.Command(os.Args[0], "-test.run=^TestAdmissionProcessHelper$")
		child.Env = append(os.Environ(), "BOUNDED_GUARD_TEST_ROOT="+g.Root, "BOUNDED_GUARD_TEST_ACCOUNT="+account)
		reader, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		writer, err := child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			writer.Close()
			if child.ProcessState == nil {
				child.Process.Kill()
				child.Wait()
			}
		})
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err != nil || line != "holding\n" {
			t.Fatalf("child admission: %q %v", line, err)
		}
		return child
	}
	second := start("shared")
	start("other")
	*now = time.Unix(1900000005, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if tk, err := g.Begin(ctx, "third", "fourth", "generate"); tk != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fourth crossed shared cap: %v", err)
	}
	if err := second.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = second.Wait()
	begin(t, g, "shared", "after-crash").Close()
}

func TestAccountLeaseAcrossProcessesAndCrash(t *testing.T) {
	g, _ := fixture(t)
	a := begin(t, g, "a", "parent")
	child := exec.Command(os.Args[0], "-test.run=^TestGuardChild$")
	child.Env = append(os.Environ(), "REQUEST_GUARD_CHILD="+g.Root)
	out, e := child.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	in, e := child.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	r := bufio.NewReader(out)
	line, e := r.ReadString('\n')
	if e != nil || line != "blocked\n" {
		t.Fatalf("cross-process exclusion: %s %v", line, e)
	}
	a.Close()
	in.Write([]byte("go\n"))
	line, e = r.ReadString('\n')
	if e != nil || line != "acquired\n" {
		t.Fatalf("cross-process release: %s %v", line, e)
	}
	child.Process.Kill()
	child.Wait()
	l := poolLeaseForTest(g, "a")
	defer l.Close()
	if ok, e := l.TryAcquire(); e != nil || !ok {
		t.Fatal("dead process retained account", e)
	}
}
func TestGuardChild(t *testing.T) {
	root := os.Getenv("REQUEST_GUARD_CHILD")
	if root == "" {
		return
	}
	g := Guard{Root: root}
	l := poolLeaseForTest(g, "a")
	if ok, e := l.TryAcquire(); e != nil || ok {
		t.Fatal("acquired parent's lock", e)
	}
	os.Stdout.WriteString("blocked\n")
	bufio.NewReader(os.Stdin).ReadString('\n')
	if ok, e := l.TryAcquire(); e != nil || !ok {
		t.Fatal("could not acquire released lock", e)
	}
	os.Stdout.WriteString("acquired\n")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
