package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatResponsesWireProtocol(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/responses" {
					t.Errorf("path=%s", r.URL.Path)
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
