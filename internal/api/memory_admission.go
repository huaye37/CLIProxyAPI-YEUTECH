package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

// Wire bytes are not heap bytes: JSON translation and replay retain several
// copies. These limits are paired with a 4 GiB container and 3 GiB GOMEMLIMIT.
const (
	memoryAdmissionVersion       = "yeutech-memory-v1"
	memoryBodyLimit        int64 = 256 << 20
	memoryLargeBody        int64 = 32 << 20
	memoryMinReservation   int64 = 1 << 20
	memoryMaxRequests            = 8
)

type memoryAdmission struct {
	mu          sync.Mutex
	enabled     bool
	active      int
	bytes       int64
	large       bool
	maxRequests int
}

func (m *memoryAdmission) requestLimit() int {
	if m.maxRequests > 0 && m.maxRequests < memoryMaxRequests {
		return m.maxRequests
	}
	return memoryMaxRequests
}

func (m *memoryAdmission) snapshot() gin.H {
	if m == nil || !m.enabled {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return gin.H{"version": memoryAdmissionVersion, "maxBodyBytes": memoryBodyLimit,
		"bodyBudgetBytes": memoryBodyLimit, "maxConcurrentRequests": m.requestLimit(),
		"largeBodyThresholdBytes": memoryLargeBody, "maxConcurrentLargeRequests": 1,
		"activeRequests": m.active, "reservedBodyBytes": m.bytes}
}

func (m *memoryAdmission) acquire(size int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active >= m.requestLimit() || size > memoryBodyLimit-m.bytes || (size >= memoryLargeBody && m.large) {
		return false
	}
	m.active++
	m.bytes += size
	if size >= memoryLargeBody {
		m.large = true
	}
	return true
}

func (m *memoryAdmission) release(size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.bytes -= size
	if size >= memoryLargeBody {
		m.large = false
	}
}

func newMemoryAdmission() *memoryAdmission {
	// Edge deployments may reduce admission without raising the tested ceiling.
	limit, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("YEUTECH_MEMORY_MAX_REQUESTS")))
	return &memoryAdmission{enabled: os.Getenv("YEUTECH_MEMORY_GUARD") == "1", maxRequests: limit}
}

func (m *memoryAdmission) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		if !m.enabled || r.Method != http.MethodPost || !isExampleAPIKeySafeModeProxyPath(r.URL.Path) {
			c.Next()
			return
		}
		// Unknown/chunked bodies reserve the entire budget before any read.
		size := r.ContentLength
		if size < 0 || (size == 0 && r.Body != nil && r.Body != http.NoBody) {
			size = memoryBodyLimit
		}
		if size > memoryBodyLimit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"code": "request_body_too_large", "message": "Request body exceeds the 256 MiB transport limit"}})
			return
		}
		// Existing handlers bound zstd decoding via the context limit below.
		// Charge decoded worst-case bytes, never compressed Content-Length.
		if encoding := strings.TrimSpace(r.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
			size = memoryBodyLimit
		}
		if size < memoryMinReservation {
			size = memoryMinReservation
		}
		if !m.acquire(size) {
			c.Header("Retry-After", "2")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "proxy_memory_busy", "type": "server_error", "message": "Proxy request memory budget is busy; retry after an active request finishes", "retryable": true}})
			return
		}
		defer m.release(size)
		// Also constrain a chunked body, or a custom reader that exceeds its
		// declared size, before the request logger or protocol decoder reads it.
		limit := size
		if n, ok := c.Get(handlers.MaxRequestBodyBytesContextKey); ok {
			if configured, ok := n.(int64); ok && configured > 0 && configured < limit {
				limit = configured
			}
		}
		c.Set(handlers.MaxRequestBodyBytesContextKey, limit)
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(c.Writer, r.Body, limit)
		}
		c.Next()
	}
}
