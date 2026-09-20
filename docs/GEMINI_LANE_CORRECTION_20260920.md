# Gemini history repair lane correction — 2026-09-20

## Incident

The original Codex task `专业课重构`, ID
`01a0bafe-881d-7fe0-810f-353e43b44796`, failed at 18:53 Beijing time with:
`request.contents[570].parts[2]: functionResponse.id` ending in `_178` not matching
the call ending in `_186` at `request.contents[569].parts[3]`.

The LAN/public lifecycle gateway deployed earlier read `active.json`, selecting
blue/18317/image `cli-proxy-api:yeutech-94b1be045cc4`. The already running amber
instance on 18315/image `cli-proxy-api:yeutech-d9ca2f5d6b63` includes the subsequent
parallel-history pairing fix, but was selected only by `active-v2.json`.
The prior release had verified simple inference and cancellation, not the backend
revision selected for this long Gemini history. That verification gap caused the
repaired instance to remain outside the new entry's request path.

## Correction without terminating in-flight work

- Created `novel-ai-proxy-gateway-v4`, using the same lifecycle-aware gateway code
  and immutable runtime image, with inference port 18310, private admin port 18309,
  and `GATEWAY_STATE_FILE=/state/active-v2.json`.
- The live status explicitly confirms `activePort:18315`; amber was not restarted.
- NAS nginx `llm-api.yeutech.cn` now proxies new requests to 18310. Certificate,
  DNS mapping and request cancellation handling are unchanged.
- Fresh Hong Kong transports `yeutech-inference-v4-tunnel-{1..4}` expose ports
  18351–18354 to Caddy and connect independently to NAS 18310. Old transports remain
  running. The SSH allowlist was extended only with loopback port 18310 for the
  existing Hong Kong host/user match; existing SSH sessions were retained.
- Caddy was hot-reloaded after all four tunnel probes succeeded. No model proxy,
  old gateway or Mac mini restart. The idle updater was reloaded to include v4 in
  its fail-closed drain accounting; it still counts all earlier gateways.
- Do not change `active.json` to amber: the original gateway's allowed backend
  set only includes blue and green. A blind global state change would break those
  internal consumers. Those older consumers remain unchanged by this correction.

## Acceptance and limits

- Gateway preflight selected the repaired amber revision; all four new transport
  checks returned expected unauthenticated 401 responses in 0.133–0.145 seconds.
- A synthetic stale parallel-tool-history probe completed through both old and
  repaired instances. It did not reproduce the exact task failure and is not the
  recovery acceptance gate.
- Resumed the original task, without editing/deleting its stored history or
  replacing the selected model. New turn: `01a0be79-04ce-7483-a378-21dd4e084701`.
- The resumed Gemini turn issued a new command reading existing 731-L007/L008
  lesson data; command marker `exec-caf4241c-e95c-4c59-92c6-aec5e54ca730` completed
  with exit code 0. The task subsequently remained active with no pairing error.
- This proves the original history was accepted and new tool work resumed. It
  does not mean the course-reconstruction task or all future turns are complete.
- Three deployment-accounting tests pass with all four gateways included.
- Dry-run deployment is idempotent; rollback dry-run identifies only the public
  inference block. Live rollback was not performed.

## Persistent source and rollback

NAS runtime source: `/volume1/docker/novel-ai-proxy/gateway-lifecycle-v4`.
Current LAN vhost source: `deploy/gateway/llm-api-v4.nginx.conf`.
The earlier `llm-api.nginx.conf` remains the immutable v3 deployment input.

NAS backups in the v4 directory: `bluegreen.before.py`, `sshd_config.before`,
`llm-api.nginx.before.conf`. Hong Kong Caddy backup:
`/opt/yeutech-api-manager/inference-pool-v4/Caddyfile.before-1789901948697905987`.

Public rollback, retaining ongoing v4 requests:

```sh
ssh hk-edge 'python3 /opt/yeutech-api-manager/inference-pool/deploy.py --gateway-v4 --rollback --apply'
```

That rollback restores the v3/blue lane and may reintroduce the Gemini history
error. LAN rollback should similarly be a targeted nginx upstream edit plus
validation/reload, never a forced gateway/container restart. Keep multi-gateway
deployment accounting while any of these gateways can still serve a request.

Future release acceptance must inspect actual `activePort` and its image revision
for both LAN and public inference, not just HTTP health, matching model catalogs
or the management dashboard's primary-lane display.
