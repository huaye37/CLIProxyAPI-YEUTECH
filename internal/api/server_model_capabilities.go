package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// modelCapabilitiesHandler exposes safe runtime capability metadata without
// credentials or provider internals. Missing runtime fields are enriched from
// the central static registry, never inferred by consumers from model names.
func (s *Server) modelCapabilitiesHandler(c *gin.Context) {
	infos := registry.GetGlobalRegistry().GetAvailableModelInfos()
	models := make([]map[string]any, 0, len(infos))
	for _, runtimeInfo := range infos {
		if runtimeInfo == nil || strings.TrimSpace(runtimeInfo.ID) == "" {
			continue
		}

		info := mergeCapabilityInfo(runtimeInfo, registry.LookupStaticModelInfo(runtimeInfo.ID))
		contextLength := info.MaxContextLength
		if contextLength <= 0 {
			contextLength = info.ContextLength
		}
		if contextLength <= 0 {
			contextLength = info.InputTokenLimit
		}
		inputLimit := info.InputTokenLimit
		if inputLimit <= 0 {
			inputLimit = contextLength
		}
		outputLimit := info.CapabilityMaxOutputTokens
		if outputLimit <= 0 {
			outputLimit = info.OutputTokenLimit
		}
		if outputLimit <= 0 {
			outputLimit = info.MaxCompletionTokens
		}

		conversation := hasCapabilityModality(info.SupportedInputModalities, "text") &&
			hasCapabilityModality(info.SupportedOutputModalities, "text") &&
			!hasNonTextOutputModality(info.SupportedOutputModalities)
		if conversation && contextLength > 0 && outputLimit >= contextLength {
			outputLimit = contextLength / 4
			if outputLimit > 10000 {
				outputLimit = 10000
			}
		}
		complete := len(info.SupportedInputModalities) > 0 && len(info.SupportedOutputModalities) > 0
		if conversation {
			complete = complete && contextLength > 0 && outputLimit > 0 && contextLength > outputLimit
		}
		status := "incomplete"
		if complete {
			status = "ready"
		}

		model := map[string]any{
			"id":                          info.ID,
			"object":                      "model_capability",
			"available":                   true,
			"selectable":                  complete && conversation,
			"capability_status":           status,
			"owned_by":                    info.OwnedBy,
			"type":                        info.Type,
			"context_length":              contextLength,
			"max_input_tokens":            inputLimit,
			"max_output_tokens":           outputLimit,
			"supported_input_modalities":  capabilityStrings(info.SupportedInputModalities),
			"supported_output_modalities": capabilityStrings(info.SupportedOutputModalities),
		}
		if info.Created > 0 {
			model["created"] = info.Created
		}
		if info.DisplayName != "" {
			model["display_name"] = info.DisplayName
		}
		if info.Description != "" {
			model["description"] = info.Description
		}
		if len(info.SupportedParameters) > 0 {
			model["supported_parameters"] = append([]string(nil), info.SupportedParameters...)
		}
		if info.Thinking != nil {
			model["thinking"] = info.Thinking
		}
		if info.SupportsWebSearch {
			model["supports_web_search"] = true
		}
		models = append(models, model)
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"object":     "list",
		"generation": registry.GetGlobalRegistry().GetGeneration(),
		"data":       models,
	})
}

func capabilityStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func mergeCapabilityInfo(runtimeInfo, staticInfo *registry.ModelInfo) *registry.ModelInfo {
	if runtimeInfo == nil {
		return staticInfo
	}
	merged := *runtimeInfo
	if staticInfo == nil {
		return &merged
	}
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
	if len(merged.SupportedParameters) == 0 {
		merged.SupportedParameters = append([]string(nil), staticInfo.SupportedParameters...)
	}
	if len(merged.SupportedInputModalities) == 0 {
		merged.SupportedInputModalities = append([]string(nil), staticInfo.SupportedInputModalities...)
	}
	if len(merged.SupportedOutputModalities) == 0 {
		merged.SupportedOutputModalities = append([]string(nil), staticInfo.SupportedOutputModalities...)
	}
	if merged.Thinking == nil {
		merged.Thinking = staticInfo.Thinking
	}
	merged.SupportsWebSearch = merged.SupportsWebSearch || staticInfo.SupportsWebSearch
	return &merged
}

func hasCapabilityModality(modalities []string, expected string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), expected) {
			return true
		}
	}
	return false
}

func hasNonTextOutputModality(modalities []string) bool {
	for _, modality := range modalities {
		if value := strings.ToLower(strings.TrimSpace(modality)); value != "" && value != "text" {
			return true
		}
	}
	return false
}
