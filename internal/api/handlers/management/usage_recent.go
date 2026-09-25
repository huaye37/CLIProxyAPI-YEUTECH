package management

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// recentUsageEntry deliberately excludes credentials, prompts and response bodies.
type recentUsageEntry struct {
	RequestID           string    `json:"requestId"`
	TraceID             string    `json:"traceId,omitempty"`
	Provider            string    `json:"provider"`
	RequestedModel      string    `json:"requestedModel"`
	RoutedModel         string    `json:"routedModel"`
	ResponseModel       string    `json:"responseModel,omitempty"`
	RequestedAt         time.Time `json:"requestedAt"`
	LatencyMS           int64     `json:"latencyMs"`
	TTFTMS              int64     `json:"ttftMs,omitempty"`
	Failed              bool      `json:"failed"`
	StatusCode          int       `json:"statusCode,omitempty"`
	InputTokens         int64     `json:"inputTokens"`
	OutputTokens        int64     `json:"outputTokens"`
	ResponseServiceTier string    `json:"responseServiceTier,omitempty"`
}

type recentUsageRing struct {
	mu    sync.RWMutex
	items []recentUsageEntry
	next  int
	limit int
}

var recentUsage = &recentUsageRing{limit: 500}

func usageField(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		return value[:200]
	}
	return value
}

func (r *recentUsageRing) HandleUsage(_ context.Context, record coreusage.Record) {
	if r == nil || r.limit < 1 {
		return
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	entry := recentUsageEntry{
		RequestID: usageField(record.RequestID), TraceID: usageField(record.TraceID),
		Provider: usageField(record.Provider), RequestedModel: usageField(record.Alias),
		RoutedModel: usageField(record.Model), ResponseModel: usageField(record.ResponseModel),
		RequestedAt: requestedAt.UTC(), LatencyMS: record.Latency.Milliseconds(),
		TTFTMS: record.TTFT.Milliseconds(), Failed: record.Failed,
		StatusCode: record.Fail.StatusCode, InputTokens: record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ResponseServiceTier: usageField(record.ResponseServiceTier),
	}
	if entry.RequestedModel == "" {
		entry.RequestedModel = entry.RoutedModel
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) < r.limit {
		r.items = append(r.items, entry)
		return
	}
	r.items[r.next] = entry
	r.next = (r.next + 1) % r.limit
}

func (r *recentUsageRing) snapshot(limit int) []recentUsageEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if limit > len(r.items) {
		limit = len(r.items)
	}
	result := make([]recentUsageEntry, 0, limit)
	for index := 0; index < limit; index++ {
		position := len(r.items) - 1 - index
		if len(r.items) == r.limit {
			position = (r.next + len(r.items) - 1 - index) % len(r.items)
		}
		result = append(result, r.items[position])
	}
	return result
}

// GetRecentUsage reads a bounded in-memory copy without consuming the usage queue.
func (h *Handler) GetRecentUsage(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 100"})
			return
		}
		limit = parsed
	}
	c.JSON(http.StatusOK, gin.H{"records": recentUsage.snapshot(limit), "retention": "memory", "capacity": recentUsage.limit})
}
