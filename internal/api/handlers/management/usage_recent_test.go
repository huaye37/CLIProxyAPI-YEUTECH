package management

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRecentUsageKeepsNewestWithoutSensitiveFields(t *testing.T) {
	ring := &recentUsageRing{limit: 2}
	for _, model := range []string{"first", "second", "third"} {
		ring.HandleUsage(context.Background(), coreusage.Record{
			RequestID: model, Alias: model, Model: "route-" + model,
			ResponseModel: "served-" + model, AuthIndex: "account-1", APIKey: "never-expose",
			RequestedAt: time.Unix(100, 0), Latency: 1500 * time.Millisecond,
			Detail: coreusage.Detail{InputTokens: 179540, CachedTokens: 120000, CacheReadTokens: 120000},
		})
	}
	got := ring.snapshot(5)
	if len(got) != 2 || got[0].RequestID != "third" || got[1].RequestID != "second" {
		t.Fatalf("recent order = %+v", got)
	}
	if got[0].LatencyMS != 1500 || got[0].ResponseModel != "served-third" {
		t.Fatalf("recent fields = %+v", got[0])
	}
	if got[0].AuthIndex != "account-1" {
		t.Fatalf("recent account index = %q", got[0].AuthIndex)
	}
	if got[0].InputTokens != 179540 || got[0].CacheReadTokens != 120000 {
		t.Fatalf("recent cache detail = %+v", got[0])
	}
}

func TestGetRecentUsageRejectsOversizedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v0/management/usage-recent?limit=101", nil)
	(&Handler{}).GetRecentUsage(c)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
