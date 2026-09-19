package websubscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownTool      = errors.New("unknown tool")
	ErrAmbiguousTool    = errors.New("ambiguous tool")
	ErrToolTypeMismatch = errors.New("tool type mismatch")
)

type ToolCatalog struct {
	tools map[string]ToolDefinition
}

func NewToolCatalog(definitions []ToolDefinition) (*ToolCatalog, error) {
	catalog := &ToolCatalog{tools: make(map[string]ToolDefinition, len(definitions))}
	for _, definition := range definitions {
		definition.Name = strings.TrimSpace(definition.Name)
		definition.Namespace = strings.TrimSpace(definition.Namespace)
		if definition.Name == "" {
			return nil, errors.New("tool name is required")
		}
		if definition.Type != ToolFunction && definition.Type != ToolCustom {
			return nil, fmt.Errorf("unsupported tool type %q", definition.Type)
		}
		if definition.Type == ToolFunction && len(definition.Parameters) > 0 && !json.Valid(definition.Parameters) {
			return nil, fmt.Errorf("tool %q has invalid parameters JSON", qualifiedName(definition.Namespace, definition.Name))
		}
		if definition.Type == ToolFunction && len(definition.Parameters) > 0 {
			var schema map[string]json.RawMessage
			if err := json.Unmarshal(definition.Parameters, &schema); err != nil || schema == nil {
				return nil, fmt.Errorf("tool %q parameters must be a JSON object", qualifiedName(definition.Namespace, definition.Name))
			}
		}
		key := catalogKey(definition.Type, definition.Namespace, definition.Name)
		if _, exists := catalog.tools[key]; exists {
			return nil, fmt.Errorf("duplicate tool %q", qualifiedName(definition.Namespace, definition.Name))
		}
		catalog.tools[key] = definition
	}
	return catalog, nil
}

func (c *ToolCatalog) Resolve(toolType ToolType, namespace, name string) (ToolDefinition, error) {
	name = strings.TrimSpace(name)
	namespace = strings.TrimSpace(namespace)
	if toolType != ToolFunction && toolType != ToolCustom {
		return ToolDefinition{}, fmt.Errorf("%w: %q", ErrToolTypeMismatch, toolType)
	}
	if namespace != "" {
		if definition, ok := c.tools[catalogKey(toolType, namespace, name)]; ok {
			return definition, nil
		}
		for _, definition := range c.tools {
			if definition.Namespace == namespace && definition.Name == name {
				return ToolDefinition{}, fmt.Errorf("%w: %s", ErrToolTypeMismatch, qualifiedName(namespace, name))
			}
		}
		return ToolDefinition{}, fmt.Errorf("%w: %s", ErrUnknownTool, qualifiedName(namespace, name))
	}

	var matches []ToolDefinition
	for _, definition := range c.tools {
		if definition.Type == toolType && definition.Name == name {
			matches = append(matches, definition)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return ToolDefinition{}, fmt.Errorf("%w: %s", ErrAmbiguousTool, name)
	}
	for _, definition := range c.tools {
		if definition.Name == name {
			return ToolDefinition{}, fmt.Errorf("%w: %s", ErrToolTypeMismatch, name)
		}
	}
	return ToolDefinition{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
}

func (c *ToolCatalog) ValidateChoice(choice ToolChoice) error {
	switch choice.Mode {
	case "", ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		return nil
	case ToolChoiceSpecific:
		if choice.Name == "" {
			return errors.New("specific tool choice requires a name")
		}
		_, err := c.Resolve(choice.Type, choice.Namespace, choice.Name)
		return err
	default:
		return fmt.Errorf("unsupported tool choice mode %q", choice.Mode)
	}
}

func catalogKey(toolType ToolType, namespace, name string) string {
	return string(toolType) + "\x00" + strings.TrimSpace(namespace) + "\x00" + strings.TrimSpace(name)
}

func qualifiedName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "::" + name
}
