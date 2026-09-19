package websubscription

import (
	"errors"
	"testing"
	"time"
)

func TestAccountSchedulerBalancesAccounts(t *testing.T) {
	now := time.Unix(100, 0)
	scheduler := newAccountScheduler(func() time.Time { return now })
	for _, account := range []Account{
		{ID: "account-a", DriverID: "gemini-web", Enabled: true, Weight: 1},
		{ID: "account-b", DriverID: "gemini-web", Enabled: true, Weight: 1},
	} {
		if err := scheduler.Upsert(account); err != nil {
			t.Fatal(err)
		}
	}
	first, err := scheduler.Acquire("gemini-web", "gemini-web-auto")
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.Acquire("gemini-web", "gemini-web-auto")
	if err != nil {
		t.Fatal(err)
	}
	if first.AccountID == second.AccountID {
		t.Fatalf("leases used the same account: %q", first.AccountID)
	}
	first.Release(AccountOutcome{})
	second.Release(AccountOutcome{})
}

func TestAccountSchedulerIsolatesModelCooldown(t *testing.T) {
	now := time.Unix(200, 0)
	scheduler := newAccountScheduler(func() time.Time { return now })
	if err := scheduler.Upsert(Account{ID: "account-a", DriverID: "chatgpt-web", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	lease, err := scheduler.Acquire("chatgpt-web", "chatgpt-web-auto")
	if err != nil {
		t.Fatal(err)
	}
	lease.Release(AccountOutcome{Failure: FailureRateLimit, RetryAt: now.Add(time.Minute)})
	if _, err = scheduler.Acquire("chatgpt-web", "chatgpt-web-auto"); !errors.Is(err, ErrNoAccountAvailable) {
		t.Fatalf("cooldown error = %v", err)
	}
	if _, err = scheduler.Acquire("chatgpt-web", "chatgpt-web-reasoning"); err != nil {
		t.Fatalf("sibling model was cooled down: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err = scheduler.Acquire("chatgpt-web", "chatgpt-web-auto"); err != nil {
		t.Fatalf("expired cooldown did not reopen: %v", err)
	}
}

func TestAccountSchedulerAuthenticationFailureDisablesOnlyAccount(t *testing.T) {
	scheduler := newAccountScheduler(func() time.Time { return time.Unix(300, 0) })
	for _, id := range []string{"account-a", "account-b"} {
		if err := scheduler.Upsert(Account{ID: id, DriverID: "gemini-web", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := scheduler.Acquire("gemini-web", "gemini-web-auto")
	if err != nil {
		t.Fatal(err)
	}
	disabledID := lease.AccountID
	lease.Release(AccountOutcome{Failure: FailureAuthentication, Reason: "signed_out"})

	next, err := scheduler.Acquire("gemini-web", "gemini-web-auto")
	if err != nil {
		t.Fatal(err)
	}
	if next.AccountID == disabledID {
		t.Fatalf("disabled account %q was selected", disabledID)
	}
	snapshots := scheduler.Snapshot()
	for _, snapshot := range snapshots {
		if snapshot.ID == disabledID && (snapshot.Enabled || snapshot.DisabledReason != "signed_out") {
			t.Fatalf("unexpected disabled snapshot: %#v", snapshot)
		}
	}
}

func TestAccountLeaseReleaseIsIdempotent(t *testing.T) {
	scheduler := newAccountScheduler(func() time.Time { return time.Unix(400, 0) })
	if err := scheduler.Upsert(Account{ID: "account-a", DriverID: "gemini-web", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	lease, err := scheduler.Acquire("gemini-web", "gemini-web-auto")
	if err != nil {
		t.Fatal(err)
	}
	lease.Release(AccountOutcome{})
	lease.Release(AccountOutcome{})
	if got := scheduler.Snapshot()[0].InFlight; got != 0 {
		t.Fatalf("in flight = %d", got)
	}
}
