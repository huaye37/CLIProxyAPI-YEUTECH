# Isolated Claude edge

This deployment keeps Claude OAuth files on the edge host and exposes inference
only through a private SSH tunnel. NAS consumers receive a separate bridge key.
No credentials belong in this directory or in Git.

## Runtime

- Pin `CLAUDE_EDGE_IMAGE` in the host's `.env` to the verified immutable image ID.
- Generate the bridge key on the host and replace the single placeholder in
  `config/config.yaml`; keep the key and configuration mode `0600`.
- Keep `auth/` mode `0700`. Only one running edge service owns token refresh.
- Publish container ports only on `127.0.0.1`. NAS uses port 18340.
- `YEUTECH_MEMORY_MAX_REQUESTS=2` reduces the core admission limit to two. It
  cannot raise the original ceiling. The server limits decoded requests to
  64 MiB, and the existing memory guard reserves compressed/chunked bodies
  conservatively before reading. No extra HTTP proxy or stream deadline is added.
- Request logging and the management panel are disabled. Application logs rotate.
- The server does not rewrite caller system prompts as a different application.
- `YEUTECH_STRIP_CLIENT_METADATA=1` is enabled only on this isolated edge. The
  core defaults to off. On inference routes it removes recognized caller device,
  SDK, browser-hint, language, timezone, location and original-IP headers before
  logging and protocol handling. Authentication, protocol capabilities, security
  proofs and conversation session headers remain available. `/healthz` reports
  `clientMetadataPrivacy.version=yeutech-client-metadata-v1` and
  `bodyRedaction=false` when enabled.
- This policy does not sanitize arbitrary headers or request bodies. Client-added
  system prompts, workspace paths, tool results, uploaded file contents, account
  identity and provider-generated metadata remain separate. It is not a location
  anonymity or regional eligibility guarantee.
- This Linux/amd64 deployment explicitly sets Claude header defaults to
  `os: Linux` and `arch: x64`, matching `uname -sm` on the server. The shared
  executor otherwise defaults to `MacOS/arm64`; seeing those defaults would not
  prove that a caller's Mac had been inspected. Server TZ and Claude
  `header-defaults.timezone` now use `Asia/Tokyo`. This controls the server clock
  and the date helper when invoked; it does not rewrite caller context, dates
  already present in history, or task execution paths. No location/GPS field is
  invented or added.

## Interactive authorization

The user requested the browser login itself to originate in Tokyo. The selected
flow uses `yeutech-claude-login.service`: an unprivileged Chromium desktop on the
edge host, shown through noVNC on loopback port 6080. Forward it to a local port:

```sh
ssh -N -L 127.0.0.1:16080:127.0.0.1:6080 tokyo-edge
```

Open `http://127.0.0.1:16080/vnc.html?autoconnect=true&resize=scale` locally. The
page displays pixels from the Tokyo browser; the Google/Claude webpages and their
network requests run in Tokyo. The user completes sign-in and any verification.
Do not read browser cookies or Google credentials. Keep the browser sandbox
enabled. VNC and its web interface listen only on loopback, behind SSH access.
Explicitly disable both x11vnc's and LibVNCServer's IPv6 listeners with `-no6
-noipv6 -rfbportv6 -1`. On this Debian build, `-no6` alone still left the library
listening on IPv6 port 5900. Verify actual sockets after each desktop start:
only `127.0.0.1:5901` and `127.0.0.1:6080` may serve the desktop.

Once the user has signed in, start the OAuth command below and open its fresh URL
inside the Tokyo browser. Its `localhost:54545` redirect then reaches the edge
container directly; no Mac callback tunnel is needed. Confirm that the token is
stored by the proxy and perform a minimal model call. Stop the desktop service
and the Mac display tunnel when no longer needed. The desktop is not enabled at
boot. Its initial two-hour runtime limit was removed after the user chose a
multi-day Free website trial; stop it manually when that trial ends. Its protected
profile supports later re-login.

The following local-browser callback alternative is documented for maintenance;
it does not meet the user's requirement that the browser login originate in Tokyo.

Check that local port 54545 is unused, then establish a short-lived callback tunnel:

```sh
ssh -N -L 127.0.0.1:54545:127.0.0.1:54545 tokyo-edge
```

In a separate terminal, start the existing OAuth command:

```sh
ssh -t tokyo-edge docker exec -it yeutech-claude-edge ./CLIProxyAPI -config /run/claude-edge/config.yaml -claude-login -no-browser
```

Open its authorization URL in the user's browser. The user selects their account
and completes any sign-in or consent steps. The `localhost:54545` callback reaches
Tokyo through the tunnel. The login waits five minutes; create a fresh flow after
expiry rather than reusing an old URL. Close the callback tunnel after completion.

## Acceptance

Verify unauthorized access, model listing, ordinary and streaming messages,
tool-call round trips, client cancellation, two concurrent requests, oversized
and compressed-body rejection, then a naturally refreshed credential. Before
authorization, run protocol and ingress checks against an isolated mock upstream.
Do not treat an empty model catalog or a healthy process as account acceptance.

Only enable the NAS candidate route after real authorization and response checks.
Keep existing providers and default model choices unchanged. Subscription use,
regional eligibility and the upstream's account decisions remain separate from
technical connectivity.

## Deployment record: 2026-10-14

- At 23:55 Asia/Shanghai (00:55 on October 15 in Tokyo), the user requested
  consistent Tokyo-facing environment values. The API container TZ and Claude
  timezone default were changed to `Asia/Tokyo`, keeping the existing image,
  Linux/x64 defaults and ingress header filter. Credential inspection found no
  device, timezone or custom-header overrides. The existing timezone selection
  regression passed; Compose validation and the actual container clock confirmed
  JST/+0900. A real Sonnet 5.5 Messages request then passed in 1.664 s.
  Backup: `/opt/yeutech-claude-edge/backups/tokyo-profile-20261014T155505Z`.
  No browser-profile, host-wide timezone, NAS context, or main gateway route was
  changed. The code/configuration behavior is verified; how the provider evaluates
  account location is not observable from this probe.
- At 23:10 Asia/Shanghai the isolated Tokyo service was updated to
  `sha256:fcf5d7018c277a5acf41760b350854c993eb4cb243034ad87b541d31e5e98ace`
  (`yeutech-claude-edge:privacy-20261014-v1`), binary SHA-256
  `acc6f99839b3851f1943bd2e63fdda63bdd810783f6fab9de786973c0e0044a5`.
  Health confirmed the header privacy policy enabled, body redaction disabled,
  concurrency two, zero active requests and no OOM/restart state. Configuration
  and image rollback files are under
  `/opt/yeutech-claude-edge/backups/privacy-20261014T151021Z`.
  Use `/usr/bin/docker-compose` (1.29.2) on this host; Docker Compose v2 is absent.
- Metadata-change verification: gofmt, focused metadata and memory admission
  tests, configured Claude header/profile and streaming regressions, and Go
  build passed. Two isolated proxy layers with synthetic credentials verified
  caller-header sentinels absent at the mock upstream while body context stayed
  intact. Messages, Responses, SSE completion, tool-call/result round trip,
  unauthorized/oversized requests and concurrency recovery passed. These mock
  tests made no external model calls and used the API-key path, not a captured
  real OAuth exchange. Post-update real Sonnet 5.5 calls passed: Tokyo Messages
  text (1.494 s), NAS-tunnel Responses SSE (first delta 6.004 s, complete at
  6.408 s), and automatic tool/result round trip (2.400 s + 1.212 s). These are
  individual samples, not a performance or long-term stability benchmark.
- The NAS AI workbench was inspected separately: the running `yeutech-agent`
  image is `yeutech-agent:opencode-1.19.0-1c0c0fc9b0c8`, with project storage
  mounted at `/projects` and its gateway still at loopback port 18319. The
  corresponding OpenCode source adds working directory, workspace root,
  platform and date to system context. The older portal/Mac execution path also
  adds an `Asia/Shanghai` output-date rule. Header filtering does not remove
  those contexts, and neither SSH nor a VPN removes them. This was a runtime
  placement plus source audit, not a full capture of live workbench payloads.
- Current status after the user enabled Pro: OAuth completed at 21:30 Asia/Shanghai
  for the expected account. One credential file is present with access/refresh
  tokens and mode 0600, under the mode-0700 Tokyo auth directory. No token was
  copied to NAS. Initial access-token expiry is 2026-10-15 05:30 Asia/Shanghai;
  natural refresh has not yet been observed.
- Real acceptance results: Tokyo Sonnet 5.5 Messages text passed (1.275 s); NAS
  through the private tunnel passed Sonnet 5.5 Responses SSE (first delta 1.867 s),
  automatic tool selection and tool-result round trip (1.720 s + 1.021 s), and
  Opus 5.5 Messages text (1.697 s). These are individual short-request samples.
  A forced tool-choice probe returned HTTP 400: this model does not support
  `tool`/`any`; the successful retry used `auto`. No claim of all-model or
  full-consumer compatibility follows from these probes.
- The main NAS gateway still has no new Claude API-key channel. Its existing
  model routes and consumer policies are unchanged. The user requested a region,
  account-risk and GitHub architecture review before expanding the integration.
- Current architecture is a direct CLIProxyAPI OAuth executor, not the official
  Claude Code binary or Agent SDK. Source review found OAuth still enables the
  CLI fingerprint policy even with `disable-claude-cloak-mode: true`. That flag
  must not be represented as disabling every client-identity imitation.
- Source/configuration inspection found no configured forwarding of the caller's
  `X-Forwarded-For`, `Forwarded` or `X-Real-IP` to Anthropic. Public IPv4 egress
  was verified as the Tokyo host address. This is not a packet-capture audit and
  cannot establish anonymity, regional eligibility, official approval or account
  safety. The proposed next candidate uses the unmodified official CLI/SDK with
  a narrow local API adapter; that alternative is researched but not deployed.

### Earlier milestones

- User update at approximately 19:20 Asia/Shanghai: the user completed website
  sign-in in Tokyo and chose to use the Free website for several days before
  considering a subscription. The visible account page reported Free and the
  active Chrome (Linux) session in Tokyo. The official authentication guide
  lists Pro/Max for personal subscription access to Claude Code:
  `https://code.claude.com/docs/en/authentication`.
  At that point, website sign-in was complete and proxy OAuth was still pending.
  The user subsequently enabled Pro. Do not purchase a subscription or treat a few
  uneventful days as proof of future account eligibility or stability.
- During browser use, the desktop service occupied about 961 MiB and the idle
  edge API container about 24.53 MiB. These are snapshots, not peak-load bounds.
  Free website trials still require the browser; the future API request path
  would not use it. The initial two-hour session stopped at 21:05 Asia/Shanghai.
  At the user's reconnect request, the runtime limit was removed and the service
  restarted using the same private profile. VNC remains reachable only via SSH.
- Worktree: `codex/claude-edge-20261014`, based on
  `30f765471b277a369fa4bb87664c84aeddc95b88`; changes remain uncommitted.
- Tokyo service: `/opt/yeutech-claude-edge`, container `yeutech-claude-edge`.
  Image ID:
  `sha256:755ed67457edf98d220c87e3bc57add37bd8e19a156c9fcee42ef8eb7ab57984`.
  Binary SHA-256:
  `732d7f395d4304528194897fafb902efd7c01ab5561fcbced6fd9d756fa76d03`.
- Edge budget: 3 GiB container, `GOMEMLIMIT=2GiB`, `GOMAXPROCS=2`, two
  admitted requests. API and callback bind to loopback 18340 and 54545.
- NAS tunnel: `/volume1/docker/yeutech-claude-edge-tunnel`, container of the
  same name. Image ID:
  `sha256:8df7b258a2d2dff70c368e16aee73dba3f51c59a3f9e622b7bf5fd60e2ecc7b8`.
  NAS loopback 18340 forwards to Tokyo loopback 18340. The dedicated SSH
  account allows this destination only. Private key mode was corrected to 0600.
- Completed earlier in this deployment: targeted memory admission, OAuth code
  exchange/refresh, Responses wire protocol and Claude streaming tests;
  CGO-enabled build; two isolated proxy layers against a mock Claude upstream
  covering unauthorized access, Messages, Responses, SSE completion,
  tool-call/result round trip, oversized requests and concurrency recovery.
  No real Claude inference requests were made during those synthetic tests.
- NAS tunnel health and authenticated empty model catalog returned HTTP 200.
  Existing NAS production providers, version and defaults were not changed.
- Infrastructure inspection: edge running, no OOM state, health reports concurrency 2;
  desktop configured without a runtime deadline. VNC and
  noVNC listen only on IPv4 loopback, with no IPv6 VNC listener. The local
  display tunnel listens only on `127.0.0.1:16080`; the Tokyo browser subsequently
  displayed the successful OAuth callback.
- Pending: main NAS gateway integration, cancellation and long-history checks,
  natural token refresh, controlled
  disconnect/restart recovery, sustained load and consumer acceptance.
  Initial health checks do not establish these pending outcomes.
