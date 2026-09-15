package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	modelUnavailableAuthUnavailable = "auth_unavailable"
	modelUnavailableCooldown        = "cooldown"
	modelUnavailableCreditsRequired = "credits_required"
	modelUnavailableNotFound        = "model_not_found"
)

// modelCapabilitiesHandler exposes safe runtime capability metadata without
// credentials or provider internals. Missing runtime fields are enriched from
// the central static registry, never inferred by consumers from model names.
func (s *Server) modelCapabilitiesHandler(c *gin.Context) {
	// Capability discovery keeps registered-but-unavailable models visible so
	// callers can explain and recover them. Execution endpoints continue to use
	// the registry's availability-filtered model list.
	infos := registry.GetGlobalRegistry().GetRegisteredModelInfos()
	var credentials []*coreauth.Auth
	if s != nil && s.handlers != nil && s.handlers.AuthManager != nil {
		credentials = s.handlers.AuthManager.List()
	}
	now := time.Now()
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

		conversationShape := hasCapabilityModality(info.SupportedInputModalities, "text") &&
			hasCapabilityModality(info.SupportedOutputModalities, "text") &&
			!hasNonTextOutputModality(info.SupportedOutputModalities)
		if conversationShape && contextLength > 0 && outputLimit >= contextLength {
			outputLimit = contextLength / 4
			if outputLimit > 10000 {
				outputLimit = 10000
			}
		}
		workloads := capabilityWorkloads(info)
		complete := len(info.SupportedInputModalities) > 0 && len(info.SupportedOutputModalities) > 0 && len(workloads) > 0
		complete = complete && contextLength > 0 && outputLimit > 0 && contextLength > outputLimit
		status := "incomplete"
		if complete {
			status = "ready"
		}

		routeAvailable, unavailableReason := modelRouteAvailability(info.ID, credentials, now)
		model := map[string]any{
			"id":                          info.ID,
			"object":                      "model_capability",
			"available":                   routeAvailable,
			"selectable":                  complete && routeAvailable,
			"capability_status":           status,
			"owned_by":                    info.OwnedBy,
			"type":                        info.Type,
			"context_length":              contextLength,
			"max_input_tokens":            inputLimit,
			"max_output_tokens":           outputLimit,
			"supported_input_modalities":  capabilityStrings(info.SupportedInputModalities),
			"supported_output_modalities": capabilityStrings(info.SupportedOutputModalities),
			"supported_workloads":         workloads,
		}
		if !routeAvailable {
			model["unavailable_reason"] = unavailableReason
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

// modelRouteAvailability combines catalog registration with the live auth
// state. A model remains available when any credential that registered that
// exact model is healthy, so a failed Claude route cannot hide a healthy
// Antigravity route for the same public model ID.
func modelRouteAvailability(modelID string, credentials []*coreauth.Auth, now time.Time) (bool, string) {
	modelRegistry := registry.GetGlobalRegistry()
	reasons := make(map[string]bool, 4)
	foundRoute := false
	for _, credential := range credentials {
		if credential == nil || !modelRegistry.ClientSupportsModel(credential.ID, modelID) {
			continue
		}
		foundRoute = true
		available, reason := authModelRouteAvailability(credential, modelID, now)
		if available {
			return true, ""
		}
		reasons[reason] = true
	}
	if !foundRoute {
		return false, modelUnavailableAuthUnavailable
	}

	// Prefer the most actionable reason when every route is blocked.
	for _, reason := range []string{
		modelUnavailableCreditsRequired,
		modelUnavailableCooldown,
		modelUnavailableAuthUnavailable,
		modelUnavailableNotFound,
	} {
		if reasons[reason] {
			return false, reason
		}
	}
	return false, modelUnavailableAuthUnavailable
}

func authModelRouteAvailability(credential *coreauth.Auth, modelID string, now time.Time) (bool, string) {
	if credential == nil || credential.Disabled || credential.Status == coreauth.StatusDisabled {
		return false, modelUnavailableAuthUnavailable
	}

	if credential.Quota.Exceeded && credential.Quota.Reason == "credential_quota" && credential.Quota.NextRecoverAt.After(now) {
		return false, runtimeUnavailableReason(credential.LastError, credential.StatusMessage, true)
	}

	if state := authModelState(credential, modelID); state != nil {
		blockedByTime := state.NextRetryAfter.After(now) || (state.Quota.Exceeded && state.Quota.NextRecoverAt.After(now))
		hadTimedBlock := !state.NextRetryAfter.IsZero() || (state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero())
		blockedWithoutDeadline := (state.Unavailable || state.Status == coreauth.StatusError) && !hadTimedBlock
		blocked := state.Status == coreauth.StatusDisabled || blockedByTime || blockedWithoutDeadline
		if blocked {
			return false, runtimeUnavailableReason(state.LastError, state.StatusMessage, blockedByTime)
		}
		// A healthy per-model state is authoritative when another model on the
		// same credential caused credential.Unavailable to be set.
		if state.Status == coreauth.StatusActive || hadTimedBlock {
			return true, ""
		}
	} else if len(credential.ModelStates) > 0 {
		// Model errors are isolated. MarkResult sets the credential lifecycle to
		// error even when only one model is cooling down; an unmentioned sibling
		// model remains schedulable unless credential_quota blocked it above.
		return true, ""
	}

	blockedByTime := credential.NextRetryAfter.After(now) ||
		(credential.Quota.Exceeded && credential.Quota.NextRecoverAt.After(now))
	hadTimedBlock := !credential.NextRetryAfter.IsZero() || (credential.Quota.Exceeded && !credential.Quota.NextRecoverAt.IsZero())
	blockedWithoutDeadline := (credential.Unavailable || credential.Status == coreauth.StatusError) && !hadTimedBlock
	if credential.Status == coreauth.StatusPending || blockedByTime || blockedWithoutDeadline {
		return false, runtimeUnavailableReason(credential.LastError, credential.StatusMessage, blockedByTime)
	}
	if credential.Status != coreauth.StatusActive && credential.Status != coreauth.StatusRefreshing {
		return false, modelUnavailableAuthUnavailable
	}
	return true, ""
}

func authModelState(credential *coreauth.Auth, modelID string) *coreauth.ModelState {
	if credential == nil {
		return nil
	}
	for registeredModel, state := range credential.ModelStates {
		if strings.EqualFold(strings.TrimSpace(registeredModel), strings.TrimSpace(modelID)) {
			return state
		}
	}
	return nil
}

func runtimeUnavailableReason(runtimeError *coreauth.Error, statusMessage string, blockedByTime bool) string {
	code := ""
	message := statusMessage
	if runtimeError != nil {
		code = strings.ToLower(strings.TrimSpace(runtimeError.Code))
		message += " " + runtimeError.Message
	}
	normalizedMessage := strings.ToLower(strings.TrimSpace(message))

	switch code {
	case modelUnavailableNotFound, "model_not_found_error", "unknown_model":
		return modelUnavailableNotFound
	case modelUnavailableCreditsRequired:
		return modelUnavailableCreditsRequired
	case modelUnavailableAuthUnavailable, "auth_not_found":
		return modelUnavailableAuthUnavailable
	case modelUnavailableCooldown, "model_cooldown", coreauth.ErrorCodeForceCooldown:
		return modelUnavailableCooldown
	}
	if strings.Contains(normalizedMessage, "usage credits are required") || strings.Contains(normalizedMessage, "credits_required") {
		return modelUnavailableCreditsRequired
	}
	if strings.Contains(normalizedMessage, "model_not_found") || strings.Contains(normalizedMessage, "unknown model") {
		return modelUnavailableNotFound
	}
	if strings.Contains(normalizedMessage, "auth_unavailable") || strings.Contains(normalizedMessage, "no auth available") {
		return modelUnavailableAuthUnavailable
	}
	if blockedByTime {
		return modelUnavailableCooldown
	}
	return modelUnavailableAuthUnavailable
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
	if len(merged.SupportedWorkloads) == 0 {
		merged.SupportedWorkloads = append([]string(nil), staticInfo.SupportedWorkloads...)
	}
	if merged.Thinking == nil {
		merged.Thinking = staticInfo.Thinking
	}
	merged.SupportsWebSearch = merged.SupportsWebSearch || staticInfo.SupportsWebSearch
	return &merged
}

func capabilityWorkloads(info *registry.ModelInfo) []string {
	if info == nil {
		return []string{}
	}
	if len(info.SupportedWorkloads) > 0 {
		return normalizeCapabilityWorkloads(info.SupportedWorkloads)
	}
	if workloads := registry.ImmutableModelWorkloads(info.ID); len(workloads) > 0 {
		return normalizeCapabilityWorkloads(workloads)
	}
	if hasCapabilityModality(info.SupportedOutputModalities, "image") {
		return []string{registry.ModelWorkloadImageGeneration}
	}
	if hasCapabilityModality(info.SupportedInputModalities, "text") &&
		hasCapabilityModality(info.SupportedOutputModalities, "text") &&
		!hasNonTextOutputModality(info.SupportedOutputModalities) {
		return []string{registry.ModelWorkloadConversation, registry.ModelWorkloadAgent}
	}
	return []string{}
}

func normalizeCapabilityWorkloads(values []string) []string {
	allowed := map[string]struct{}{
		registry.ModelWorkloadConversation:    {},
		registry.ModelWorkloadAgent:           {},
		registry.ModelWorkloadReview:          {},
		registry.ModelWorkloadImageGeneration: {},
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		workload := strings.ToLower(strings.TrimSpace(value))
		if _, ok := allowed[workload]; !ok {
			continue
		}
		if _, ok := seen[workload]; ok {
			continue
		}
		seen[workload] = struct{}{}
		result = append(result, workload)
	}
	return result
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
