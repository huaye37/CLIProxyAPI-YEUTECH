# Codex desktop speed toggle through YEUTECH

Codex desktop only exposes its Fast and Ultrafast selector when the active
app-server provider requires the signed-in ChatGPT account. A custom provider
using `model_providers.<id>.auth.command` still accepts `service_tier`, but the
desktop UI reports no account for that provider and hides the selector.

`scripts/codex-yeutech-auth-bridge.mjs` keeps both requirements true:

1. Codex uses its existing ChatGPT login against a loopback-only provider, so
   the desktop UI can expose the speed selector.
2. The bridge removes the ChatGPT OAuth `Authorization` header before traffic
   leaves the Mac, reads the YEUTECH gateway token from Keychain, and forwards
   the unchanged request body to `https://llm-api.yeutech.cn`.

Install the bridge with:

```bash
scripts/install-codex-yeutech-auth-bridge
```

Both `yeutech` and the legacy `openai-http` provider must then use:

```toml
base_url = "http://127.0.0.1:18401/v1"
requires_openai_auth = true
```

Codex voice is a separate ChatGPT-subscription WebRTC path. It must not inherit
the ordinary model provider base URL, otherwise the call-creation request is
sent to the YEUTECH Responses gateway and returns `404 /v1/live`. Keep normal
model requests on the loopback bridge and configure only voice call creation:

```toml
experimental_realtime_webrtc_call_base_url = "https://chatgpt.com/backend-api/codex"
```

Do not set `experimental_realtime_ws_base_url` for this setup. The Codex
app-server derives and authenticates the sideband connection from the created
ChatGPT call; overriding it would incorrectly couple voice to the Responses
gateway again.

Remove their `[model_providers.<id>.auth]` blocks; Codex does not allow
`auth.command` and `requires_openai_auth` on the same provider. Keep the
provider IDs unchanged so existing threads resume through the same route.

Verification is layered:

```bash
node --test scripts/test-codex-yeutech-auth-bridge.mjs
curl --fail http://127.0.0.1:18401/_yeutech/health
codex login status
```

Finally, start a fresh `codex app-server`, call `account/read`, and require a
`chatgpt` account with `requiresOpenaiAuth: true`. `model/list` must still
advertise the intended speed tiers. A catalog-only check is insufficient.

For voice, a fresh embedded Codex app-server must accept
`thread/realtime/start` with WebRTC transport, emit
`thread/realtime/started`, and return a non-empty `thread/realtime/sdp`. A
successful health check or an HTTP route test is not a voice acceptance test.

The selector is an opt-in request control, not proof that the upstream granted
the tier. Verify the terminal response's `service_tier`. On 2026-10-10, minimal
`gpt-6.1-sol` and `gpt-5.6-sol` requests from the active Pro account returned
`default` even when `priority` was requested; a direct request to the ChatGPT
Codex backend returned the same result. This rules out the YEUTECH bridge and
proxy as the cause, but Fast must remain documented as requested rather than
granted until an upstream response reports `priority`.
