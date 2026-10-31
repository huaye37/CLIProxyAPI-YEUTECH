package management

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestRecentUsageKeepsNewestWithoutSensitiveFields(t *testing.T) {
	ring := &recentUsageRing{limit: 2}
	for _, model := range []string{"first", "second", "third"} {
		ring.HandleUsage(context.Background(), coreusage.Record{
			RequestID: model, Alias: model, Model: "route-" + model,
			ResponseModel: "served-" + model, AuthIndex: "account-1", APIKey: "never-expose",
			ServiceTier: "priority", ResponseServiceTier: "default",
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
	if got[0].RequestedServiceTier != "priority" || got[0].ResponseServiceTier != "default" {
		t.Fatalf("recent service tiers = %+v", got[0])
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

func TestRecentStructuredUsageOwnsCountersAndPreservesZero(t *testing.T) {
	zero, count, invalid := int64(0), int64(3), int64(-1)
	ring := &recentUsageRing{limit: 2}
	ring.HandleUsage(context.Background(), coreusage.Record{Detail: coreusage.Detail{
		ImageCount: &count, TextInputTokens: &invalid,
		ToolUsageDetails: &coreusage.ToolUsageDetails{WebSearchCalls: &zero, Extra: map[string]int64{"private": 42}},
	}})
	count = 9
	got := ring.snapshot(1)[0].StructuredUsage
	if got["imageCount"] != 3 || got["webSearchCalls"] != 0 || len(got) != 2 {
		t.Fatalf("counters = %+v", got)
	}
	got["imageCount"] = 100
	if ring.snapshot(1)[0].StructuredUsage["imageCount"] != 3 {
		t.Fatal("snapshot aliases ring storage")
	}
}

func TestRecentUsageV8ExposesRequestedTierWithoutChangingLegacyContract(t *testing.T) {
	recentUsage = &recentUsageRing{limit: 2}
	t.Cleanup(func() { recentUsage = &recentUsageRing{limit: 500} })
	recentUsage.HandleUsage(context.Background(), coreusage.Record{RequestID: "tier", ServiceTier: "priority", ResponseServiceTier: "default"})

	request := func(path string, handler gin.HandlerFunc) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", path, nil)
		handler(c)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return body
	}

	legacy := request("/v0/management/usage-recent", (&Handler{}).GetRecentUsage)
	legacyRecord := legacy["records"].([]any)[0].(map[string]any)
	if _, exists := legacyRecord["requestedServiceTier"]; exists {
		t.Fatalf("legacy response unexpectedly changed: %+v", legacyRecord)
	}
	v8 := request("/v8/management/observability/usage/recent", (&Handler{}).GetRecentUsageV8)
	v8Record := v8["records"].([]any)[0].(map[string]any)
	if v8Record["requestedServiceTier"] != "priority" || v8Record["responseServiceTier"] != "default" {
		t.Fatalf("v8 tiers = %+v", v8Record)
	}
}
