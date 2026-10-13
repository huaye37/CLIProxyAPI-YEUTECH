package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

type unreadBody struct{ read bool }

func (b *unreadBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (*unreadBody) Close() error               { return nil }

func TestMemoryAdmissionLargeRequestsAndRecovery(t *testing.T) {
	m := &memoryAdmission{enabled: true}
	entered, finish := make(chan struct{}), make(chan struct{})
	r := gin.New()
	r.Use(m.middleware())
	r.POST("/v1/responses", func(c *gin.Context) {
		if c.GetHeader("X-Test-Hold") == "1" {
			close(entered)
			<-finish
		}
		c.Status(http.StatusOK)
	})
	first := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}"))
	first.ContentLength = 80 << 20
	first.Header.Set("X-Test-Hold", "1")
	done := make(chan struct{})
	go func() { defer close(done); r.ServeHTTP(httptest.NewRecorder(), first) }()
	<-entered
	body := &unreadBody{}
	second := httptest.NewRequest("POST", "/v1/responses", body)
	second.ContentLength = 80 << 20
	w := httptest.NewRecorder()
	r.ServeHTTP(w, second)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" || body.read || !strings.Contains(w.Body.String(), "proxy_memory_busy") {
		t.Fatalf("second request was not safely rejected: %d read=%v", w.Code, body.read)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatal("small independent request was blocked")
	}
	close(finish)
	<-done
	w = httptest.NewRecorder()
	r.ServeHTTP(w, second)
	if w.Code != 200 || m.active != 0 || m.bytes != 0 || m.large {
		t.Fatal("reservation leaked after completion")
	}
}

func TestMemoryAdmissionLimitsBeforeReadingAndLeavesControlPlaneAvailable(t *testing.T) {
	m := &memoryAdmission{enabled: true}
	r := gin.New()
	r.Use(m.middleware())
	r.Any("/v1/responses", func(c *gin.Context) { c.Status(200) })
	r.GET("/v1/model-capabilities", func(c *gin.Context) { c.Status(200) })
	b := &unreadBody{}
	req := httptest.NewRequest("POST", "/v1/responses", b)
	req.ContentLength = memoryBodyLimit + 1
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 413 || b.read {
		t.Fatal("oversize body was read")
	}
	for range memoryMaxRequests {
		if !m.acquire(memoryMinReservation) {
			t.Fatal("unexpected admission failure")
		}
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/model-capabilities", nil))
	if w.Code != 200 {
		t.Fatal("catalog was blocked")
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}")))
	if w.Code != 503 {
		t.Fatal("request count limit was ignored")
	}
	for range memoryMaxRequests {
		m.release(memoryMinReservation)
	}
}

func TestMemoryAdmissionPreservesZstdAndChunkedAndReleasesOnPanic(t *testing.T) {
	m := &memoryAdmission{enabled: true}
	r := gin.New()
	r.Use(gin.Recovery(), m.middleware())
	r.POST("/v1/responses", func(c *gin.Context) {
		if m.bytes != memoryBodyLimit {
			t.Error("compressed or chunked request was under-reserved")
		}
		body, err := handlers.ReadRequestBody(c)
		if err != nil || string(body) != "{\"input\":\"ok\"}" {
			t.Errorf("body changed: %s %v", body, err)
		}
		panic("test release")
	})
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	compressed := encoder.EncodeAll([]byte(`{"input":"ok"}`), nil)
	for _, encoding := range []string{"zstd", "chunked"} {
		var req *http.Request
		if encoding == "zstd" {
			req = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(compressed))
			req.Header.Set("Content-Encoding", "zstd")
		} else {
			req = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":"ok"}`))
			req.ContentLength = -1
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 500 || m.bytes != 0 || m.active != 0 {
			t.Fatal("panic leaked reservation")
		}
	}
}

func TestMemoryAdmissionHonorsSmallerConfiguredLimit(t *testing.T) {
	m := &memoryAdmission{enabled: true}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(handlers.MaxRequestBodyBytesContextKey, int64(4)) }, m.middleware())
	r.POST("/v1/responses", func(c *gin.Context) {
		_, err := handlers.ReadRequestBody(c)
		var max *http.MaxBytesError
		if !errors.As(err, &max) {
			t.Errorf("missing body limit: %v", err)
		}
		c.Status(413)
	})
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("12345"))
	request.ContentLength = -1
	r.ServeHTTP(httptest.NewRecorder(), request)
	if m.bytes != 0 {
		t.Fatal("read failure leaked reservation")
	}
}
