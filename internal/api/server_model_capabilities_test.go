package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestModelCapabilitiesHandlerReturnsSafeRuntimeMetadata(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities"
	modelRegistry.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID:                        "test-capability-model",
		Object:                    "model",
		OwnedBy:                   "anthropic",
		Type:                      "claude",
		DisplayName:               "Test Capability Model",
		ContextLength:             200000,
		MaxContextLength:          180000,
		MaxCompletionTokens:       64000,
		CapabilityMaxOutputTokens: 10000,
		SupportedParameters:       []string{"reasoning_effort"},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "high"},
		},
		Config: &registry.ModelConfig{
			OverrideHeader: map[string]string{"authorization": "must-not-leak"},
		},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/model-capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var response struct {
		Object     string           `json:"object"`
		Generation uint64           `json:"generation"`
		Data       []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Object != "list" {
		t.Fatalf("object = %q, want list", response.Object)
	}

	var target map[string]any
	for _, model := range response.Data {
		if model["id"] == "test-capability-model" {
			target = model
			break
		}
	}
	if target == nil {
		t.Fatalf("test model missing from response: %s", recorder.Body.String())
	}
	if target["context_length"] != float64(180000) {
		t.Fatalf("context_length = %v, want 180000", target["context_length"])
	}
	if target["max_input_tokens"] != float64(180000) {
		t.Fatalf("max_input_tokens = %v, want 180000", target["max_input_tokens"])
	}
	if target["max_output_tokens"] != float64(10000) {
		t.Fatalf("max_output_tokens = %v, want safe capability budget 10000", target["max_output_tokens"])
	}
	if target["capability_status"] != "ready" || target["selectable"] != true {
		t.Fatalf("capability readiness = %#v, want ready/selectable", target)
	}
	if _, exists := target["config"]; exists {
		t.Fatal("internal model config leaked")
	}
	if _, exists := target["override_header"]; exists {
		t.Fatal("internal override headers leaked")
	}
}

func TestModelCapabilitiesHandlerEnrichesStaticGeminiMetadata(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-gemini"
	modelRegistry.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{{
		ID:                        "gemini-3.1-flash-image",
		Object:                    "model",
		OwnedBy:                   "antigravity",
		Type:                      "antigravity",
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text", "image"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/model-capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, model := range response.Data {
		if model["id"] != "gemini-3.1-flash-image" {
			continue
		}
		if model["context_length"] != float64(1048576) || model["max_output_tokens"] != float64(65536) {
			t.Fatalf("static metadata was not enriched: %#v", model)
		}
		if model["capability_status"] != "ready" || model["selectable"] != false {
			t.Fatalf("image-output model = %#v, want complete but not conversation-selectable", model)
		}
		return
	}
	t.Fatal("gemini-3.1-flash-image missing from response")
}

func TestModelCapabilitiesHandlerBoundsFullWindowOutputForConversation(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-full-window"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:                        "full-window-output-model",
		Object:                    "model",
		ContextLength:             128000,
		MaxCompletionTokens:       128000,
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/model-capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, model := range response.Data {
		if model["id"] != "full-window-output-model" {
			continue
		}
		if model["max_output_tokens"] != float64(10000) || model["selectable"] != true {
			t.Fatalf("full-window model was not safely bounded: %#v", model)
		}
		return
	}
	t.Fatal("full-window-output-model missing from response")
}

func TestModelCapabilitiesHandlerKeepsNewIncompleteModelsVisible(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-incomplete"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:          "newly-discovered-model",
		Object:      "model",
		OwnedBy:     "openai",
		DisplayName: "Newly Discovered Model",
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/model-capabilities", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, model := range response.Data {
		if model["id"] != "newly-discovered-model" {
			continue
		}
		if model["available"] != true || model["selectable"] != false || model["capability_status"] != "incomplete" {
			t.Fatalf("new model visibility = %#v, want available but incomplete", model)
		}
		input, ok := model["supported_input_modalities"].([]any)
		if !ok || len(input) != 0 {
			t.Fatalf("supported_input_modalities = %#v, want an empty array", model["supported_input_modalities"])
		}
		output, ok := model["supported_output_modalities"].([]any)
		if !ok || len(output) != 0 {
			t.Fatalf("supported_output_modalities = %#v, want an empty array", model["supported_output_modalities"])
		}
		return
	}
	t.Fatalf("newly discovered incomplete model missing from response: %s", recorder.Body.String())
}
