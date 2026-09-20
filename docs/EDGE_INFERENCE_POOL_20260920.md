# Edge inference transport pool — 2026-09-20

## Scope and diagnosis

The public `llm-api.yeutech.cn` endpoint previously sent all requests through one
SSH TCP connection: Hong Kong `172.18.0.1:18319` to NAS `127.0.0.1:18319`.
During the incident, unauthenticated model-list requests stalled for 12–15 seconds
while the NAS returned 401 in approximately 1 ms. The SSH socket had roughly 1 MB
queued and active TCP retransmissions. It later recovered without a restart.
This establishes intermittent transport contention, not a permanent model delay
or proof that all perceived UI latency has the same cause.

## Deployed topology

- Caddy selects `host.docker.internal:18331` through `18334` using `least_conn`.
- Four `yeutech-inference-tunnel-{1..4}` containers each own an independent SSH
  connection to NAS gateway `127.0.0.1:18319`. Explicitly disable SSH multiplexing.
- All four still use the same physical WAN and NAS gateway; this is not four times
  the bandwidth and does not increase upstream account quotas.
- Model POST retries remain disabled. No new model response or stream timeout.
- Health probes use unauthenticated `/v1/models`, expecting 401. Probe timeout is
  3 seconds, interval 10 seconds, two failures/passes to transition health.
  Health probes do not invoke models or carry credentials.
- Existing low-latency stream flushing is preserved.
- The management site retains its separate transport on port 18321.
- Existing port 18319/18320 tunnel remains running to drain old requests and allow
  a rollback. No NAS gateway, model proxy, management service or Mac mini restart.
- GPT/Gemini Web executors remain disabled. No provider/model routing changes.

## Release and rollback

Canonical deployment script: `scripts/edge-inference-pool.py`.
On `hk-edge`, deployed at `/opt/yeutech-api-manager/inference-pool/deploy.py`.
Dry-run is the default; `--apply` creates only the new pool with `--no-recreate`,
checks all four transports, validates Caddy, and reloads only a surgically changed
inference block. Existing containers are not recreated. The exact current SSH
image digest is reused, with the existing read-only key mount; no secret is copied.

Persistent pool definition: `/opt/yeutech-api-manager/inference-pool/compose.json`.
Pre-release backup:
`/opt/yeutech-api-manager/inference-pool/Caddyfile.before-1789898324388679393`.

Rollback only this block, preserving other sites and existing streams:

```sh
ssh hk-edge 'python3 /opt/yeutech-api-manager/inference-pool/deploy.py --rollback --apply'
```

Do not run `docker-compose down` or stop any transport to roll back. New streams
may still be using the pool. Future portal edge releases must retain the matching
pool block in `yeutech-home-platform/Caddyfile`; the deployment script rejects
unexpected block drift rather than overwriting it.

## Verification

- Surgical patch/rollback roundtrip, unrelated site preservation and drift
  rejection checked locally. Caddy 2.11.4 validated and hot-reloaded the candidate.
- All four transport preflight checks returned 401 in 0.122–0.141 seconds.
- Before deployment: 4 concurrent public GPT-6 Astra Responses requests completed;
  first text 2.374–2.985 seconds, total 2.510–3.182 seconds.
- After deployment: 8 concurrent public GPT-6 Astra Responses requests completed;
  first text 2.447–3.988 seconds, total 2.652–4.694 seconds. All eight emitted
  `response.completed`. Caddy live counters showed exactly 2 requests on each of
  the 4 pool members, with no recorded failures at that snapshot.
- 16 unauthenticated public transport probes at concurrency 8: 16/16 expected
  responses before and after. Before p50/max 0.411/0.601 seconds; after
  0.403/0.521 seconds. 401 is expected authentication rejection, not model success.
- Original Caddy, inference tunnel and management tunnel container start times
  unchanged after release.
- No browser/UI acceptance, long-context soak, induced failure test or sustained
  lossy-WAN benchmark performed. The short probes do not establish a speedup for
  long agent sessions or prove intermittent WAN stalls are eliminated.

Use `scripts/edge-inference-bench.py` for bounded public probes. Model mode reads
the API key from stdin and emits only timings/status, never keys or response text.
Model concurrency remains governed by upstream limits; four transport workers
are not a four-session application limit.

## Follow-up if long sessions remain slow

Measure large-context upload time, transport retransmission/queued bytes and model
TTFT separately. More tunnels cannot fix a saturated physical WAN or provider
queue. Consider a different WAN path only after collecting evidence under actual
long-session load; do not add blind retries or shorten model stream deadlines.

References: [Caddy reverse proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
for least-current-requests balancing, active health checks, retry defaults and
streaming behavior.
