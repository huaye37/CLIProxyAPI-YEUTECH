package api

import (
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

const clientMetadataPrivacyVersion = "yeutech-client-metadata-v1"

// clientMetadataPrivacy minimizes caller environment headers at a dedicated
// gateway boundary. It never rewrites request bodies or invents device/location
// values. Provider authentication, server-generated metadata, and the network
// peer address remain outside this policy.
type clientMetadataPrivacy struct {
	enabled bool
}

func newClientMetadataPrivacy() *clientMetadataPrivacy {
	return &clientMetadataPrivacy{enabled: os.Getenv("YEUTECH_STRIP_CLIENT_METADATA") == "1"}
}

func (p *clientMetadataPrivacy) snapshot() gin.H {
	if p == nil || !p.enabled {
		return nil
	}
	return gin.H{"version": clientMetadataPrivacyVersion, "scope": "caller_environment_headers", "bodyRedaction": false}
}

func isClientEnvironmentHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"x-forwarded-", "x-stainless-", "sec-ch-", "x-device-", "x-client-device-", "x-geo-", "x-claude-remote-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "forwarded", "x-real-ip", "x-client-ip", "true-client-ip", "cf-connecting-ip", "cf-ipcountry", "fastly-client-ip", "x-original-forwarded-for",
		"user-agent", "accept-language", "referer", "cookie", "x-app", "x-client-app",
		"x-machine-id", "x-hardware-id", "x-client-version", "x-client-os", "x-client-platform", "x-client-arch",
		"x-timezone", "x-time-zone", "time-zone", "x-user-timezone", "x-client-timezone", "x-country-code", "x-location":
		return true
	default:
		return false
	}
}

func (p *clientMetadataPrivacy) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if p != nil && p.enabled && isExampleAPIKeySafeModeProxyPath(c.Request.URL.Path) {
			// Delete the actual map key, including non-canonical casing. Run before
			// loggers, protocol detection, and executor options copy the headers.
			for name := range c.Request.Header {
				if isClientEnvironmentHeader(name) {
					delete(c.Request.Header, name)
				}
			}
		}
		c.Next()
	}
}
