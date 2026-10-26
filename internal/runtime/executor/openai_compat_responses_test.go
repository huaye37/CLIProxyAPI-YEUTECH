package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatResponsesPreservesSSEFrameBoundaries(t *testing.T) {
	events := []string{
		`{"type":"response.created","response":{"id":"resp_compact","output":[]}}`,
		`{"type":"response.in_progress","response":{"id":"resp_compact","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","id":"cmp_test","encrypted_content":"fixture"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_test","encrypted_content":"fixture"}}`,
		`{"type":"response.completed","response":{"id":"resp_compact","status":"completed","output":[{"type":"compaction","id":"cmp_test","encrypted_content":"fixture"}]}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = io.WriteString(w, "data: "+event+"\n\n")
		}
	}))
	defer srv.Close()
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "web", WireAPI: "responses", BaseURL: srv.URL}}}
	e := NewOpenAICompatExecutor("web", cfg)
	a := &auth.Auth{Provider: "web", Attributes: map[string]string{"base_url": srv.URL, "compat_name": "web"}}
	req := execution.Request{Model: "test", Payload: []byte(`{"model":"test","input":[{"type":"compaction_trigger"}],"stream":true}`)}
	result, err := e.ExecuteStream(context.Background(), a, req, execution.Options{SourceFormat: translator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var buffered strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		if !strings.HasSuffix(string(chunk.Payload), "\n\n") {
			t.Errorf("native Responses chunk lacks SSE frame boundary: %q", chunk.Payload)
		}
		// Bootstrap retries concatenate control events before delivering the first payload.
		buffered.Write(chunk.Payload)
	}
	frames := strings.Split(strings.TrimSuffix(buffered.String(), "\n\n"), "\n\n")
	if len(frames) != len(events) {
		t.Fatalf("buffered stream has %d frames, want %d", len(frames), len(events))
	}
	for i, frame := range frames {
		payload := strings.TrimPrefix(frame, "data: ")
		if !json.Valid([]byte(payload)) || payload != events[i] {
			t.Errorf("frame %d changed or became invalid: %q", i, frame)
		}
	}
}

func TestOpenAICompatResponsesWireProtocol(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/responses" {
					t.Errorf("path=%s", r.URL.Path)
				}
				if gjson.GetBytes(body, "stream_options").Exists() {
					t.Error("Responses request must not contain Chat Completions stream_options")
				}
				if !gjson.GetBytes(body, "input").Exists() || !strings.Contains(string(body), "call_echo") {
					t.Errorf("lost tool transcript: %s", body)
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[]}}\n\n")
				} else {
					io.WriteString(w, `{"id":"resp_test","object":"response","status":"completed","output":[]}`)
				}
			}))
			defer srv.Close()
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "web", WireAPI: "responses", BaseURL: srv.URL}}}
			e := NewOpenAICompatExecutor("web", cfg)
			a := &auth.Auth{Provider: "web", Attributes: map[string]string{"base_url": srv.URL, "compat_name": "web"}}
			req := execution.Request{Model: "chatgpt-web", Payload: []byte(`{"model":"chatgpt-web","input":[{"type":"function_call","call_id":"call_echo","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"call_echo","output":"ok"}]}`)}
			opts := execution.Options{SourceFormat: translator.FormatOpenAIResponse, Stream: stream}
			if stream {
				result, err := e.ExecuteStream(context.Background(), a, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					output.Write(chunk.Payload)
				}
				if !strings.Contains(output.String(), "response.completed") {
					t.Fatal(output.String())
				}
			} else {
				_, err := e.Execute(context.Background(), a, req, opts)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
