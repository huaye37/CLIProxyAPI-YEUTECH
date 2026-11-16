package helps

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestguard"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func AntigravityProtectionContext(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	return requestguard.WithFingerprint(ctx, req.Model, opts.Alt, payload)
}

func antigravityGuard(cfg *config.Config) requestguard.Guard {
	return requestguard.Guard{Root: filepath.Join(cfg.AuthDir, ".yeutech-antigravity-protection")}
}

func ProtectAntigravityHTTPClient(client *http.Client, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	if client == nil || cfg == nil || !cfg.Antigravity.RequestProtection {
		return client
	}
	account := ""
	if auth != nil {
		// A duplicate credential file for the same account must use the same lease.
		if email, ok := auth.Metadata["email"].(string); ok {
			account = strings.ToLower(strings.TrimSpace(email))
		}
		if account == "" {
			account = auth.ID
		}
	}
	copyClient := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copyClient.Transport = &requestguard.Transport{Base: base, Guard: antigravityGuard(cfg), Account: account}
	// Redirects are not an extra attempt to a different provider endpoint.
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &copyClient
}

func PauseAntigravityProtection(cfg *config.Config) error {
	if cfg == nil || !cfg.Antigravity.RequestProtection {
		return nil
	}
	return antigravityGuard(cfg).Pause(6 * time.Hour)
}
