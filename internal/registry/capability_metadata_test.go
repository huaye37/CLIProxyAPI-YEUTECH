package registry

import (
	"slices"
	"testing"
)

func TestAuditedXAISelectionAndToolRestrictions(t *testing.T) {
	for _, id := range []string{"grok-3-mini", "grok-3-mini-fast", "grok-composer-2.5-fast"} {
		if CapabilitySelectionIssue(&ModelInfo{ID: id, Type: "xai"}) == "" {
			t.Fatalf("missing identity restriction for %s", id)
		}
		if CapabilitySelectionIssue(&ModelInfo{ID: id, Type: "openai"}) != "" {
			t.Fatal("restriction leaked to another provider")
		}
	}
	got := EnrichCapabilityInfo(&ModelInfo{ID: "grok-4.20-multi-agent-0309", Type: "xai", SupportedWorkloads: []string{ModelWorkloadAgent}})
	if !slices.Equal(got.SupportedWorkloads, []string{ModelWorkloadConversation}) {
		t.Fatal("multi-agent must not advertise client function tools")
	}
	for _, id := range []string{xaiBuiltinImageModelID, xaiBuiltinImageQualityModelID, xaiBuiltinImage20ModelID} {
		got := EnrichCapabilityInfo(&ModelInfo{ID: id, Type: "xai"})
		if slices.Contains(got.SupportedParameters, "quality") != (id == xaiBuiltinImage20ModelID) {
			t.Fatalf("incorrect quality support for %s", id)
		}
	}
}

func TestEnrichCapabilityInfoPreservesProviderAndOverrides(t *testing.T) {
	input := &ModelInfo{ID: "deepseek-flash", OwnedBy: "Deepseek", MaxContextLength: 128000, ExplicitInputModalities: true, ExplicitThinking: true}
	got := EnrichCapabilityInfo(input)
	if got.ContextLength != 1048576 || got.MaxContextLength != 128000 {
		t.Fatalf("limits = %#v", got)
	}
	if len(got.SupportedInputModalities) != 0 || got.Thinking != nil {
		t.Fatal("explicit restrictions overwritten")
	}
	if input.ContextLength != 0 {
		t.Fatal("mutated live registry")
	}
	unknown := EnrichCapabilityInfo(&ModelInfo{ID: "unknown-model"})
	if unknown.ContextLength != 0 || len(unknown.SupportedInputModalities) > 0 {
		t.Fatal("invented unknown capabilities")
	}
}

func TestEnrichExactMediaCapabilitiesHaveNoTextWindow(t *testing.T) {
	for _, tt := range []struct{ id, workload, output string }{
		{"grok-imagine-image", ModelWorkloadImageGeneration, "image"},
		{"grok-imagine-video", ModelWorkloadVideoGeneration, "video"},
		{"grok-tts", ModelWorkloadSpeechGeneration, "audio"},
	} {
		got := EnrichCapabilityInfo(&ModelInfo{ID: tt.id, Type: "xai"})
		if got.ContextLength != 0 || got.Thinking != nil || len(got.SupportedWorkloads) != 1 || got.SupportedWorkloads[0] != tt.workload || got.SupportedOutputModalities[0] != tt.output {
			t.Fatalf("wrong media contract: %#v", got)
		}
	}
	got := EnrichCapabilityInfo(&ModelInfo{ID: "grok-imagine-unverified", Type: "xai"})
	if len(got.SupportedWorkloads) != 0 {
		t.Fatal("inferred unknown model from its name")
	}
}
