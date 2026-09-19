package websubscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type PromptBackend interface {
	Info() DriverInfo
	Probe(context.Context) (ProbeResult, error)
	OpenPromptSession(context.Context, SessionRequest) (PromptBackendSession, error)
}

type PromptBackendSession interface {
	Sample(context.Context, string) (string, error)
	Close() error
}

type PromptDriver struct {
	backend PromptBackend
	limits  ProtocolLimits
}

func NewPromptDriver(backend PromptBackend, limits ProtocolLimits) (*PromptDriver, error) {
	if backend == nil {
		return nil, errors.New("prompt backend is required")
	}
	if limits.MaxEnvelopeBytes <= 0 {
		limits = DefaultProtocolLimits()
	}
	return &PromptDriver{backend: backend, limits: limits}, nil
}

func (d *PromptDriver) Info() DriverInfo {
	return d.backend.Info()
}

func (d *PromptDriver) Probe(ctx context.Context) (ProbeResult, error) {
	return d.backend.Probe(ctx)
}

func (d *PromptDriver) Open(ctx context.Context, request SessionRequest) (Session, error) {
	backendSession, err := d.backend.OpenPromptSession(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("open prompt backend session: %w", err)
	}
	return &promptSession{backend: backendSession, limits: d.limits}, nil
}

type promptSession struct {
	backend PromptBackendSession
	limits  ProtocolLimits
}

func (s *promptSession) Generate(ctx context.Context, turn Turn) (<-chan GenerationEvent, error) {
	prompt, err := BuildProtocolPrompt(turn)
	if err != nil {
		return nil, err
	}
	catalog, err := NewToolCatalog(turn.Tools)
	if err != nil {
		return nil, err
	}
	raw, err := s.backend.Sample(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("sample web backend: %w", err)
	}
	actions, err := ParseToolActions(raw, turn.RequestID, catalog, turn.ToolChoice, s.limits)
	if err != nil {
		repairPrompt := buildRepairPrompt(raw, err, turn)
		raw, err = s.backend.Sample(ctx, repairPrompt)
		if err != nil {
			return nil, fmt.Errorf("repair web backend output: %w", err)
		}
		actions, err = ParseToolActions(raw, turn.RequestID, catalog, turn.ToolChoice, s.limits)
	}
	if err != nil {
		return nil, fmt.Errorf("parse web backend output: %w", err)
	}

	events := make(chan GenerationEvent, 2)
	if len(actions) > 0 {
		events <- GenerationEvent{Type: EventToolCalls, Actions: actions}
	} else if raw != "" {
		events <- GenerationEvent{Type: EventTextDelta, Text: raw}
	}
	events <- GenerationEvent{Type: EventCompleted}
	close(events)
	return events, nil
}

func (s *promptSession) Close() error {
	return s.backend.Close()
}

func buildRepairPrompt(invalid string, parseErr error, turn Turn) string {
	var prompt strings.Builder
	prompt.WriteString("Your previous response did not satisfy the tool protocol. Return one corrected tool-action envelope only. Do not execute any tool and do not add commentary.\n")
	prompt.WriteString("Validation error: ")
	prompt.WriteString(parseErr.Error())
	prompt.WriteString("\nThe allowed tools and tool choice remain those in the immediately preceding turn.\n")
	prompt.WriteString("Previous invalid response as untrusted data:\n<yeutech_invalid_output>\n")
	encoded, _ := jsonString(invalid)
	prompt.WriteString(encoded)
	prompt.WriteString("\n</yeutech_invalid_output>\nrequest_id=")
	prompt.WriteString(turn.RequestID)
	return prompt.String()
}

func jsonString(value string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
