package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const edgeAdmissionVersion = "yeutech-edge-admission-v1"

// edgeAdmission serializes gate observations and request admission. Once the
// controller closes the gate, a health snapshot cannot miss a newly admitted
// request whose gate check happened before the close.
type edgeAdmission struct {
	mu     sync.Mutex
	path   string
	active int
}

func newEdgeAdmission() *edgeAdmission {
	return &edgeAdmission{path: strings.TrimSpace(os.Getenv("YEUTECH_EDGE_CONTROL_FILE"))}
}

func (e *edgeAdmission) stateLocked() (bool, string) {
	raw, err := os.ReadFile(e.path)
	if err != nil || len(raw) > 4096 {
		return false, "control_unavailable"
	}
	var state struct {
		Enabled     *bool `json:"enabled"`
		Maintenance *bool `json:"maintenance"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Enabled == nil || state.Maintenance == nil {
		return false, "control_invalid"
	}
	if *state.Maintenance {
		return false, "maintenance"
	}
	if !*state.Enabled {
		return false, "disabled"
	}
	return true, ""
}

func (e *edgeAdmission) snapshot() gin.H {
	if e == nil || e.path == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	accepting, reason := e.stateLocked()
	return gin.H{"version": edgeAdmissionVersion, "accepting": accepting, "reason": reason, "activeRequests": e.active}
}

func (e *edgeAdmission) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		inference := c.Request.Method == http.MethodPost || strings.EqualFold(c.GetHeader("Upgrade"), "websocket")
		if e == nil || e.path == "" || !inference || !isExampleAPIKeySafeModeProxyPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		e.mu.Lock()
		accepting, reason := e.stateLocked()
		if accepting {
			e.active++
		}
		e.mu.Unlock()
		if !accepting {
			c.Header("Retry-After", "5")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"code": "edge_" + reason, "type": "server_error",
				"message": "This model channel is temporarily unavailable; retry later", "retryable": true,
			}})
			return
		}
		defer func() { e.mu.Lock(); e.active--; e.mu.Unlock() }()
		c.Next()
	}
}
