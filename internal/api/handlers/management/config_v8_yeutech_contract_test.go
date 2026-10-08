package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// A manager client-settings write must not change model limits, provider
// payload rules, or retry policy when YAML is normalized.
func TestConfigV8NormalizationRetainsYEUTECHConsumerContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	filename := filepath.Join(t.TempDir(), "config.yaml")
	raw := `# Preserve the consumer contracts.
config-version: 8
server: {port: 8317}
client: {codex: {enable-apply-patch: true, optimize-multi-agent-v2: false}}
oauth:
  settings:
    codex: [{name: gpt-6-astra, max-context-length: 500000}]
    antigravity: [{name: gemini-pro-agent, max-context-length: 1048576}]
requests:
  payload:
    override:
      - models: [{name: "gpt-*", protocol: codex}]
        params: {"reasoning.effort": high}
routing: {retry: {request-retry: 0, max-retry-credentials: 1, max-retry-interval: 0}}
`
	if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(filename)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: filename}
	router := gin.New()
	router.GET("/v8/management/config/*path", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	request := func(method, target, body string) any {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, target, strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, target, recorder.Code, recorder.Body.String())
		}
		var result any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	paths := []string{"oauth/settings", "requests/payload", "routing/retry"}
	before := make(map[string]any)
	for _, path := range paths {
		before[path] = request(http.MethodGet, "/v8/management/config/"+path, "")
	}
	request(http.MethodPut, "/v8/management/config/client/codex", `{"enable-apply-patch":true,"optimize-multi-agent-v2":true}`)
	for _, path := range paths {
		if after := request(http.MethodGet, "/v8/management/config/"+path, ""); !reflect.DeepEqual(before[path], after) {
			t.Fatalf("normalization changed %s: before=%v after=%v", path, before[path], after)
		}
	}
	saved, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "# Preserve the consumer contracts.") || strings.Contains(string(saved), `"reasoning.effort":`) || strings.Contains(string(saved), "codex: [") {
		t.Fatalf("comment or collection normalization regressed: %s", saved)
	}
	if err := config.ValidateV8Config(saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(filename)
	if err != nil || !loaded.Client.Codex.EnableApplyPatch || !loaded.Client.Codex.OptimizeMultiAgentV2 || loaded.RequestRetry != 0 {
		t.Fatalf("persisted consumer/retry settings changed: %v", err)
	}
}
