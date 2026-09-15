package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestModelCapabilitiesHandlerUsesSafeCatalogBudget(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-budget"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID: "gpt-5.3-codex-spark", Object: "model", OwnedBy: "openai", Type: "openai",
		ContextLength: 128000, MaxCompletionTokens: 128000,
		SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: "codex", Status: coreauth.StatusActive})

	target := requestModelCapabilityFromServer(t, server, "gpt-5.3-codex-spark")
	if target["max_output_tokens"] != float64(10000) {
		t.Fatalf("max_output_tokens = %v, want safe catalog budget 10000", target["max_output_tokens"])
	}
	if target["capability_status"] != "ready" || target["selectable"] != true {
		t.Fatalf("capability readiness = %#v, want ready/selectable", target)
	}
	assertCapabilityWorkloads(t, target, "conversation", "agent")
}

func TestModelCapabilitiesHandlerEnrichesStaticGeminiMetadata(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-gemini"
	modelRegistry.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{{
		ID: "gemini-3.1-flash-image", Object: "model", OwnedBy: "antigravity", Type: "antigravity",
		SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"text", "image"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: "antigravity", Status: coreauth.StatusActive})

	target := requestModelCapabilityFromServer(t, server, "gemini-3.1-flash-image")
	if target["context_length"] != float64(1048576) || target["max_output_tokens"] != float64(65536) {
		t.Fatalf("static metadata was not enriched: %#v", target)
	}
	if target["capability_status"] != "ready" || target["selectable"] != true {
		t.Fatalf("image-output model = %#v, want ready/selectable for its declared workload", target)
	}
	assertCapabilityWorkloads(t, target, "image_generation")
}

func TestModelCapabilitiesHandlerDeclaresReviewWorkloadWithoutConversation(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-review"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{ID: "codex-auto-review", Object: "model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: "codex", Status: coreauth.StatusActive})

	target := requestModelCapabilityFromServer(t, server, "codex-auto-review")
	if target["capability_status"] != "ready" || target["selectable"] != true {
		t.Fatalf("review model = %#v, want ready/selectable for its declared workload", target)
	}
	assertCapabilityWorkloads(t, target, "review")
}

func TestModelCapabilitiesHandlerKeepsIncompleteGPTImageModelsUnselectable(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-gpt-images"
	models := registry.WithCodexBuiltins(nil)
	modelRegistry.RegisterClient(clientID, "codex", models)
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: "codex", Status: coreauth.StatusActive})

	for _, model := range models {
		target := requestModelCapabilityFromServer(t, server, model.ID)
		if target["capability_status"] != "incomplete" || target["selectable"] != false {
			t.Fatalf("GPT image model %q = %#v, want incomplete/unselectable", model.ID, target)
		}
		assertCapabilityWorkloads(t, target, "image_generation")
	}
}

func TestModelCapabilitiesHandlerKeepsPluginIdentifiersOutOfWorkloads(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-plugin-boundary"
	modelRegistry.RegisterClient(clientID, "plugin-provider", []*registry.ModelInfo{{
		ID: "plugin-model", Object: "model", ContextLength: 32000, MaxCompletionTokens: 4000,
		SupportedParameters:       []string{"tools"},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedWorkloads:        []string{"agent", "plugin:weather", "tool:search", "AGENT"},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: "plugin-provider", Status: coreauth.StatusActive})

	target := requestModelCapabilityFromServer(t, server, "plugin-model")
	if target["selectable"] != true {
		t.Fatalf("plugin model = %#v, want selectable agent workload", target)
	}
	assertCapabilityWorkloads(t, target, "agent")
	if _, exists := target["provider_capabilities"]; exists {
		t.Fatalf("model capability response must not publish unprobed provider capabilities: %#v", target)
	}
}

func TestModelCapabilitiesHandlerKeepsUnknownModelsIncomplete(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-model-capabilities-incomplete"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "newly-discovered-model", Object: "model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: "openai", Status: coreauth.StatusActive})

	target := requestModelCapabilityFromServer(t, server, "newly-discovered-model")
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
	assertCapabilityWorkloads(t, target)
}

func TestModelCapabilitiesHandlerMergesRuntimeRouteState(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		credential *coreauth.Auth
		wantReason string
	}{
		{
			name: "auth unavailable",
			credential: &coreauth.Auth{
				Status: coreauth.StatusError, Unavailable: true,
				LastError: &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"},
			},
			wantReason: modelUnavailableAuthUnavailable,
		},
		{
			name: "model not found",
			credential: &coreauth.Auth{Status: coreauth.StatusActive, ModelStates: map[string]*coreauth.ModelState{
				"route-model": {Status: coreauth.StatusError, Unavailable: true, LastError: &coreauth.Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound}},
			}},
			wantReason: modelUnavailableNotFound,
		},
		{
			name: "credits required",
			credential: &coreauth.Auth{Status: coreauth.StatusActive, ModelStates: map[string]*coreauth.ModelState{
				"route-model": {
					Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour),
					LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "Usage credits are required for this model"},
				},
			}},
			wantReason: modelUnavailableCreditsRequired,
		},
		{
			name: "cooldown",
			credential: &coreauth.Auth{Status: coreauth.StatusActive, ModelStates: map[string]*coreauth.ModelState{
				"route-model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour)},
			}},
			wantReason: modelUnavailableCooldown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			clientID := "test-runtime-route-" + strings.ReplaceAll(test.name, " ", "-")
			registerCapabilityTestModel(t, clientID, "claude", "route-model")
			test.credential.ID = clientID
			test.credential.Provider = "claude"
			registerCapabilityTestAuth(t, server, test.credential)

			target := requestModelCapabilityFromServer(t, server, "route-model")
			if target["available"] != false || target["selectable"] != false || target["unavailable_reason"] != test.wantReason {
				t.Fatalf("route state = %#v, want unavailable reason %q", target, test.wantReason)
			}
			if target["capability_status"] != "ready" {
				t.Fatalf("capability_status = %v, want capability definition to remain ready", target["capability_status"])
			}
		})
	}
}

func TestModelCapabilitiesHandlerKeepsModelAvailableWhenAnyProviderRouteIsHealthy(t *testing.T) {
	server := newTestServer(t)
	modelID := "shared-route-model"
	failedID := "test-shared-route-claude"
	healthyID := "test-shared-route-antigravity"
	registerCapabilityTestModel(t, failedID, "claude", modelID)
	registerCapabilityTestModel(t, healthyID, "antigravity", modelID)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{
		ID: failedID, Provider: "claude", Status: coreauth.StatusActive,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: {Status: coreauth.StatusError, Unavailable: true, LastError: &coreauth.Error{Code: "model_not_found"}},
		},
	})
	registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: healthyID, Provider: "antigravity", Status: coreauth.StatusActive})

	target := requestModelCapabilityFromServer(t, server, modelID)
	if target["available"] != true || target["selectable"] != true {
		t.Fatalf("multi-provider route = %#v, want healthy route to keep model selectable", target)
	}
	if _, exists := target["unavailable_reason"]; exists {
		t.Fatalf("healthy model must not expose unavailable_reason: %#v", target)
	}
}

func TestModelCapabilitiesHandlerKeepsHealthyProviderFamiliesSelectable(t *testing.T) {
	for _, provider := range []string{"codex", "gemini", "antigravity", "deepseek"} {
		t.Run(provider, func(t *testing.T) {
			server := newTestServer(t)
			modelID := "healthy-" + provider + "-model"
			clientID := "test-healthy-route-" + provider
			registerCapabilityTestModel(t, clientID, provider, modelID)
			registerCapabilityTestAuth(t, server, &coreauth.Auth{ID: clientID, Provider: provider, Status: coreauth.StatusActive})

			target := requestModelCapabilityFromServer(t, server, modelID)
			if target["available"] != true || target["selectable"] != true {
				t.Fatalf("healthy %s route = %#v, want available/selectable", provider, target)
			}
		})
	}
}

func TestModelCapabilitiesHandlerDoesNotPoisonSiblingModelsOnOneCredential(t *testing.T) {
	server := newTestServer(t)
	clientID := "test-model-isolation"
	failedModel := "failed-model"
	healthySibling := "healthy-sibling"
	modelRegistry := registry.GetGlobalRegistry()
	models := []*registry.ModelInfo{
		{ID: failedModel, ContextLength: 32000, MaxCompletionTokens: 4000, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
		{ID: healthySibling, ContextLength: 32000, MaxCompletionTokens: 4000, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
	}
	modelRegistry.RegisterClient(clientID, "deepseek", models)
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	registerCapabilityTestAuth(t, server, &coreauth.Auth{
		ID: clientID, Provider: "deepseek", Status: coreauth.StatusError,
		ModelStates: map[string]*coreauth.ModelState{
			failedModel: {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)},
		},
	})

	target := requestModelCapabilityFromServer(t, server, healthySibling)
	if target["available"] != true || target["selectable"] != true {
		t.Fatalf("healthy sibling = %#v, want one model failure isolated from its siblings", target)
	}
}

func TestModelCapabilitiesHandlerReopensRouteAfterCooldownExpires(t *testing.T) {
	server := newTestServer(t)
	clientID := "test-expired-cooldown"
	modelID := "expired-cooldown-model"
	registerCapabilityTestModel(t, clientID, "claude", modelID)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{
		ID: clientID, Provider: "claude", Status: coreauth.StatusError,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: {
				Status: coreauth.StatusError, Unavailable: true,
				NextRetryAfter: time.Now().Add(-time.Minute),
				LastError:      &coreauth.Error{Code: "model_not_found"},
			},
		},
	})

	target := requestModelCapabilityFromServer(t, server, modelID)
	if target["available"] != true || target["selectable"] != true {
		t.Fatalf("expired cooldown route = %#v, want route reopened for a new probe", target)
	}
}

func TestModelCapabilitiesHandlerFailsClosedWithoutMatchingAuth(t *testing.T) {
	server := newTestServer(t)
	registerCapabilityTestModel(t, "test-no-auth-route", "claude", "no-auth-model")

	target := requestModelCapabilityFromServer(t, server, "no-auth-model")
	if target["available"] != false || target["selectable"] != false || target["unavailable_reason"] != modelUnavailableAuthUnavailable {
		t.Fatalf("missing auth route = %#v, want fail-closed auth_unavailable", target)
	}
}

func TestModelCapabilitiesHandlerKeepsSuspendedDiscoveryVisible(t *testing.T) {
	server := newTestServer(t)
	clientID := "test-suspended-discovery"
	modelID := "suspended-discovery-model"
	registerCapabilityTestModel(t, clientID, "claude", modelID)
	registerCapabilityTestAuth(t, server, &coreauth.Auth{
		ID: clientID, Provider: "claude", Status: coreauth.StatusActive,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: {Status: coreauth.StatusError, Unavailable: true, LastError: &coreauth.Error{Code: "model_not_found"}},
		},
	})
	registry.GetGlobalRegistry().SuspendClientModel(clientID, modelID, "model_not_found")

	target := requestModelCapabilityFromServer(t, server, modelID)
	if target["available"] != false || target["unavailable_reason"] != modelUnavailableNotFound {
		t.Fatalf("suspended discovery = %#v, want visible model_not_found entry", target)
	}
}

func assertCapabilityWorkloads(t *testing.T, model map[string]any, expected ...string) {
	t.Helper()
	values, ok := model["supported_workloads"].([]any)
	if !ok || len(values) != len(expected) {
		t.Fatalf("supported_workloads = %#v, want %v", model["supported_workloads"], expected)
	}
	for index, value := range values {
		if value != expected[index] {
			t.Fatalf("supported_workloads = %#v, want %v", model["supported_workloads"], expected)
		}
	}
}

func requestModelCapabilityFromServer(t *testing.T, server *Server, modelID string) map[string]any {
	t.Helper()
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

func registerCapabilityTestModel(t *testing.T, clientID, provider, modelID string) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: provider, Type: provider,
		ContextLength: 32000, MaxCompletionTokens: 4000,
		SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"},
		SupportedWorkloads: []string{registry.ModelWorkloadConversation, registry.ModelWorkloadAgent},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })
}

func registerCapabilityTestAuth(t *testing.T, server *Server, credential *coreauth.Auth) {
	t.Helper()
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register auth %q: %v", credential.ID, err)
	}
}
