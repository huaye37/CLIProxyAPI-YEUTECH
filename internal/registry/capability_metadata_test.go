package registry

import "testing"

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
