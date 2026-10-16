package registry

import "strings"

// EnrichCapabilityInfo fills missing metadata only, preserving route overrides.
// Both public capability discovery and client-specific catalogs use this path.
func EnrichCapabilityInfo(runtimeInfo *ModelInfo) *ModelInfo {
	if runtimeInfo == nil {
		return nil
	}
	staticInfo := LookupStaticModelInfo(runtimeInfo.ID)
	if strings.EqualFold(runtimeInfo.OwnedBy, "deepseek") {
		staticInfo = LookupStaticModelInfoByChannel(runtimeInfo.ID, "deepseek")
	}
	merged := cloneModelInfo(runtimeInfo)
	if staticInfo != nil {
		if merged.OwnedBy == "" {
			merged.OwnedBy = staticInfo.OwnedBy
		}
		if merged.Type == "" {
			merged.Type = staticInfo.Type
		}
		if merged.DisplayName == "" {
			merged.DisplayName = staticInfo.DisplayName
		}
		if merged.Description == "" {
			merged.Description = staticInfo.Description
		}
		merged.SupportsWebSearch = merged.SupportsWebSearch || staticInfo.SupportsWebSearch
		if merged.ContextLength <= 0 {
			merged.ContextLength = staticInfo.ContextLength
		}
		if merged.MaxContextLength <= 0 {
			merged.MaxContextLength = staticInfo.MaxContextLength
		}
		if merged.InputTokenLimit <= 0 {
			merged.InputTokenLimit = staticInfo.InputTokenLimit
		}
		if merged.OutputTokenLimit <= 0 {
			merged.OutputTokenLimit = staticInfo.OutputTokenLimit
		}
		if merged.MaxCompletionTokens <= 0 {
			merged.MaxCompletionTokens = staticInfo.MaxCompletionTokens
		}
		if merged.CapabilityMaxOutputTokens <= 0 {
			merged.CapabilityMaxOutputTokens = staticInfo.CapabilityMaxOutputTokens
		}
		if len(merged.SupportedInputModalities) == 0 && !merged.ExplicitInputModalities {
			merged.SupportedInputModalities = append([]string(nil), staticInfo.SupportedInputModalities...)
		}
		if len(merged.SupportedOutputModalities) == 0 {
			merged.SupportedOutputModalities = append([]string(nil), staticInfo.SupportedOutputModalities...)
		}
		if len(merged.SupportedWorkloads) == 0 {
			merged.SupportedWorkloads = append([]string(nil), staticInfo.SupportedWorkloads...)
		}
		if len(merged.SupportedParameters) == 0 {
			merged.SupportedParameters = append([]string(nil), staticInfo.SupportedParameters...)
		}
		if merged.Thinking == nil && !merged.ExplicitThinking {
			merged.Thinking = staticInfo.Thinking
		}
	}
	// These are exact registered xAI IDs, not model-name pattern inference.
	if strings.EqualFold(merged.Type, "xai") || strings.EqualFold(merged.OwnedBy, "xai") {
		var input, output, workloads []string
		switch merged.ID {
		case xaiBuiltinImageModelID, xaiBuiltinImageQualityModelID, xaiBuiltinImage20ModelID:
			input, output, workloads = []string{"text", "image"}, []string{"image"}, []string{ModelWorkloadImageGeneration}
		case xaiBuiltinVideoModelID, xaiBuiltinVideo15ModelID, xaiBuiltinVideo15PreviewID:
			input, output, workloads = []string{"text", "image", "video"}, []string{"video"}, []string{ModelWorkloadVideoGeneration}
		case xaiBuiltinSpeechModelID, xaiBuiltinSpeechVoiceModelID:
			input, output, workloads = []string{"text"}, []string{"audio"}, []string{ModelWorkloadSpeechGeneration}
		case "grok-composer-2.5-fast":
			// Official Composer page declares a 200k agent model. Preserve the
			// registered context/output limits; do not invent vision or effort levels.
			input, output, workloads = []string{"text"}, []string{"text"}, []string{ModelWorkloadConversation, ModelWorkloadAgent}
		}
		if len(merged.SupportedInputModalities) == 0 && !merged.ExplicitInputModalities {
			merged.SupportedInputModalities = input
		}
		if len(merged.SupportedOutputModalities) == 0 {
			merged.SupportedOutputModalities = output
		}
		if len(merged.SupportedWorkloads) == 0 {
			merged.SupportedWorkloads = workloads
		}
	}
	return merged
}
