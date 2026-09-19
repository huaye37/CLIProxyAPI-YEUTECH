package websubscription

import (
	"context"
	"errors"
	"testing"
)

type fakePromptBackend struct {
	session *fakePromptSession
}

func (b *fakePromptBackend) Info() DriverInfo {
	return DriverInfo{ID: "fake-web", Implementation: "test"}
}

func (b *fakePromptBackend) Probe(context.Context) (ProbeResult, error) {
	return ProbeResult{Status: DriverAvailable}, nil
}

func (b *fakePromptBackend) OpenPromptSession(context.Context, SessionRequest) (PromptBackendSession, error) {
	return b.session, nil
}

type fakePromptSession struct {
	outputs []string
	prompts []string
	closed  bool
}

func (s *fakePromptSession) Sample(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if len(s.outputs) == 0 {
		return "", errors.New("no fake output")
	}
	output := s.outputs[0]
	s.outputs = s.outputs[1:]
	return output, nil
}

func (s *fakePromptSession) Close() error {
	s.closed = true
	return nil
}

func TestPromptDriverReturnsFinalText(t *testing.T) {
	backendSession := &fakePromptSession{outputs: []string{"final answer"}}
	driver, err := NewPromptDriver(&fakePromptBackend{session: backendSession}, DefaultProtocolLimits())
	if err != nil {
		t.Fatal(err)
	}
	session, err := driver.Open(context.Background(), SessionRequest{Model: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := session.Generate(context.Background(), Turn{
		RequestID:  "req-final",
		Messages:   []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "hello"}}}},
		ToolChoice: ToolChoice{Mode: ToolChoiceNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := <-events
	second := <-events
	if first.Type != EventTextDelta || first.Text != "final answer" || second.Type != EventCompleted {
		t.Fatalf("unexpected events: %#v %#v", first, second)
	}
}

func TestPromptDriverRepairsInvalidToolOutputOnce(t *testing.T) {
	backendSession := &fakePromptSession{outputs: []string{
		envelope(`{"calls":[{"type":"function","name":"search","arguments":"bad"}]}`),
		envelope(`{"calls":[{"type":"function","name":"search","arguments":{"query":"fixed"}}]}`),
	}}
	driver, err := NewPromptDriver(&fakePromptBackend{session: backendSession}, DefaultProtocolLimits())
	if err != nil {
		t.Fatal(err)
	}
	session, err := driver.Open(context.Background(), SessionRequest{Model: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := session.Generate(context.Background(), Turn{
		RequestID:  "req-tools",
		Messages:   []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "search"}}}},
		Tools:      []ToolDefinition{{Type: ToolFunction, Name: "search"}},
		ToolChoice: ToolChoice{Mode: ToolChoiceAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := <-events
	if first.Type != EventToolCalls || len(first.Actions) != 1 || len(backendSession.prompts) != 2 {
		t.Fatalf("event=%#v prompts=%d", first, len(backendSession.prompts))
	}
	if err := session.Close(); err != nil || !backendSession.closed {
		t.Fatalf("close err=%v closed=%v", err, backendSession.closed)
	}
}
