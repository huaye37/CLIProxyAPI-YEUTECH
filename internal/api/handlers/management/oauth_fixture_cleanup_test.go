package management

import "testing"

// Completed OAuth sessions intentionally remain available for recovery status.
// Remove only each test's synthetic identities after it has finished.
func cleanupOAuthFixtureStates(t *testing.T, states ...string) {
	t.Helper()
	t.Cleanup(func() {
		oauthSessions.mu.Lock()
		defer oauthSessions.mu.Unlock()
		for _, state := range states {
			delete(oauthSessions.sessions, state)
		}
	})
}
