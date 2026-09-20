# Gateway lifecycle and LAN routing release — 2026-09-20

## Outcomes

- Home DNS now maps exactly `api.yeutech.cn` and `llm-api.yeutech.cn` to
  `192.168.100.234`. Both were missing from the existing per-host dnsmasq list.
  Existing domains such as `agent.yeutech.cn` were correctly mapped already.
- OpenClash stays `fake-ip-tun` in rule mode. `DOMAIN-SUFFIX,yeutech.cn,DIRECT`
  bypasses proxy nodes; it does not replace public DNS with a NAS address.
- LAN inference has its own verified TLS virtual host, using the existing valid
  wildcard certificate. Before the virtual host existed, forcing `llm-api` to the
  NAS selected an unrelated default certificate. No certificate was replaced.
- Public DNS is unchanged. Off-LAN clients still reach the Hong Kong edge.
- Both inference virtual hosts allow Responses, Chat Completions, Messages and
  Messages token counting. Management APIs remain unexposed (404).

## Runtime topology and lifecycle

New container `novel-ai-proxy-gateway-v3` listens on NAS loopback port 18312;
its private status endpoint is 18311. It shares `/state/active.json` and the same
model backends as the original gateway, without copying credentials or sessions.
The exact previous gateway image digest is reused with the new source mounted
read-only. Port 18311 exposes only per-backend counts, oldest request age and
completed/disconnected/upstream-error totals, never request bodies or headers.

New public transport pool: `yeutech-inference-v3-tunnel-{1..4}`, Hong Kong ports
18341–18344, each forwarding to NAS 18312. New requests use least-current-requests
balancing. The previous pool, original tunnel, old gateways and model backends
remain running; no existing inference process was restarted.

HTTP disconnects before or after response headers now destroy the upstream
connection and release accounting once. Truncated upstream streams close rather
than append a JSON error into SSE. WebSocket peer disconnects/end/errors close both
sides, including cancellation before upgrade finishes.

Caddy's previous `flush_interval -1` explicitly prevents backend cancellation on
client disconnect. The new block uses default streaming behavior: SSE flushes
immediately without detaching cancellation. No model request retries were added.
The LAN nginx vhost also disables request/response buffering and upstream retries,
and propagates client aborts. Its read/write inactivity limits are 7 days, not an
absolute generation deadline.

The deployment controller now aggregates in-flight counts from all three gateways
and refuses to reuse a slot that any gateway still actively selects, even at zero
requests. If a registered gateway status is unavailable, replacement fails closed.
Only the idle update-controller process was restarted to load this safeguard;
its saved phase was `complete`. The model proxy and Mac mini were untouched.

## Acceptance evidence

- Five Node tests: disconnect during SSE, disconnect before headers, upstream
  truncation, WebSocket disconnect and old-stream preservation across slot switch.
- Three Python tests: all-gateway count aggregation, no reuse of an active empty
  slot, failure to read a registered gateway blocks replacement.
- Live local DNS and actual curl peer IP are `192.168.100.234`; TLS verification
  result is 0. Unauthenticated model query about 0.013 seconds; API manager login
  redirect about 0.014 seconds. These are transport/UI-entry checks, not model TTFT
  or logged-in browser acceptance.
- Real model SSE checks, each completed successfully:

| Path | Protocol / model | First text | Total |
| --- | --- | ---: | ---: |
| LAN | Responses / GPT-6 Astra | 2.910 s | 3.130 s |
| LAN | Chat Completions / GPT-6 Astra | 2.949 s | 3.164 s |
| LAN | Messages / Claude Fable 5 | 4.596 s | 4.621 s |
| HK forced IP, TLS hostname retained | Responses / GPT-6 Astra | 2.316 s | 2.495 s |
| HK forced IP, TLS hostname retained | Chat Completions / GPT-6 Astra | 2.561 s | 2.832 s |
| HK forced IP, TLS hostname retained | Messages / Claude Fable 5 | 3.012 s | 3.013 s |

These single samples are not a speed ranking of LAN vs HK; provider variation
dominates tiny probes. LAN avoids the observed shared WAN path for large inputs.
- A separately created public Responses probe was intentionally closed after its
  first SSE event (1.587 s). New gateway `disconnected` increased from 4 to 5,
  active counts returned to zero, upstream errors stayed zero.
- Old gateway still reports blue 10 / green 16. These were not reset or killed.
  Internal consumers still configured for 18319/18314 are not silently migrated;
  only new requests through the public/LAN `llm-api` entry use lifecycle-v3.
- No long-context soak, actual offsite-device test, or logged-in UI acceptance.
- Nginx validation also reports pre-existing duplicate `write.yeutech.cn` vhosts
  and duplicate wasm MIME entry. This release did not modify those unrelated sites.

## Files, persistence and rollback

Versioned gateway/updater source and tests live under `deploy/`; working API-manager
source was synchronized to the same changes. Run Node tests from `deploy/`:
`node --test test/proxy-gateway.test.mjs`; run Python test directly.

NAS immutable release source: `/volume1/docker/novel-ai-proxy/gateway-lifecycle-v3`.
LAN nginx persistent config: `/usr/local/etc/nginx/sites-enabled/yeutech-llm-api.conf`.
The NAS certificate is the existing `/volume1/docker/yeutech-portal-local/certs` pair.
SSH PermitOpen was extended only with `127.0.0.1:18312` for the existing
`fangjialiang` / `8.217.64.178` match. The SSH listener was HUP-reloaded, preserving
existing sessions. Only the newly created, still-unused v3 tunnels were restarted
to pick up this permission before the Caddy cutover.

Backups:

- Router: `/etc/config/dhcp.before-api-lan-20260920-182907`.
- NAS release: `bluegreen.before.py` and `sshd_config.before`.
- HK: `/opt/yeutech-api-manager/inference-pool-v3/Caddyfile.before-1789900233915054155`.

Public rollback (dry-run without `--apply`):

```sh
ssh hk-edge 'python3 /opt/yeutech-api-manager/inference-pool/deploy.py --gateway-v3 --rollback --apply'
```

This returns new public requests to the original four-tunnel pool without stopping
the v3 streams. LAN rollback can surgically remove the two exact dnsmasq list
entries and reload dnsmasq; retain v3 until all existing requests drain. Do not
restore an entire old router config over future unrelated changes. Do not revert
multi-gateway deployment accounting while v3 is still serving requests.

Existing clients may keep established connections or cached DNS temporarily;
do not restart active sessions just to force route migration. New DNS lookups on
the checked Mac use LAN; no per-client base URL change is required.

References: [Caddy streaming semantics](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming),
[OpenClash mode discussion](https://github.com/vernesong/OpenClash/discussions/2857).
