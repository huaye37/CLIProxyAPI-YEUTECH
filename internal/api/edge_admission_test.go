package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEdgeAdmissionDrainsWithoutInterruptingAndKeepsControlAvailable(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate.json")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(gate+".tmp", []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(gate+".tmp", gate); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"enabled":true,"maintenance":false}`)
	policy := &edgeAdmission{path: gate}
	router := gin.New()
	router.Use(policy.middleware())
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router.POST("/v1/messages", func(c *gin.Context) {
		if c.GetHeader("X-Hold") == "1" {
			close(entered)
			<-release
		}
		c.Status(200)
	})
	router.GET("/v1/models", func(c *gin.Context) { c.Status(200) })
	router.GET("/v1/responses", func(c *gin.Context) { c.Status(200) })
	first := httptest.NewRequest("POST", "/v1/messages", strings.NewReader("{}"))
	first.Header.Set("X-Hold", "1")
	go func() { defer close(done); router.ServeHTTP(httptest.NewRecorder(), first) }()
	<-entered
	write(`{"enabled":true,"maintenance":true}`)
	if state := policy.snapshot(); state["accepting"] != false || state["activeRequests"] != 1 {
		t.Fatal(state)
	}
	reader := &unreadBody{}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", "/v1/messages", reader))
	if response.Code != 503 || reader.read || !strings.Contains(response.Body.String(), "edge_maintenance") {
		t.Fatal("new request was admitted or read")
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/v1/models", nil))
	if response.Code != 200 {
		t.Fatal("catalog unavailable during drain")
	}
	ws := httptest.NewRequest("GET", "/v1/responses", nil)
	ws.Header.Set("Upgrade", "websocket")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, ws)
	if response.Code != 503 {
		t.Fatal("websocket bypassed gate")
	}
	close(release)
	<-done
	if policy.snapshot()["activeRequests"] != 0 {
		t.Fatal("active count leaked")
	}
	write(`{"enabled":true,"maintenance":false}`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", "/v1/messages", strings.NewReader("{}")))
	if response.Code != 200 {
		t.Fatal("channel did not recover")
	}
}

func TestEdgeAdmissionOptInAndFailClosed(t *testing.T) {
	t.Setenv("YEUTECH_EDGE_CONTROL_FILE", "")
	if newEdgeAdmission().snapshot() != nil {
		t.Fatal("gate must be opt-in")
	}
	gate := filepath.Join(t.TempDir(), "gate.json")
	policy := &edgeAdmission{path: gate}
	for _, raw := range []string{"", "{}", "invalid", `{"enabled":false,"maintenance":false}`} {
		if raw != "" {
			if err := os.WriteFile(gate, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if policy.snapshot()["accepting"] != false {
			t.Fatalf("accepted invalid or disabled gate: %q", raw)
		}
	}
}
