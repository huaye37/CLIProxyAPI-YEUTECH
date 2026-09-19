package websubscription

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	ToolEnvelopeStart = "<yeutech_tool_actions>"
	ToolEnvelopeEnd   = "</yeutech_tool_actions>"
)

type ProtocolLimits struct {
	MaxEnvelopeBytes    int
	MaxArgumentsBytes   int
	MaxCustomInputBytes int
	MaxToolOutputBytes  int
	MaxCalls            int
}

func DefaultProtocolLimits() ProtocolLimits {
	return ProtocolLimits{
		MaxEnvelopeBytes:    256 << 10,
		MaxArgumentsBytes:   64 << 10,
		MaxCustomInputBytes: 64 << 10,
		MaxToolOutputBytes:  256 << 10,
		MaxCalls:            32,
	}
}

type actionEnvelope struct {
	Calls []ToolAction `json:"calls"`
}

func ParseToolActions(raw, requestID string, catalog *ToolCatalog, choice ToolChoice, limits ProtocolLimits) ([]ToolAction, error) {
	payload, found, err := extractEnvelope(raw, limits.MaxEnvelopeBytes)
	if err != nil {
		return nil, err
	}
	if !found {
		if choice.Mode == ToolChoiceRequired || choice.Mode == ToolChoiceSpecific {
			return nil, errors.New("required tool action is missing")
		}
		return nil, nil
	}
	if choice.Mode == ToolChoiceNone {
		return nil, errors.New("tool actions are disabled")
	}
	if err := catalog.ValidateChoice(choice); err != nil {
		return nil, fmt.Errorf("invalid tool choice: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope actionEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("invalid tool action envelope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid tool action envelope: trailing data")
	}
	if len(envelope.Calls) == 0 {
		return nil, errors.New("tool action envelope contains no calls")
	}
	if limits.MaxCalls > 0 && len(envelope.Calls) > limits.MaxCalls {
		return nil, fmt.Errorf("too many tool actions: %d", len(envelope.Calls))
	}

	var selectedDefinition ToolDefinition
	if choice.Mode == ToolChoiceSpecific {
		selectedDefinition, err = catalog.Resolve(choice.Type, choice.Namespace, choice.Name)
		if err != nil {
			return nil, fmt.Errorf("invalid specific tool choice: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(envelope.Calls))
	for index := range envelope.Calls {
		action := &envelope.Calls[index]
		definition, errResolve := catalog.Resolve(action.Type, action.Namespace, action.Name)
		if errResolve != nil {
			return nil, fmt.Errorf("call %d: %w", index, errResolve)
		}
		action.Namespace = definition.Namespace
		if choice.Mode == ToolChoiceSpecific && (definition.Type != selectedDefinition.Type || definition.Name != selectedDefinition.Name || definition.Namespace != selectedDefinition.Namespace) {
			return nil, fmt.Errorf("call %d does not match specific tool choice", index)
		}
		switch action.Type {
		case ToolFunction:
			if len(action.Arguments) == 0 || string(action.Arguments) == "null" {
				return nil, fmt.Errorf("call %d: function arguments are required", index)
			}
			if limits.MaxArgumentsBytes > 0 && len(action.Arguments) > limits.MaxArgumentsBytes {
				return nil, fmt.Errorf("call %d: function arguments exceed limit", index)
			}
			var object map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(action.Arguments, &object); errUnmarshal != nil || object == nil {
				return nil, fmt.Errorf("call %d: function arguments must be a JSON object", index)
			}
			if action.Input != "" {
				return nil, fmt.Errorf("call %d: function action cannot contain custom input", index)
			}
		case ToolCustom:
			if len(action.Arguments) != 0 {
				return nil, fmt.Errorf("call %d: custom action cannot contain arguments", index)
			}
			if limits.MaxCustomInputBytes > 0 && len(action.Input) > limits.MaxCustomInputBytes {
				return nil, fmt.Errorf("call %d: custom input exceeds limit", index)
			}
		}
		if strings.TrimSpace(action.CallID) == "" {
			action.CallID = deterministicCallID(requestID, index, *action)
		}
		if _, exists := seen[action.CallID]; exists {
			return nil, fmt.Errorf("duplicate call ID %q", action.CallID)
		}
		seen[action.CallID] = struct{}{}
	}
	return envelope.Calls, nil
}

func ParseToolActionsWithRepair(raw, requestID string, catalog *ToolCatalog, choice ToolChoice, limits ProtocolLimits, repair func(string, error) (string, error)) ([]ToolAction, error) {
	actions, err := ParseToolActions(raw, requestID, catalog, choice, limits)
	if err == nil || repair == nil {
		return actions, err
	}
	repaired, errRepair := repair(raw, err)
	if errRepair != nil {
		return nil, fmt.Errorf("repair tool action envelope: %w", errRepair)
	}
	return ParseToolActions(repaired, requestID, catalog, choice, limits)
}

func ToolResultMessage(action ToolAction, output string, limits ProtocolLimits) Message {
	truncated := false
	if limits.MaxToolOutputBytes > 0 && len(output) > limits.MaxToolOutputBytes {
		output = output[:limits.MaxToolOutputBytes]
		truncated = true
	}
	return Message{Role: RoleTool, ToolCallID: action.CallID, Name: action.Name, Namespace: action.Namespace, ToolOutput: output, Truncated: truncated}
}

func extractEnvelope(raw string, maxBytes int) ([]byte, bool, error) {
	startCount := strings.Count(raw, ToolEnvelopeStart)
	endCount := strings.Count(raw, ToolEnvelopeEnd)
	if startCount == 0 && endCount == 0 {
		return nil, false, nil
	}
	if startCount != 1 || endCount != 1 {
		return nil, false, errors.New("tool action output must contain exactly one complete envelope")
	}
	start := strings.Index(raw, ToolEnvelopeStart) + len(ToolEnvelopeStart)
	end := strings.Index(raw, ToolEnvelopeEnd)
	if end < start {
		return nil, false, errors.New("tool action envelope is malformed")
	}
	payload := []byte(strings.TrimSpace(raw[start:end]))
	if maxBytes > 0 && len(payload) > maxBytes {
		return nil, false, errors.New("tool action envelope exceeds limit")
	}
	return payload, true, nil
}

func deterministicCallID(requestID string, index int, action ToolAction) string {
	source := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s", requestID, index, action.Type, action.Namespace, action.Name, action.Arguments, action.Input)
	sum := sha256.Sum256([]byte(source))
	return "call_web_" + hex.EncodeToString(sum[:12])
}
