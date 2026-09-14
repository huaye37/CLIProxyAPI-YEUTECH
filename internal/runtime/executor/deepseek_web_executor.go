package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/deepseekweb"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// DeepSeekWebExecutor uses fresh upstream sessions and replays the complete text history.
type DeepSeekWebExecutor struct {
	cfg     *config.Config
	baseURL string
}

func NewDeepSeekWebExecutor(cfg *config.Config) *DeepSeekWebExecutor {
	return &DeepSeekWebExecutor{cfg: cfg, baseURL: "https://chat.deepseek.com"}
}
func (e *DeepSeekWebExecutor) Identifier() string { return "deepseek-web" }
func (e *DeepSeekWebExecutor) RequestToFormat(_ execution.Request, _ execution.Options) translator.Format {
	return translator.FormatOpenAI
}
func (e *DeepSeekWebExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}
func (e *DeepSeekWebExecutor) CountTokens(context.Context, *auth.Auth, execution.Request, execution.Options) (execution.Response, error) {
	return execution.Response{}, &deepseekweb.Error{Code: 400, Message: "DeepSeek Web does not expose exact token counting"}
}
func (e *DeepSeekWebExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, &deepseekweb.Error{Code: 400, Message: "DeepSeek Web does not support arbitrary authenticated requests"}
}

type deepSeekRequestError struct{ message string }

func (e *deepSeekRequestError) Error() string         { return e.message }
func (e *deepSeekRequestError) StatusCode() int       { return 400 }
func (e *deepSeekRequestError) IsRequestScoped() bool { return true }
func deepSeekPrompt(raw []byte) (string, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return "", &deepSeekRequestError{"Invalid request JSON"}
	}
	for _, key := range []string{"tools", "tool_choice", "functions", "function_call", "reasoning_effort", "thinking", "response_format", "temperature", "top_p", "max_tokens", "max_completion_tokens", "stop", "seed", "n", "logprobs", "presence_penalty", "frequency_penalty"} {
		if value, ok := body[key]; ok && string(value) != "null" {
			return "", &deepSeekRequestError{"DeepSeek Web does not support parameter: " + key}
		}
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(body["messages"], &messages) != nil || len(messages) == 0 {
		return "", &deepSeekRequestError{"Text messages are required"}
	}
	parts := make([]string, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "system", "developer", "user", "assistant":
		default:
			return "", &deepSeekRequestError{"DeepSeek Web supports text conversation roles only"}
		}
		if len(m.Content) == 0 || string(m.Content) == "null" {
			return "", &deepSeekRequestError{"Text content is required"}
		}
		var value string
		if json.Unmarshal(m.Content, &value) != nil {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				return "", &deepSeekRequestError{"DeepSeek Web supports text only"}
			}
			for _, block := range blocks {
				if block.Type != "text" {
					return "", &deepSeekRequestError{"DeepSeek Web supports text only"}
				}
				value += block.Text
			}
		}
		if len(messages) == 1 && m.Role == "user" {
			return value, nil
		}
		parts = append(parts, m.Role+":\n"+value)
	}
	return strings.Join(parts, "\n\n"), nil
}
func (e *DeepSeekWebExecutor) prepare(req execution.Request, opts execution.Options) ([]byte, string, bool, error) {
	thinking := false
	switch req.Model {
	case "deepseek-web-chat":
	case "deepseek-web-reasoner":
		thinking = true
	default:
		return nil, "", false, &deepSeekRequestError{"Unknown DeepSeek Web model"}
	}
	// Validate unsupported controls before translators can discard them.
	var original map[string]json.RawMessage
	input := opts.OriginalRequest
	if len(input) == 0 {
		input = req.Payload
	}
	if json.Unmarshal(input, &original) != nil {
		return nil, "", false, &deepSeekRequestError{"Invalid request JSON"}
	}
	for _, key := range []string{"reasoning", "reasoning_effort", "thinking", "tools", "tool_choice", "temperature", "top_p", "max_tokens", "max_output_tokens", "max_completion_tokens", "response_format", "stop", "seed", "n"} {
		if v, ok := original[key]; ok && string(v) != "null" {
			return nil, "", false, &deepSeekRequestError{"DeepSeek Web does not support parameter: " + key}
		}
	}
	payload := translator.TranslateRequest(opts.SourceFormat, translator.FormatOpenAI, req.Model, req.Payload, opts.Stream)
	prompt, err := deepSeekPrompt(payload)
	return payload, prompt, thinking, err
}
func (e *DeepSeekWebExecutor) run(ctx context.Context, a *auth.Auth, prompt string, thinking bool, emit func(string, string) error) error {
	token := ""
	if a != nil {
		token, _ = a.Metadata["user_token"].(string)
	}
	if strings.TrimSpace(token) == "" {
		return &deepseekweb.Error{Code: 401, Message: "DeepSeek Web user_token is missing"}
	}
	client := deepseekweb.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0), BaseURL: e.baseURL}
	body, cleanup, err := client.Open(ctx, token, prompt, thinking)
	if err != nil {
		return err
	}
	defer cleanup()
	defer body.Close()
	return deepseekweb.ReadStream(body, emit)
}
func (e *DeepSeekWebExecutor) Execute(ctx context.Context, a *auth.Auth, req execution.Request, opts execution.Options) (execution.Response, error) {
	payload, prompt, thinking, err := e.prepare(req, opts)
	if err != nil {
		return execution.Response{}, err
	}
	var content, reasoning strings.Builder
	err = e.run(ctx, a, prompt, thinking, func(kind, value string) error {
		if kind == "reasoning_content" {
			reasoning.WriteString(value)
		} else {
			content.WriteString(value)
		}
		return nil
	})
	if err != nil {
		return execution.Response{}, err
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	raw, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}}})
	var param any
	out := translator.TranslateNonStream(ctx, translator.FormatOpenAI, execution.ResponseFormatOrSource(opts), req.Model, opts.OriginalRequest, payload, raw, &param)
	return execution.Response{Payload: out}, nil
}
func (e *DeepSeekWebExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, req execution.Request, opts execution.Options) (*execution.StreamResult, error) {
	payload, prompt, thinking, err := e.prepare(req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan execution.StreamChunk)
	go func() {
		defer close(chunks)
		var param any
		id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()
		send := func(raw []byte) error {
			for _, out := range translator.TranslateStream(ctx, translator.FormatOpenAI, execution.ResponseFormatOrSource(opts), req.Model, opts.OriginalRequest, payload, raw, &param) {
				select {
				case chunks <- execution.StreamChunk{Payload: out}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		emit := func(delta map[string]any, finish any) error {
			raw, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			return send(append(append([]byte("data: "), raw...), []byte("\n\n")...))
		}
		started := false
		err := e.run(ctx, a, prompt, thinking, func(kind, value string) error {
			if !started {
				if err := emit(map[string]any{"role": "assistant"}, nil); err != nil {
					return err
				}
				started = true
			}
			return emit(map[string]any{kind: value}, nil)
		})
		if err != nil {
			select {
			case chunks <- execution.StreamChunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if err = emit(map[string]any{}, "stop"); err == nil {
			_ = send([]byte("data: [DONE]\n\n"))
		}
	}()
	return &execution.StreamResult{Chunks: chunks}, nil
}
