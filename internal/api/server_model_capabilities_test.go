package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestModelCapabilitiesHandlerUsesSafeCatalogBudget(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-budget"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID: "gpt-5.3-codex-spark", Object: "model", OwnedBy: "openai", Type: "openai",
		ContextLength: 128000, MaxCompletionTokens: 128000,
		SupportedInputModalities:  []string{"text"}, SupportedOutputModalities: []string{"text"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	target := requestModelCapability(t, "gpt-5.3-codex-spark")
	if target["max_output_tokens"] != float64(10000) {
		t.Fatalf("max_output_tokens = %v, want safe catalog budget 10000", target["max_output_tokens"])
	}
	if target["capability_status"] != "ready" || target["selectable"] != true {
		t.Fatalf("capability readiness = %#v, want ready/selectable", target)
	}
}

func TestModelCapabilitiesHandlerEnrichesStaticGeminiMetadata(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-gemini"
	modelRegistry.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{{
		ID: "gemini-3.1-flash-image", Object: "model", OwnedBy: "antigravity", Type: "antigravity",
		SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"text", "image"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	target := requestModelCapability(t, "gemini-3.1-flash-image")
	if target["context_length"] != float64(1048576) || target["max_output_tokens"] != float64(65536) {
		t.Fatalf("static metadata was not enriched: %#v", target)
	}
	if target["capability_status"] != "ready" || target["selectable"] != false {
		t.Fatalf("image-output model = %#v, want complete but not conversation-selectable", target)
	}
}

func TestModelCapabilitiesHandlerKeepsUnknownModelsIncomplete(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-incomplete"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "newly-discovered-model", Object: "model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	target := requestModelCapability(t, "newly-discovered-model")
	if target["capability_status"] != "incomplete" || target["selectable"] != false {
		t.Fatalf("unknown model = %#v, want visible but incomplete", target)
	}
	input, ok := target["supported_input_modalities"].([]any)
	if !ok || len(input) != 0 {
		t.Fatalf("supported_input_modalities = %#v, want an empty array", target["supported_input_modalities"])
	}
	output, ok := target["supported_output_modalities"].([]any)
	if !ok || len(output) != 0 {
		t.Fatalf("supported_output_modalities = %#v, want an empty array", target["supported_output_modalities"])
	}
}

func requestModelCapability(t *testing.T, modelID string) map[string]any {
	t.Helper()
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/model-capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("model capability response must not be cached")
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, model := range response.Data {
		if model["id"] == modelID {
			return model
		}
	}
	t.Fatalf("model %q missing from response", modelID)
	return nil
}
