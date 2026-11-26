package pluginhost

import (
	"os"
	"strconv"
	"strings"
)

// DefaultMaxPluginBodyBytes bounds the raw request body size forwarded across
// the C ABI to full-body plugin hooks (ModelRouter, RequestInterceptor with
// body, RequestBodyPatcher, RequestNormalizer, and request-body copies on
// response/stream interceptors). Oversized multimodal payloads bypass full-body
// plugin copies and fall through to native CPA execution so multi-runtime Go
// .so heaps cannot amplify a single large request into a container OOM.
const DefaultMaxPluginBodyBytes = 16 << 20 // 16 MiB

const maxPluginBodyBytesEnv = "YEUTECH_PLUGIN_MAX_BODY_BYTES"

// MaxPluginBodyBytes returns the effective byte ceiling for full-body plugin
// C ABI dispatch. Setting YEUTECH_PLUGIN_MAX_BODY_BYTES=0 disables the limit.
func MaxPluginBodyBytes() int {
	raw := strings.TrimSpace(os.Getenv(maxPluginBodyBytesEnv))
	if raw == "" {
		return DefaultMaxPluginBodyBytes
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return DefaultMaxPluginBodyBytes
	}
	return n
}

// MaxPluginBodyBytes implements the optional handler body-limit detector on Host.
func (h *Host) MaxPluginBodyBytes() int {
	return MaxPluginBodyBytes()
}

func exceedsPluginBodyLimit(body []byte) bool {
	limit := MaxPluginBodyBytes()
	return limit > 0 && len(body) > limit
}
