package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStartWebSubscriptionSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	driver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/login/sessions" || r.Method != http.MethodPost {
			t.Fatalf("unexpected driver request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://login.example.test/session/1","session_id":"session-1"}`))
	}))
	defer driver.Close()
	t.Setenv("WEB_SUBSCRIPTION_GEMINI_URL", driver.URL)
	t.Setenv("WEB_SUBSCRIPTION_DRIVER_TOKEN", "test-token")

	router := gin.New()
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	router.POST("/web-subscriptions/:channel/start", handler.StartWebSubscriptionSession)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/web-subscriptions/gemini-web/start", strings.NewReader(`{}`))
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"session_id":"session-1"`) {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}

func TestStartWebSubscriptionSessionRejectsUnknownChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	router.POST("/web-subscriptions/:channel/start", handler.StartWebSubscriptionSession)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/web-subscriptions/unknown/start", nil)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestStartWebSubscriptionSessionRequiresHTTPSLoginURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	driver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"url":"http://127.0.0.1:6080/"}`))
	}))
	defer driver.Close()
	t.Setenv("WEB_SUBSCRIPTION_CHATGPT_URL", driver.URL)

	router := gin.New()
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	router.POST("/web-subscriptions/:channel/start", handler.StartWebSubscriptionSession)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/web-subscriptions/chatgpt-web/start", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
}
