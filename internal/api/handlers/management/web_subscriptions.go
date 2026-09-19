package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const webSubscriptionResponseLimit = 64 << 10

var webSubscriptionHTTPClient = &http.Client{Timeout: 60 * time.Second}

type webSubscriptionStartResponse struct {
	URL       string `json:"url"`
	State     string `json:"state,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ExpiresIn int    `json:"expires_in,omitempty"`
}

// StartWebSubscriptionSession asks an isolated web driver to create a login session.
func (h *Handler) StartWebSubscriptionSession(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	baseURL, ok := webSubscriptionDriverURL(channel)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported web subscription channel"})
		return
	}
	if baseURL == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "web subscription driver is not configured"})
		return
	}

	target, errParse := url.Parse(baseURL)
	if errParse != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "web subscription driver URL is invalid"})
		return
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/v1/login/sessions"
	target.RawQuery = ""
	target.Fragment = ""

	payload, _ := json.Marshal(gin.H{"channel": channel})
	req, errRequest := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, target.String(), bytes.NewReader(payload))
	if errRequest != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create web subscription request"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(os.Getenv("WEB_SUBSCRIPTION_DRIVER_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, errDo := webSubscriptionHTTPClient.Do(req)
	if errDo != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "web subscription driver is unavailable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, webSubscriptionResponseLimit+1))
	if errRead != nil || len(body) > webSubscriptionResponseLimit {
		c.JSON(http.StatusBadGateway, gin.H{"error": "web subscription driver returned an invalid response"})
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("web subscription driver returned HTTP %d", resp.StatusCode)})
		return
	}

	var result webSubscriptionStartResponse
	if errDecode := json.Unmarshal(body, &result); errDecode != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "web subscription driver returned invalid JSON"})
		return
	}
	loginURL, errLoginURL := url.Parse(strings.TrimSpace(result.URL))
	if errLoginURL != nil || loginURL.Scheme != "https" || loginURL.Host == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "web subscription driver returned an invalid login URL"})
		return
	}
	result.URL = loginURL.String()
	c.JSON(http.StatusOK, result)
}

func webSubscriptionDriverURL(channel string) (string, bool) {
	var name string
	switch channel {
	case "gemini-web":
		name = "WEB_SUBSCRIPTION_GEMINI_URL"
	case "chatgpt-web":
		name = "WEB_SUBSCRIPTION_CHATGPT_URL"
	default:
		return "", false
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/"), true
}
