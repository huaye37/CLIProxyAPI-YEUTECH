package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestResponseCompressionPreservesCodexRateLimitDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, accept, agent, originator, encoding string
		status                                                  int
		compressed                                              bool
	}{
		{"codex_gzip_429", "POST", "/v1/responses", "text/event-stream", "codex_cli_rs/fixture", "", "gzip", 429, false},
		{"codex_zstd_429", "POST", "/v1/responses", "text/event-stream", "codex_cli_rs/fixture", "", "zstd", 429, false},
		{"originator_429", "POST", "/v1/responses", "text/event-stream", "", "codex-tui/fixture", "gzip", 429, false},
		{"ordinary_429", "POST", "/v1/responses", "text/event-stream", "ordinary-client", "", "gzip", 429, true},
		{"non_stream_429", "POST", "/v1/responses", "application/json", "codex_cli_rs/fixture", "", "gzip", 429, true},
		{"other_path_429", "POST", "/v1/chat/completions", "text/event-stream", "codex_cli_rs/fixture", "", "gzip", 429, true},
		{"other_method_429", "GET", "/v1/responses", "text/event-stream", "codex_cli_rs/fixture", "", "gzip", 429, true},
		{"other_error", "POST", "/v1/responses", "text/event-stream", "codex_cli_rs/fixture", "", "gzip", 503, true},
		{"successful_response", "POST", "/v1/responses", "text/event-stream", "codex_cli_rs/fixture", "", "gzip", 200, true},
		{"identity_refused", "POST", "/v1/responses", "text/event-stream", "codex_cli_rs/fixture", "", "gzip, identity;q=0", 429, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"error":{"code":"model_cooldown","message":"` + strings.Repeat("fixture ", 256) + `"}}`
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(&config.Config{}))
			engine.Any(tc.path, func(c *gin.Context) {
				c.Header("Retry-After", "60")
				c.Header("Content-Type", "application/json")
				c.Status(tc.status)
				_, _ = c.Writer.WriteString(body)
			})
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Accept", tc.accept)
			req.Header.Set("Accept-Encoding", tc.encoding)
			req.Header.Set("User-Agent", tc.agent)
			req.Header.Set("Originator", tc.originator)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			encoding := recorder.Header().Get("Content-Encoding")
			if (encoding != "") != tc.compressed {
				t.Fatalf("encoding=%q, compressed=%v", encoding, tc.compressed)
			}
			if recorder.Code != tc.status || recorder.Header().Get("Retry-After") != "60" {
				t.Fatal("status or retry metadata changed")
			}
			if string(compressionTestBody(t, encoding, recorder.Body.Bytes())) != body {
				t.Fatal("response body changed")
			}
		})
	}
}
