package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientMetadataPrivacyStripsEnvironmentPreservesProtocolAndBody(t *testing.T) {
	privateHeaders := []string{
		"User-Agent", "x-stainless-os", "X-Stainless-Arch", "X-Stainless-Runtime-Version",
		"Sec-CH-UA-Platform", "Sec-CH-UA-Model", "Forwarded", "X-Forwarded-For",
		"X-Forwarded-Host", "X-Real-IP", "CF-Connecting-IP", "CF-IPCountry",
		"Accept-Language", "Referer", "Cookie", "X-Client-App", "X-Machine-ID",
		"X-Device-Model", "X-Client-OS", "X-Timezone", "Time-Zone", "X-Geo-City",
		"X-Claude-Remote-Container-Id", "X-Claude-Remote-Session-Id",
	}
	functionalHeaders := http.Header{
		"Authorization":                     {"Bearer test-key"},
		"X-Api-Key":                         {"test-key"},
		"Content-Type":                      {"application/json"},
		"Content-Encoding":                  {"identity"},
		"Accept":                            {"text/event-stream"},
		"Accept-Encoding":                   {"identity"},
		"Anthropic-Version":                 {"2023-06-01"},
		"Anthropic-Beta":                    {"test-capability"},
		"X-Claude-Code-Session-Id":          {"test-session"},
		"X-Anthropic-Additional-Protection": {"test-security-proof"},
		"Origin":                            {"https://client.example"},
		"Connection":                        {"Upgrade"},
		"Upgrade":                           {"websocket"},
		"Sec-Websocket-Key":                 {"dGVzdA=="},
		"Sec-Websocket-Version":             {"13"},
	}
	body := `{"model":"example","messages":[{"role":"user","content":"Use /Users/example/file in Asia/Shanghai"}],"tools":[{"name":"echo","input_schema":{"type":"object"}}]}`
	for _, route := range []string{"/v1/messages", "/v1/responses", "/openai/v1/chat/completions", "/backend-api/codex/responses"} {
		t.Run(route, func(t *testing.T) {
			router := gin.New()
			router.Use((&clientMetadataPrivacy{enabled: true}).middleware())
			router.POST(route, func(c *gin.Context) {
				if !reflect.DeepEqual(c.Request.Header, functionalHeaders) {
					t.Errorf("functional headers changed or private headers survived: %v", c.Request.Header)
				}
				got, err := io.ReadAll(c.Request.Body)
				if err != nil || string(got) != body {
					t.Errorf("body changed: %q, error=%v", got, err)
				}
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
			req.Header = functionalHeaders.Clone()
			for _, name := range privateHeaders {
				req.Header[name] = []string{"private-caller-sentinel"}
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("unexpected status %d", response.Code)
			}
		})
	}
}

func TestClientMetadataPrivacyDoesNotReadBodyAndCoversWebsocketHandshake(t *testing.T) {
	reader := &unreadBody{}
	router := gin.New()
	router.Use((&clientMetadataPrivacy{enabled: true}).middleware())
	router.GET("/v1/responses", func(c *gin.Context) {
		if c.GetHeader("User-Agent") != "" || c.GetHeader("Upgrade") != "websocket" {
			t.Error("websocket metadata was not filtered without preserving the upgrade")
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", reader)
	req.Header.Set("User-Agent", "private-caller-sentinel")
	req.Header.Set("Upgrade", "websocket")
	router.ServeHTTP(httptest.NewRecorder(), req)
	if reader.read {
		t.Fatal("metadata policy read the request body")
	}
}

func TestClientMetadataPrivacyDefaultOffAndControlPlaneUnchanged(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, route := range []string{"/healthz", "/management.html", "/v0/management/config", "/v8/management/config"} {
			router := gin.New()
			router.Use((&clientMetadataPrivacy{enabled: enabled}).middleware())
			router.GET(route, func(c *gin.Context) {
				if c.GetHeader("User-Agent") != "control-caller" {
					t.Error("control-plane metadata changed")
				}
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, route, nil)
			req.Header.Set("User-Agent", "control-caller")
			router.ServeHTTP(httptest.NewRecorder(), req)
		}
	}
	t.Setenv("YEUTECH_STRIP_CLIENT_METADATA", "")
	policy := newClientMetadataPrivacy()
	if policy.enabled || policy.snapshot() != nil {
		t.Fatal("privacy policy must be opt-in")
	}
	router := gin.New()
	router.Use(policy.middleware())
	router.POST("/v1/messages", func(c *gin.Context) {
		if c.GetHeader("User-Agent") != "existing-client" {
			t.Error("default behavior changed")
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "existing-client")
	router.ServeHTTP(httptest.NewRecorder(), req)
	t.Setenv("YEUTECH_STRIP_CLIENT_METADATA", "1")
	if got := newClientMetadataPrivacy().snapshot(); got["version"] != clientMetadataPrivacyVersion || got["bodyRedaction"] != false {
		t.Fatal("enabled policy must report its limited scope")
	}
}
