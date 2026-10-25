package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCodexClientSnapshotSurvivesGinReuse(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "codex"}[codex], func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if codex {
				c.Request.Header.Set("User-Agent", "codex_cli_rs/fixture")
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := snapshotCodexClientRequest(context.WithValue(parent, "gin", c))
			// Reuse the original pooled object concurrently, without reading it from
			// the accounting context. The race detector guards this lifetime boundary.
			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := 0; i < 1000; i++ {
					c.Request = httptest.NewRequest(http.MethodPost, "/next", nil)
					if !codex {
						c.Request.Header.Set("User-Agent", "codex-desktop/next")
					}
				}
			}()
			for i := 0; i < 1000; i++ {
				if got := IsCodexClientRequest(ctx); got != codex {
					t.Errorf("caller classification = %v, want %v", got, codex)
					break
				}
			}
			<-done
			if !IsCodexClientRequest(ctx, http.Header{"User-Agent": {"codex-rs/explicit"}}) {
				t.Error("explicit client headers must retain their existing precedence")
			}
			cancel()
			if ctx.Err() != context.Canceled {
				t.Error("snapshot lost parent cancellation")
			}
		})
	}
	if snapshotCodexClientRequest(nil) != nil {
		t.Error("nil context behavior changed")
	}
}
