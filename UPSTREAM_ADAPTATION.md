# Unified v8.0.29 adaptation gate

Checked 2026-10-15 Asia/Shanghai. GitHub's latest non-draft, non-prerelease
release is v8.0.29 (published 2026-10-14T16:19:43Z). The official tag is an
ancestor of this isolated candidate. NAS and Tokyo share this source; neither
production deployment is changed by these source commits.

## Issue 6559: two independently reproduced causes

1. A config reload parsed version N, then read the file again after its callback
   and recorded the hash of concurrently saved N+1. Subsequent reloads skipped
   N+1 as already applied, leaving the last compatible model absent indefinitely.
   `configApplyMu` serializes complete reloads and the applied hash now identifies
   the pre-load observation, never a later unread write.
2. A queued, unversioned auth-file observation could overwrite a management
   prefix save before its post-persist hook registered the new models. The old
   observation incremented runtime Generation, so the newer persisted snapshot
   could then be rejected as stale. File synthesis now attaches an in-memory
   source digest. Register/Update validate it under the existing per-auth
   mutation gate, before taking the global manager lock. Stale/removed/uncertain
   source observations do not replace current auth. Accepted digests are cleared
   before persistence and publication, so later management edits are unaffected.

Deterministic regressions fail against the original implementations using Go
overlays: config ports observed `[8317]` instead of `[8317,8318]`; stale auth
overwrites `new` with `old`; a removed source registers an auth. All pass with
the fixes. The existing Issue6559 test and its assertions are unchanged.

- Unmodified isolated candidate: 14 failures / 50 original regression runs.
- Config-hash fix alone: 6 failures / 100, all final auth-prefix registration;
  this intermediate green full-suite run was NOT treated as resolution.
- Both fixes: original regression 200/200; both concurrent config/auth tests
  and targeted source/fencing regressions with `-race -count=50` passed.
- Go 1.26.4 `go test -p 4 ./...`: all 103 tested packages passed.
- No real account, paid inference, credential refresh or production restart was
  used for these tests. Synthetic concurrency proof is not a provider benchmark.

## Upstream changes and consumer boundaries

| Upstream commits / function | Current provider and call chain / files | Frontend | Evidence and conclusion |
| --- | --- | --- | --- |
| `5fc580b1`, `08e5762a`, `81573f04`, `e230a04b`, `e235fb3d`: reload, deletion, registration fencing | All existing NAS/Tokyo auths; management persist -> watcher -> auth manager -> model registry; `internal/watcher`, `sdk/cliproxy/service_auth.go`, `sdk/cliproxy/auth`, `sdk/cliproxy/model_registry.go` | No new control | Adapted with the two fixes above; deterministic red/green, unchanged concurrency regression, race checks and full suite. Deployment validation remains separate. |
| `345cc9d5`, `b3dc6c3f`, `56eee6a4`, `51952a7b`: TLS handshake and HTTP/2 lifecycle | Tokyo Claude and existing NAS providers; request transport -> executor retry; `internal/runtime/executor/claude_tls_handshake_retry_test.go`, `helps/utls_client_handshake_timeout_test.go`, `sdk/httptransport`, `sdk/cliproxy/auth/conductor_transport_retry_test.go` | None for unchanged defaults | Adapted protocol paths tested locally. `requests.tls-handshake-timeout-seconds` is the supported canonical setting; zero remains disabled. No new UI setting or configured timeout is claimed. Existing optional Tokyo headers remain opt-in. |
| `5efe9758`, `a0fd8448`, `57d005fe`, `58257007`, `b0db5448`: tool schemas and IDs | Existing Gemini/Antigravity, Claude, Codex tools; consumer tool -> translator/schema sanitizer -> executor -> response IDs; `internal/util/gemini_schema*`, `internal/runtime/executor/helps/codex_tool_schema*`, `internal/translator/{gemini,claude,openai}` | No new control | Adapted; union, integer-bound and multi-turn ID regression tests pass. No new upstream tool authorization is inferred. |
| `77fd91e8`, `97ba1e89`, `02fc76b1` (+ merge `1fbb06d5`, `fb87727d`): tool_search, deferred cache, wrapped MCP names | Existing local client/workbench tools -> Responses/Claude/Gemini translation; `internal/translator/gemini/openai/responses/*tool_search*`, `internal/translator/claude/openai/responses/*tool_search*`, `internal/runtime/executor/claude_executor_tool_search_test.go`, `claude_executor_request_remap_test.go` | No new model parameter | Protocol adaptation tests pass. Actual paid, provider-side tool_search execution was not tested; no blanket new capability claim or automatic enablement. |
| `7795de19`, `912a038b`, `7f2f17d7`: SSE, cached usage, malformed tool errors | Existing Codex and Gemini/Antigravity -> SSE scanner / translator -> consumer usage and retry; `helps/codex_sse_scanner*`, `internal/translator/antigravity/claude`, `internal/translator/gemini/claude`, `sdk/api/handlers/claude/code_handlers_error_test.go` | No new control | Adapted with scanner, usage and structured-error fixtures. No visible text/tool replay is added; provider failure recovery in live accounts remains untested. |
| `0d18d3b3`: per-model output limit | Existing Claude -> OpenAI Chat translation -> model registry; `internal/translator/claude/openai/chat-completions/claude_openai_request*` | No new control | Adapted via existing optional max-token contract and default-limit tests. GPT 500k and 80% compression policy is not changed. |
| `8fb633c9`: Antigravity client fallback | NAS Antigravity executor -> request headers; `internal/misc/antigravity_version*` | No | Adapted fallback 2.23.0; local version tests. No credential or regional routing changes. |
| `6143cc11`, `866d84d0`, `a19a8ecd`, `f62d4d28`, `4f9630df`: model catalog / native search | Catalog -> account entitlement intersection -> consumers; `internal/registry/models/models.json`, `sdk/cliproxy/antigravity_models.go`; Tokyo pinned catalog -> image pair | Existing model selection | Catalog definitions only, not proof of calls. Preserve existing enabled NAS and 11 validated Tokyo models. New models require separate minimal evidence before registration. Staging must reject removal of installed Tokyo IDs. |
| `50eaed0d`: plugin defaults | Existing plugin management -> SDK schema; `sdk/pluginapi/types.go`, `internal/api/handlers/management/plugins_test.go` | Existing plugin settings only | Serialization/default tests pass. No new plugin or supplier enabled. |
| `40ce0e01`, `f9ac397b`, `4c159069`: Kimi schema, Meta Muse models | No configured Kimi/Meta supplier in the scoped deployment | No | Not applicable to enabled consumers; retain upstream code, do not enable providers or advertise live availability. |
| `f88b6b2f`, `c96d9c92`, `39ace7e9`, `50bfc50f`, `333f62b3`, `02efbf13`, `a4a545c0`: upstream README / merges | Documentation only | No | Not applicable to runtime. Documentation does not establish entitlement or deployment. |

All existing local clients, AI workbench, novel text, independent image entry,
Kaoyan assistant, explanation/grading and other policy-controlled consumers keep
their model IDs, defaults, saved reasoning/speed values, histories, quota source
and material contract. Image requests do not inherit text reasoning/speed modes.
No Fast/Ultrafast grant or changed `response.service_tier` is claimed.

## Release gates

Source validation is separate from paired artifacts and deployment. Run only the
authoritative manager's `scripts/stage-unified-proxy.py`; verify NAS proxy,
paired manager and Tokyo catalog/image manifests against one full source commit.
This document alone does not set either node ready. User-triggered installation,
real paid provider scenarios and production observation remain separate evidence.

## v8.0.30 integration on 2026-10-15

The official release appeared at 21:55:09 Asia/Shanghai while the v8.0.29 paired
candidate was being checked. The updater correctly refused to call that older
pair current. This candidate merges the v8.0.30 tag on top of maintenance commit
191515d0, including the Hong Kong relay sources and incident-preservation contract.

The watcher conflict is semantic: both branches introduced configApplyMu. Keep
one mutex, use upstream SourceHash for the exact bytes actually parsed, and retain
the existing auth-file source-digest fence from 20b4a41d. Neither the previous
config/auth regression nor its assertions is removed. Catalog IDs were checked
for duplicates after the automatic merge.

| Upstream change | Existing call chain and implementation | Frontend / consumer impact | Verification boundary |
| --- | --- | --- | --- |
| 1631875b management-save/reload fence | All NAS/Tokyo config writes -> management handler -> watcher -> runtime; internal/config/config_load.go, internal/api/handlers/management, internal/watcher | No new control. Preserve settings and current auth registration. | Resolve the duplicate mutex and parsed-source hash as above; repeat Issue6559/6631 and local snapshot tests with race detection, then full suite. |
| 35af74f9 stale success/cooldown fence | Existing provider execution -> auth conductor/model cooldown; sdk/cliproxy/auth/conductor_*.go | Existing cooldown/quota UI consumes the same state shape; no new account or retry policy. | Issue6643 tests cover in-flight success, persistence and real post-cooldown success. No account load test is claimed. |
| 9db0729f, 4e8a4125 Claude header operator overrides | Tokyo Claude executor/header defaults and fingerprint policy | Optional operator configuration, not a user model-generation parameter. Existing Linux/x64, Asia/Tokyo and caller-header filtering remain. Do not enable new diagnostic/header modes merely because supported. | Upstream override/config tests plus existing Tokyo profile/privacy protocol tests; no anonymity or account-policy guarantee. |
| 59930ba3 incomplete Responses tool status | Local client/workbench/novel/Kaoyan tools -> Responses-to-Claude translator -> tool_result.is_error | Existing tool result/error path; no UI parameter or changed tool authority. | Translator regression covers incomplete statuses; synthetic tool round trip remains required in both-node staging. |
| e9ba5c50 unsupported discriminator removal | Existing Gemini/Antigravity tool schemas -> internal/util/gemini_schema.go -> provider | Shared protocol adaptation for all policy-controlled consumers; no changed model settings. | Schema fixtures and executor suite; does not authorize new tools. |
| ceb4397f, 2d7f5377 (+ 5be2cdab) Vertex thought-only tail and full publisher URLs | Vertex execution/content normalization and config/vertex_compat.go | No Vertex supplier enabled in this deployment; optional existing base-URL field already carries the full prefix. Independent image path keeps its own contract. | Retain upstream tests, no paid Vertex claim or new supplier registration. |
| bbfd7886 (+ 207d119d) xAI web-search arguments | xAI Responses request builder | No enabled xAI supplier here; no frontend or model registration change. | Upstream request-builder tests; not a live web-search claim. |
| f9437700 pre-upload hashing and remote store seed | Startup/config normalization -> Git/Postgres/object storage seed | NAS/Tokyo currently use file-backed config/auth. Do not migrate storage or rewrite credentials for this feature. | Config-load/seed regression suite; no production remote-store migration. |
| 74592556 Gemini 4 Argon definition | Registry -> entitlement intersection -> live consumer capability catalog | Existing model selectors and saved preferences; preserve account filtering and custom metadata. A definition is not new account access. | Duplicate-ID check plus registry tests; no automatic provider/model enablement. |

The affected shared paths serve local clients, the AI workbench, novel text,
independent image generation, Kaoyan assistant/explanation/grading and other
policy-controlled entries. Existing IDs, histories, defaults, quota sources,
GPT 500k/80% compression, independent speed/reasoning fields and material contract
are unchanged. Source tests, prepared paired manifests, production installation
and real long-workload acceptance remain separate outcomes.

## 2026-10-16: Codex rate-limit diagnostics at the local auth bridge

Tokyo's read-only live quota observation at 01:21 Shanghai time reported the
five-hour window at 100%, resetting at 01:50, while the seven-day window was 7%
used. The node remained reachable, accepting and profile-valid; the credential
was present, refreshable and not disabled. This is quota evidence, not an account
ban. NAS preserved HTTP 429/model_cooldown and Retry-After, but installed Codex
0.162.0-alpha.17.2 discarded the error body and rendered only the retry-limit text.

scripts/codex-yeutech-auth-bridge.mjs now adapts only official Codex POST
/v1/responses calls accepting text/event-stream: a bounded JSON HTTP 429 error
becomes one standard response.failed event with a Chinese explanation and bounded
upstream retry advice in Asia/Shanghai. The Responses turn remains failed, with
empty output and upstream_status=429; there is no fabricated assistant answer.
Ordinary clients, non-streaming calls, successful streams, other HTTP statuses,
malformed/oversized bodies and compressed errors retain their original transport.
Codex Live routing and OAuth replacement remain unchanged.

The pure formatter lives in scripts/codex-yeutech-errors.mjs and is installed
alongside the bridge. It distinguishes explicit quota exhaustion, model cooldown,
ordinary rate limits, concurrency and Fast entitlement errors. A generic cooldown
does not prove which subscription window ran out, and absent retry advice does
not receive an invented reset time. Raw upstream messages, credentials, paths and
account identifiers are not copied into the display text.

Validation: 14 Node assertions/integration tests pass, including unchanged Fast
payload/auth isolation, Live routing, bounded fallback and failed-event semantics;
Go build ./... passes. A loopback-only HTTP fixture reproduced the exact screenshot
error in the installed Codex CLI. Through the candidate bridge, the same native
client reports the Chinese cooldown and retry time as turn.failed (exit 1), with
its configured two stream retries finishing in 2.18 seconds. No paid inference
was used for this client regression. Local runtime deployment is a separate gate;
this source change does not install or restart NAS or Tokyo proxy binaries.

Daily maintenance must preserve both bridge modules and the installer's new copy
step. Do not overwrite the running bridge with the old one-file implementation
while syncing catalogs or Fast support. Preserve the provider IDs, credentials,
saved model choices, historical chats and existing inference traffic on rollout.

## 2026-10-17: full registered-model audit and shared capability repair (not deployed)

Scope is all 56 models registered by the live NAS proxy, not only the local
Codex picker. Evidence is under `docs/audits/2026-10-17-*.json`; the summary has
one row per model, provider, call chain, implementation, frontend requirement,
actual result and remaining acceptance boundary. No production configuration,
consumer allowlist, credential, tunnel, gateway or proxy process was replaced.

| API / feature | Providers and call chain | Implementation | Frontend / consumers | Evidence | Conclusion |
| --- | --- | --- | --- | --- | --- |
| Shared capability enrichment | Registered routes -> standard /v1/models, capability API and Codex client catalog | registry/capability_metadata.go; model_registry.go; client/codex/models/models.go | Every catalog reader; GPT policy unchanged | DeepSeek was 1,048,576 in capabilities but 272,000 in the Codex-shaped catalog. Provider-scoped enrichment and override-preservation regressions pass. Unknown entries no longer borrow a GPT window. | Source repaired, not installed |
| Independent image/video/speech metadata | xAI exact media IDs -> existing generation, edit, retrieval and speech handlers | server_model_capabilities.go, exact-ID metadata enrichment | Opt-in /v1/model-capabilities?schema=2; manager contract and BFF parser deltas prepared separately | Eight NAS direct image responses contain valid image bytes, two speech responses contain audio, three video jobs reached done with a URL. Gemini image uses the business plugin and returned an artifact/dimensions/hash. | Basic output verified; editing, video playback and speech quality untested |
| Backward-compatible capability rollout | Existing strict capability-v1 BFF and new schema=2 consumers | Schema opt-in on the same authenticated endpoint; no new management exposure | Old v1 fields/workloads remain compatible; new BFF accepts media without treating it as a text agent | v1/v2 regression tests; BFF source files matched installed NAS SHA256 before edits; 233 BFF tests pass | Source repaired; precise agent image not prepared or deployed |
| Public media API paths | Public domain -> HK Caddy -> existing inference forwarding -> NAS | Separate portal Caddy matcher delta only | Other API clients; no consumer whitelist changes | All public media paths returned HTML 404. Live Caddy matcher excluded them. NAS-side calls succeeded. Candidate config passes Caddy adapt, no reload performed. | Source repaired; public entry remains blocked until authorized rollout |
| Actual function tools | Text-model routes -> chat/completions -> tool result continuation | Existing executors; audit scripts | Workbench, writing, Kaoyan and other text agents | 36 models passed tool/result roundtrips. Tokyo Claude 5.5 and DeepSeek require auto tool choice in the tested reasoning modes; forced selection failures remain in the original evidence. Grok 4.20 Multi-Agent failed both forced and auto custom-tool requests. | Do not claim universal agent/tool support |
| Model identity and account entitlement | Existing xAI OAuth and ChatGPT-account Codex routes | Existing runtime routing, no alias rewrite | Do not enable unresolved entries just to fill the picker | Mini/Mini Fast returned grok-4.3; Composer Fast returned grok-4.5-build; GPT 5.5 was explicitly unsupported on the current ChatGPT route. xAI authenticated model catalog advertises four Build entries with context_windows [256000,500000]. | Three identities unresolved; GPT 5.5 not applicable to current route; no full-window load proof |
| Speed and quota attribution | Same accounts and routes | No speed defaults changed | No Ultrafast or paid route enabled | Grok 4.7 Fast returned its Fast model ID but final service_tier remained default. This is requested Fast, upstream grant unconfirmed, not measured acceleration. | Preserve Standard recovery; quota charging source unverified |

Validation at this checkpoint: Go 1.26.4 full `go test ./...` and `go build ./...`,
focused post-change tests, audit-script tests, manager 94 Node/71 Python tests,
and BFF 233 Node tests passed. These are distinct from browser, production,
max-window, long-session and independent business-image acceptance.

Release remains held: no exact NAS/Tokyo/manager paired candidate was prepared.
The authoritative manager tree still has its pre-existing paused UI merge;
this work uses isolated deltas and does not resolve or overwrite that scene.
The business image plugin still needs a provider-specific xAI parameter adapter
before exposing Grok images to the novel/workbench consumers. Preserve existing
selections, history and the relay; do not publish a partial or falsely verified
catalog. Reconcile these failures and consumer acceptance, then stage from the
authoritative manager tree and leave proxy/paired installation to the user.

### 2026-10-17 continuation: consumer corrections and stable v8.0.32

The preceding hold is historical. The isolated manager combines authoritative
280d8e4 with verified live UI 72a2e6b; the original dirty authority tree remains
untouched. The Agent candidate retains official v1.19.0 ancestry, the installed
fd6dc0a2d interface and live maintenance guards. Container hashes (not the stale
NAS source directory) establish that baseline.

| Commit / feature | Current provider and call chain | Implementation / frontend | Evidence and conclusion |
| --- | --- | --- | --- |
| fa68946a config ETags | All providers; manager -> v8 config | manager server.mjs/app.js If-Match and reread | Adapted. Isolated desktop save conflict disables saving; reread permits save with readback. |
| 7a511e4d validated stream body | Tokyo Claude -> Responses | executor / translator; no new UI | Merged; Go regression gate. Candidate long-stream acceptance untested. |
| 202b8b8c tool arguments | Tokyo Claude tool/result chain | Claude executor/schema cleanup; no new UI | Merged; historical auto-tool probes remain historical, not candidate proof. |
| 7604d3df session date retention | Tokyo Claude; OAuth stays in Tokyo | Claude executor/config; upstream default retained | Merged; natural credential-switch continuity not yet observed. |
| 911f49cb preserved thinking | Tokyo Claude thinking/tools | Claude executor; no additional setting | Merged; source/regression evidence only until candidate use. |
| 088e4457 terminal disconnect | Existing streaming providers | Meta executor tests; no frontend | Upstream regression retained. |
| b946d54e Devin quota | No registered Devin account | Auth conductor / Devin executor | Not applicable to current routes; no supplier enabled. |
| 355fd10e plugin shadow copies | Optional dynamic providers | pluginhost / extension loader | Retained; no new plugin installed. |
| e7003d2a relay documentation | No configured relay | Upstream README | Not applicable; no relay account/routing added. |
| xAI image business adapter | xAI -> shared image plugin -> business consumers | Agent image-generation-plugin.mjs; aspect ratio at 1k, declared quality only | Local HTTP/BFF tests cover policy-safe default, invalid-quality recovery, actual dimensions, idempotent replay. Earlier NAS image bytes are real-provider evidence; new adapter not yet paid-tested. |
| Identity and tool corrections | xAI -> shared catalogs -> consumers | capability_metadata.go; capability v1/v2; Codex visibility | Mini/Mini Fast/Composer stay in history but are not offered for new selection until identity is resolved. Multi-Agent is conversation-only after both custom-tool probes failed. |

Official xAI image documentation was read on October 17: aspect_ratio, resolution,
response_format; quality low/medium/auto is declared only for image-2.0. No exact
pixel guarantee, text reasoning/speed parameter, resolution upgrade, paid-route
switch or consumer allowlist widening was invented.

Local real-browser checks cover manager media details, context save conflict and
readback, and desktop/390x844 Agent failed-send recovery and preference persistence.
These use isolated fixture upstreams with no paid model calls. Draft recovery was
ported into the split UI modules rather than lost in the presentation merge.
All-business production UI acceptance, maximum context, Fast grant and public HK
media routing are separate pending gates. No production process was restarted.
Final candidate identities must be recorded separately after staging.

## 2026-10-18: Tokyo 413 transport-limit correction

| Feature / source | Provider and call chain | Implementation / frontend | Evidence and conclusion |
| --- | --- | --- | --- |
| 64 MiB edge rejection of an existing 97,919,776-byte request | Codex -> unchanged local bridge -> NAS -> existing HK SSH relay -> Tokyo Claude | `deploy/claude-edge/config.example.yaml` sets 128 MiB; `set_body_limit.py` performs guarded, backed-up hot reload; no new frontend or parameter | Applied to the existing c91552e2 Tokyo runtime, not a new binary. Same image/start time and zero restarts. Config changed only the body limit, gate restored. |
| Memory and protocol safety | Exact Tokyo image, isolated on NAS, synthetic Claude provider only | `deploy/claude-edge/large_body_probe.py` exercises actual Chat/Responses translation and existing admission | Original-size plus 128 MiB requests passed; concurrent large request 503, oversized 413; peak 1,734,488,064 bytes, no OOM, no paid inference. Test-harness startup/stream fixtures corrected before production. |
| Existing private transport | NAS -> HK -> Tokyo | Full 97,919,776-byte invalid JSON through existing tunnel | 400 after body admission, 9.672 seconds. No tunnel, gateway or credential change and no paid call. Original conversation retry is still user acceptance, not established by this probe. |
| Other consumers and budgets | Local client, workbench, novel text/image, Kaoyan assistant/explanation/scoring and media consumers | Existing shared contracts unchanged | No context/compression/model policy, Fast/Ultrafast, quota, history or media parameter change. NAS limit remains unchanged. Shared binary candidates require normal paired staging after this source commit; installs remain manual. |

Runtime backup and exact boundaries are documented in `deploy/claude-edge/README.md`.
# 2026-10-18 Codex Grok hosted-search compaction compatibility

| Upstream behavior / feature | Provider and consumer path | Implementation / UI | Evidence and conclusion |
| --- | --- | --- | --- |
| xAI hosted tools report cumulative input over all reasoning steps; Codex 0.163.0-alpha.10.2 uses last response total for active-context limits | Grok 4.7 Fast -> xAI Responses -> Codex desktop | No usage rewriting; native standalone search feature separates search from inference; no new UI | Incident rollout 01a14e14-e6ab-7d41-896c-4880f9661b2e recorded input 1,128,483, then a compaction with input 43,035, then search input 1,015,785. Root cause reproduced with installed engine and current catalog. |
| Codex standalone search selects a Codex credential, not a Grok inference model | Codex web.run -> local bridge -> gateway /v1/alpha/search -> existing Codex search subscription | scripts/codex-yeutech-search.mjs; bridge and installer; features.standalone_web_search=true on client | Live search with Grok model returned 503, missing model returned 400, verified search model gpt-6.1-sol returned 200 with non-empty results. Route selection is search-only; Grok inference and real usage unchanged. |
| Regression boundaries | Local Codex client; all its models use standalone search when enabled | scripts/test-codex-standalone-search.mjs; existing auth/error tests | 19 tests passed, including actual installed engine: old hosted-search cumulative usage causes 1 compaction; independent search makes 1 search and 2 inference calls with 0 compactions; genuine oversized context still causes 1 compaction. 500K window and 400K threshold unchanged. OAuth replacement, Live, Fast payload and 429 display preserved. Go build passed. |
| Other consumers and native X search | Workbench, novel, study and arbitrary third-party clients | No changes in this client-specific fix | Not claimed fixed or tested here. They retain their existing native tool routing and billing. Standalone Codex search is not xAI-native X search. Production proxy/Tokyo/manager installation is not authorized by this local repair. |
| xAI custom code-mode tool payload mismatch | Real installed Codex -> Grok -> functions.exec | scripts/refresh-yeutech-model-catalog selects direct ordinary tools for actual xai provider metadata | Two isolated real probes stopped at their validation deadlines with incompatible exec payloads; they were failures, not successful search acceptance. Ordinary-tool mode then completed one standalone search and the final Grok response, engine exit 0. |
| Actual local client activation | Existing provider IDs and OAuth loopback bridge; existing Grok account for inference and Codex subscription for independent search | Three bridge modules plus catalog generator installed; existing LaunchAgent drained at activeRequests=0, SIGTERM only | Health version 20261018-standalone-search-v1. Config adds standalone_web_search=true. Deep comparison confirms only two Grok tool_mode fields changed in the 15-model catalog; all windows, 80% thresholds, other model data, Fast fields and history unchanged. Backup: ~/.codex/backups/grok-search-20261018-1725. |

The successful minimal live probe used Grok 4.7 Fast with low effort in a new,
ephemeral, isolated CLI session. One web_search began and completed, followed by
the final answer and turn.completed. Reported real turn usage was input 414652,
cached input 2304, output 134, reasoning output 92. This is cumulative turn
usage, not a claim that the final context occupied 414652 tokens, nor evidence
of a granted Fast service tier. The interrupted user conversation was not resumed.
The direct-tool catalog was additionally run through the same installed-engine
regressions (5/5 passed); unchanged bridge/auth regression suite passed 14/14.

Official behavior checked against xAI `developers/tools/tool-usage-details` and
the installed Codex engine plus OpenAI's core/session/context_window.rs and
core/context_manager/history.rs. Live end-to-end and activation evidence must
be recorded separately from these deterministic tests; do not resume the user's
interrupted thread as a test. See deploy/MAINTENANCE_CN.md for rollout boundaries.

## 2026-10-24: built-in Codex provider HTTP fallback (candidate, not activated)

| Feature / observed behavior | Provider and call chain | Implementation / frontend | Evidence and conclusion |
| --- | --- | --- | --- |
| Built-in `openai` probes Responses WebSocket; the previous bridge stripped Upgrade and forwarded an ordinary GET | Installed Codex -> loopback auth bridge -> existing YEUTECH gateway | `scripts/codex-yeutech-auth-bridge.mjs` locally returns HTTP/1.1 426 for Responses upgrades; no client UI patch | Reproduced upstream GET returning 400 and failed turn, including with request compression disabled. Native engine recognizes local 426 and falls back to POST/SSE. No model alias, body field or account identity rewrite is needed. |
| OAuth isolation, streaming and cancellation | Existing local Codex provider IDs, Gemini/GPT/Claude/Grok/DeepSeek catalog | Same credential replacement, three-module installation, Live and search routing | New mock tests verify no handshake reaches upstream, exact zstd request bytes, unchanged 503 admission error, cancellation and SIGTERM drain. Installed engine completes with compression on/off against a loopback fixture; upstream receives exactly one POST and the gateway sentinel credential, never the OAuth token. |
| Real built-in-provider acceptance | Ephemeral installed Codex -> isolated candidate bridge -> existing gateway -> Gemini 4 Argon | Process-only `model_provider="openai"`, `openai_base_url` pointing to the temporary port, `features.enable_request_compression=false` | Account type ChatGPT and requiresOpenaiAuth=true, rateLimits readable, five model families present. Minimal real response is OK and turn.completed. This is one short Gemini call, not all-model or full-window acceptance. |
| Compressed requests and memory admission | Same public gateway with concurrent production requests | Existing `internal/api/memory_admission.go`, unchanged | zstd POST returns explicit 503/proxy_memory_busy: compressed bodies reserve the full decoded budget. This is separate from the GET/400 defect. Deployment profile disables wire compression, not context compaction; no memory ceiling, model window, retry semantics or usage is weakened. |
| Safe local activation | Existing LaunchAgent and local provider history | Installer refuses busy/unhealthy/unmanaged bridges; existing service uses SIGTERM and KeepAlive, never bootout or forced kickstart | Shell syntax checked; active SSE drain test passes. Live bridge remained 20261018-standalone-search-v1 with active requests; no local runtime/default-provider or production proxy change made by this checkpoint. Dot visibility still requires desktop restart and user acceptance after activation. |

Validation: the initial complete bridge/search/installed-engine suite passed 25
tests with no skips; the added SIGTERM drain and cancellation checks passed
separately. Official `openai_base_url` configuration support was verified at
https://learn.chatgpt.com/docs/config-file/config-advanced . Keep the existing
`openai-http` and `yeutech` provider entries for historical threads. Do not edit
the application bundle, feature gates, account, context limits or conversation
history to restore the sidebar entry. NAS/Tokyo/manager candidate readiness is a
separate manifest/image gate and is not implied by this local bridge result.

## 2026-10-24: official v8.0.41 semantic integration

The official tag is `8289ec4c71ad914d0612c2776352b76ba3d355a6`, 112 commits
(100 non-merge commits) after v8.0.33. The release is merged as a real Git parent,
not copied files with an invented ancestry marker. The legacy compatibility
branch is a publication mirror; `huaye37/yeutech/apps/model` remains the only
maintenance authority. The parent workflow must verify exact model-tree equality
and record both mirror and monorepo identities before paired staging.

The starting mirror `5e764e547001e076f24a3f0840a5ce5d45f69ec2` already contains
Lite video routing and `c91552e2` diagnostics. The verified monorepo HTTP fallback,
draining installer, three bridge modules and passive preview checker were carried
forward before the merge. No production credentials, provider settings, gateway,
Tokyo network profile, client config or running process are changed by this tree.

| Upstream commits / function | Current provider and call chain / implementation | Frontend / consumers | Evidence and conclusion |
| --- | --- | --- | --- |
| `d5816d5e`, `7484a521`, `5fe203d5`, `57a12996`, `d69962e4`, `8bd17955`: snapshot, file-read and edit fencing | Existing NAS/Tokyo providers; management -> file watcher -> auth manager; `internal/watcher`, `sdk/cliproxy/auth/conductor_lifecycle.go`, `internal/api/handlers/management` | Existing account/config forms; no new generation setting | Semantic merge retains local source digest, upstream runtime cooldown watermark and pending provider reloads. `ClearFileSnapshot` clears both observation representations for explicit persisted login handover; stale observations remain rejected. Real-file Vertex fixture replaces a nonexistent hardcoded path. Full tests and focused repeated race checks are the gate; no credential rotation is performed. |
| `feb63984`, `4138edb1`, `904ec710`, `2f6f2924`: refresh, request-scoped 401, credits recovery and guardian affinity | Existing Codex/Antigravity and shared auth selector; `conductor_refresh`, `conductor_cooldown`, `selector`, `session_cache` | Existing quota/retry and agent flows, no new control | Upstream regression tests retained. A request-only 401 does not disable a credential; parent affinity remains same-provider eligible. No real refresh/credits exhaustion or cross-account claim is inferred. |
| `d50a1995`, `6bbd2cb7`, `bb2d8f82`, `88041f0c`, `ae3ee3be`, `06fd5b8b`, `d946c4fb`: SIWC bind/status and Codex exclusion | Codex credential manager and `/v8/management/oauth/siwc/*`; `codex_siwc_bind.go`, management SIWC files, Codex auth builder | Optional new binding UI is not enabled; normal Codex clients remain original non-SIWC credentials | Retained v8-only endpoints, symlink/account replacement fences and tests. Existing accounts are not rebound. New SIWC UI is not required for the scoped existing routes and is not claimed available. |
| `0db19025`, `e7d4c731`, `b9bb031d`, `9e79552f`: search identity, history and WebSocket authorization | Local Codex -> loopback bridge -> alpha search / Responses; Codex executor/search, response steering and WebSocket handlers | Existing search and agent tools, no new UI | Original OAuth search selection and per-turn authorization preserved. HTTP fallback stays local; 429 failure semantics and token isolation retained. Installed-engine loopback tests cover both wire encodings; no repeated paid call. |
| `8df4e42a`, `99d1698f`, `009f8d7a`, `376015c3`: direct tools, explicit parallel=false, standalone effort and commentary | All non-Codex catalog routes / Claude and Codex translators; `internal/client/codex/models`, thinking and translator packages | Existing local model picker and saved independent effort/speed settings | Non-Codex catalog correctly advertises direct tool mode and disables Responses-lite. Tests verify required nullable keys, explicit parallel=false and effort translation. GPT 500k and 80% compression policy unchanged. |
| `c449529e`, `aadb34f9`, `c2dfec28`, `d495e24c`, `43f27e3a`: Claude stop, refusal, overflow and pause | Tokyo Claude -> Chat/Responses translator -> all text consumers; Claude executor and translator stop-reason tests | Existing error/retry UI; no new setting | Regression fixtures retain tool_use, length/incomplete and explicit context_length_exceeded. No fabricated success or extra compaction retries. Real full-window/long-conversation acceptance is not claimed. |
| `9d453bc0`, `ab9c06b4`, `cddc9bb8`, `df860eb9`: Claude search-result restore/cache | Claude Responses session cache; `internal/cache`, Claude web-search helpers/translators | Optional operator `client.codex.web-search-result-cache`; disabled by default, no automatic UI toggle | Session/credential isolation and deferred tool-output tests retained. Existing independent Codex search continues unchanged. Home KV integration is inapplicable while Home is not configured. |
| `084b9ca7`, `20c9095c`, `d2e61d19`, `df07b1e5`, `e1fb2521`: Claude cloak/header, empty text and empty object tools | Tokyo OAuth and Claude API-key paths; Claude request, fingerprint and tool schema code | No newly enabled cloak or identity control | Existing Tokyo Linux/x64, Asia/Tokyo optional metadata filter and private routing stay unchanged. Tests cover default/passthrough headers and exact tool arguments; no regional/account safety claim. |
| `a4660260`, `88ac14f3`, `772df04b`, `38706390`, `e5434f65`, `7a0b7563`, `674dbe8f`, `cc5682c5`, `be9fbd94`, `a68c7d8e`: reasoning signature replay | Existing Gemini/Antigravity/Codex/xAI and generic Responses translation; `internal/signature`, `internal/translator` | Tool/result continuation for all text consumers; no new UI | Cross-family rejection, ordered thought/function signatures and private RouterSuffix stripping tests retained. Meta-specific replay supported in code, no Meta account enabled. |
| `c558d320`, `154d5144`, `29150a22`: schema references and sibling constraints | Gemini/Antigravity tools -> `internal/util/gemini_schema.go` -> upstream | Existing tools; no frontend parameter | Ref sibling/literal preservation and readOnly/writeOnly cleanup regressions retained; schemas are not relaxed to conceal errors. |
| `80aa2b1d`, `02e9e852`, `45501610`, `b12db8c3`, `3f5d3a16`, `83cb1b95`: Gemini/Antigravity replay, Hub, finish and limits | NAS Antigravity/Gemini executor and catalog; provider caches and Gemini translation | Existing model catalogs and error paths across consumers | Cache version invalidation, mixed tools, blocked prompt and terminal usage tests. No provider added or account/region changed. Token-limit metadata is not a full-window invocation proof. |
| `d9451cb6`, `d44f8682`, `88732ad5`: Vertex partner/OpenAPI routing | Optional Vertex executor/config/OAuth model settings | No configured Vertex supplier in scoped deployment; no UI activation | New settings and regional endpoints retained and tested. Not applicable to active routes; no API key or paid partner model enabled. |
| `45ccd9c3`, `8a3e7622`, `5026f365`: xAI aliases and outbound User-Agent | Existing xAI image/video/speech and text paths; image handler and xAI executor | Independent media clients; no text effort imposed on media | Prefix/vendor alias fixtures and existing Lite video create/edit/extend/polling tests preserved. Caller metadata filter remains effective on Tokyo. No paid video/image generation or playback performed. |
| `07b5a7a1`, `b90190db`, `e198f3cd`, `7db0935a`, `8289ec4c`, `f434192a`, `9d463e39`: usage, service tier and TTFT | Shared usage reporters -> consumer usage displays; Claude/Codex/xAI/OpenAI-compatible and Interactions routes | Existing usage/speed controls, no tier authorization change | Upstream records effective outbound tier after payload rules and provider speed signals; request/default is not asserted to be a granted Fast/Ultrafast tier. Input/usage not rewritten to hide compaction. Token/TTFT fixtures retained. |
| `344e0ef9`: Kimi/Meta remote compaction v2 | Optional Kimi/Meta Responses executors | No such active supplier enabled | Code and tests retained; not applicable to currently enabled routes. No automatic context policy change. |
| `4846a289`, `13874f1f`, `1ad175b6`, `50cfd238`, `a014ba0e`: plugin body, model callbacks and scheduler | Existing pluginhost / management model definition reads; plugin SDK | Existing configured plugins only, no new plugin installed | Decoded body slots, per-credential model lookup, scheduler lock and race fixtures retained. Optional host API addition does not itself provide a consumer feature. |
| `befcba23`, `e45f9426`, `888f2382`, `4d34502f`, `07feedf7`: Home rebalance, latency, RESP bounds and model errors | Optional Home control path; `internal/home`, API Home routes | No configured Home cluster; no UI activation | Local fixture caught blocking RPC cancellation through redis pool Close; direct owned-socket cancellation restores all four heartbeat expiry tests without touching dispatch sockets. RESP bounds, telemetry and catalog tests retained. No Home server rollout. |
| `5de9de9a`, `030d17ed`, `77af3795`, `80d04e19`, `83189e5f`, `c0e619d5`, `95e15777`, `3b153145`, `f4a2d040`, `74e7ff60`, `ac3cd66f`, `ad09c779`: translator allocation/cache improvements | All existing translated text/tool streams; corresponding translator/util packages | No UI change | Upstream correctness and allocation tests retained, including race-specific allocation exclusion. No benchmark or latency promise from compilation. |
| `633a294f`: Go/dependency update; `4337f1f0`: log path escaping | Build toolchain, Git-backed store and access logger | No UI change | Use actual Go 1.26.10, never lower go.mod to installed 1.26.4. New go-git packhandle missing-file error classified narrowly; corruption fixture reopens unlinked pack handles. Missing auth/config and permission errors remain failures. Log escaping regression retained. |
| `ceb045a1`: release workflow; `d232ee10`, `ce07d189`, `3f0cef01`: sponsors/docs | CI and documentation only | None | Upstream release workflow edit intentionally not carried to mirror because workflow scope is unavailable; workflow bytes equal old mirror. Local builds still use required Go 1.26.10. Sponsor docs are not runtime capability or endorsement. |

Two textual conflicts were resolved explicitly: `Watcher` has one
`configApplyMu` plus upstream `pendingProviderReloadAuths`; OpenAI-compatible
`wire-api: responses` retains `/responses`, no Chat `stream_options`, and counts
effective reasoning/tier only after final payload rules. Additional all-suite
failures were repaired, not skipped: blocked Home RPC cancellation, go-git missing
pack classification/fixture handle freshness, dual snapshot-clear semantics and
a Vertex synthesis fixture with an actual on-disk source.

All consumer categories remain in scope: local clients, AI workbench, novel text,
independent image/video/speech, Kaoyan assistant/explanation/scoring and other
policy-controlled clients. These transparent protocol fixes require no new model
parameter or broadened allowlist; saved choices, history, existing model IDs,
context policy, independent speed/effort fields and quota attribution stay intact.
New optional SIWC, Vertex, Home and cache controls are not silently enabled.
This source ledger does not assert all-consumer browser acceptance, real refresh,
full-window/long-stream stability, paid media playback or upstream Fast grants.

Final validation evidence is recorded outside the immutable source tree by the
parent release workflow: full Go test/build, repeated targeted race checks,
29 Node tests with the installed Codex engine and no skipped tests, exact tag
ancestry and workflow equality. NAS, paired manager and Tokyo manifests/images,
idle-window installation and post-cutover health are separate parent gates.

## 2026-10-25: official v8.0.42 maintenance candidate

Official release `v8.0.42` (`bc0643405a1d0adf77dd0f109972ac3313d66397`)
contains eleven commits after v8.0.41. Maintenance remains in the canonical
monorepo `apps/model`; the legacy branch is only an exact-tree publication
mirror. This scheduled run prepares candidates only. NAS and Tokyo were both
observed running v8.0.41 before preparation; no installation is authorized here.

| Upstream commit / feature | Existing provider / actual consumer chain | Implementation and UI requirement | Evidence / conclusion |
| --- | --- | --- | --- |
| `0d3ca1d3`, `50147aec`: Git corruption and cancelable Redis RPC | Optional Git credential store and Home RPC; not a new supplier | `internal/store/gitstore.go`, `internal/home/client.go`, `cluster_balance.go`, `cluster_latency.go`; no UI | Semantically merged with the existing missing-pack fence and owned-socket cancellation. Add upstream missing-index classification but retain exact Git pack-path validation, so missing auth/config and permission failures still fail closed. New regression rejects a bare misleading packhandle message. Repeated race tests cover store and Home; no live credential reset or Home activation. |
| `ab139b45`: shared K3.1 Pro thinking replay | Optional Kimi executor; no Kimi account enabled by this run | `internal/runtime/executor/kimi_thinking_replay.go`; no UI or catalog expansion | Upstream model-variant and replay fixtures pass. Not applicable to current configured routes; not advertised as a newly available model. |
| `da21f1e5`: Antigravity signatures and streaming call indices | Existing Gemini/Antigravity -> Responses/Chat -> local Codex, workbench, novel text, study assistant/explanation/scoring and other policy-controlled clients | `antigravity_reasoning_replay.go`, `antigravity_executor_request.go`, Gemini and Antigravity Chat translators; no new parameter/control | Non-stream native response shape, preceding thought-signature association, caller-scoped replay and stable streaming tool index fixtures pass. Transparent protocol adaptation is implemented. No account/login/context/allowlist change; real long-conversation and all-consumer end-to-end acceptance are not claimed. |
| `9d08f0d4`: cross-origin redirect credential filtering | Plugin HTTP bridge for existing operator-configured plugins | `internal/pluginhost/http_bridge.go`, `host_callbacks.go`; no frontend | Cross-origin redirect fixtures cover credential stripping and unchanged allowed same-origin behavior. Security fix implemented; no plugin installed or enabled. |
| `44d1ddf5`: count_tokens response shape | Plugin executor count -> registered protocol translator -> consumer token counting | `adapters_executors.go`, `sdk/translator/registry.go`; no UI | Token-count fixtures verify counts remain counts rather than fabricated model completions. No rewrite of real inference usage or compaction thresholds. |
| `35e834da`: Retry-After metadata | Plugin executor errors -> RPC/stream bridge -> existing error and retry handling | `sdk/pluginabi/types.go`, pluginhost `rpc_client.go`, `stream_bridge.go`; existing error UI | Upstream error-metadata and streaming fixtures pass. Preserve bounded 429 formatter and upstream timing; do not classify every rate limit as exhausted quota or invent reset times. |
| `e42eee6e`, `c9af5290`: final payload and canonical thinking pipeline | Existing configured plugin executors -> model capability lookup -> thinking conversion -> final user payload rules | `adapters_executors.go`, `internal/thinking`, executor `helps/model_capabilities.go`; no new frontend field | Plugin model/suffix/native-format fixtures and payload-order tests pass. Final payload rules remain the last semantic mutation. Independent speed/effort and saved parameter contracts unchanged; no claim of upstream Fast authorization. |
| `179d43dc`: request-log filename traversal | All enabled request logging -> local writer; Tokyo logging policy unchanged | `internal/logging/request_logger_writer.go`; no UI | Path traversal fixtures and repeated race tests pass; no runtime logs, permissions or auth files removed. |
| `bc064340`: outdated third-party README entries | Documentation only | README translations; no frontend/runtime | Removed only the upstream-obsolete entries. Not a change to configured consumers or supplier availability. |

The three merge conflicts were in Home cancellation and Git corruption handling,
including the corruption fixture. The upstream fixture now closes the original
repository before unlinking and reopens it for the observation; local strict
classification is retained instead of accepting arbitrary missing-file messages.
All regression tests are retained, including the new upstream tests.

Validation before publication: actual Go 1.26.10 full tests/build; three repeated
race runs for Home/store/pluginhost/thinking/logging; installed Codex engine
loopback tests for standalone search, 429, OAuth replacement, HTTP fallback,
both request encodings, cancellation and smooth drain; API tests, updater tests,
source identity tests and static material-contract build. No paid inference or
media request is required to repeat those deterministic protocol fixtures.

Model catalogs, client bridge modules, installer, Fast/Ultrafast settings,
GPT 500k/other-model capability limits, 80% compaction, provider IDs, history,
consumer policy, Tokyo private ingress/profile and NAS-HK-Tokyo relay are unchanged.
Independent image/video/speech remain separate workloads; no text reasoning
control is added to media. NAS proxy, paired manager and Tokyo exact manifests,
browser acceptance and any failure are recorded outside this frozen source tree.
Source validation does not imply candidate readiness or production installation.

## 2026-10-26: official v8.0.43 maintenance candidate

Official v8.0.43 is `307803510bce87f986fcf7b1a84190f105c7b4aa`, published
2026-10-25T15:37:22Z, with eighteen commits after v8.0.42. The canonical
monorepo subtree is merged with the real upstream tag; the legacy repository
remains an exact-tree publication mirror, never a second development tree.
Both live nodes were observed on v8.0.41 with v8.0.42 candidates before this run.
This scheduled operation prepares a replacement candidate, not an installation.

| Upstream commits / feature | Existing provider and consumer chain | Implementation / frontend | Verification and conclusion |
| --- | --- | --- | --- |
| `32dd2c86`, `21553509`: persisted snapshot and re-login recovery | Existing NAS/Tokyo OAuth -> management persistence -> watcher -> auth selector -> all enabled consumers | `internal/watcher/{dispatcher,clients,watcher}.go`, `sdk/cliproxy/auth/conductor_{lifecycle,refresh}.go`; existing login/status UI | Preserve file binding and persisted revision during scans, plus the local source-digest mutation fence. New credentials clear stale refresh failures without clearing real quota cooldown. Upstream snapshot and re-login regressions, full tests and repeated race gate; no live login or token rotation. |
| `6340fe43`, `3e7a21e2`: duplex model/principal binding | Codex WebSocket clients -> interceptors -> per-turn authorization -> selected provider | `codex_websockets_duplex.go`, `handlers_interceptors.go`, `openai_responses_websocket.go`; no new UI | Preserve requested model authorization after routing rewrites, copy request context and enforce the bound principal. Upstream requested-model/principal tests and repeated race gate. Existing local HTTP/SSE fallback remains unchanged; not proof of live WebSocket availability for every provider. |
| `0fe4cc2c`, `90fe5b23`: terminal stream errors and cancellation | Shared Chat/Responses streams -> local client, workbench, novel text, study assistant/explanation/grading and other policy-controlled consumers | `openai_responses_websocket_forward.go`, `handlers_stream.go`; existing error/retry UI | Buffered terminal errors survive data-channel closure; canceled streams remain failures. Upstream channel-order and cancellation fixtures; no fabricated completion, silent retry or usage rewrite. |
| `b2fda98e`, `bdde391d`: Gemini schema and finish reasons | Existing Gemini/Antigravity tools and Gemini-compatible consumers -> schema/response translation | `internal/util/gemini_schema.go`, Codex/OpenAI-to-Gemini response translators; no new parameter | Resolve refs/allOf before inserting array placeholders; preserve finishReason. Upstream schema/stream fixtures; existing context, reasoning and independent speed contracts unchanged. |
| `e48d780f`: caller-scoped Claude MCP replay | Existing Claude OAuth and operator-configured plugins -> host execution -> tool aliases | `pluginhost`, `sdk/pluginapi`, `claude_executor_request.go`; no UI | Opaque caller scope survives plugin replay without revealing downstream API keys; alias identity stays stable across credential refresh. Caller-isolation and replay tests plus race gate; no new plugin enabled. |
| `0c61b12c`: xAI historical tool deduplication | Existing Grok -> Responses additional_tools -> xAI request and tool-result mapping | `xai_executor_{request,response}.go`; all current text-agent consumers, no new UI | Current top-level declarations win over historical duplicates; namespace counts and dispatch mappings use unique tools. Upstream duplicate/namespace/tool-result tests. Hosted-search usage and native-search quota attribution are not changed. |
| `27f3abfb`: session-scoped OAuth proxy | Optional operator-selected per-account network route, not used by existing managed login flows | v8 POST `/oauth/auth-url`, `oauth_sessions.go`, provider OAuth/callback handlers; existing manager still omits proxy overrides | Upstream validation, session isolation, persistence and callback fixtures cover this optional backend capability. No new route field is exposed: existing NAS route and Tokyo-local credential/network boundaries are intentionally preserved. Not a claim of a configured per-account proxy or newly available user-facing control. |
| `ce433b37`, merge `cf4fc4e7`: Home subscription backoff | Optional Home control plane, not configured by this maintenance | `service_home.go`, `service_home_retry.go`; no UI | Keep owned-socket cancellation and add bounded jittered reconnect backoff. Upstream deterministic retry tests; not applicable to deployed Home infrastructure because none is enabled here. |
| `ba06ec58`: Vertex default global location | Optional Vertex credential/import path | `vertex_import.go`, Gemini/Vertex executor and helpers; no UI | Retain default-location fixtures; no Vertex supplier/account is newly enabled or region migrated. |
| `3dae075d`: Devin lightning aggregation | Optional Devin catalog and UID selector | `devin_models.go`, `helps/devin_models.go`, standalone Devin catalog; no consumer allowlist expansion | Upstream aggregate-ID/routing tests retained. No new paid model or model preference registered. Shared pinned catalog remains byte-identical; a registry name is not entitlement evidence. |
| `30780351`: AI Studio multi-frame relay | AI Studio WebSocket relay -> Gemini-compatible stream translation | `aistudio_executor.go`, `aistudio_multiframe_test.go`; no new UI | Every frame in a relay chunk passes model/usage/error translation; raw JSON and keepalives remain distinct. Synthetic multi-frame fixtures only, no browser-driver restart or new provider activation. |
| `2bb6ac6f`, `84e83441`: lint and deterministic tests | Plugin/auth cancellation cleanup and executor WebSocket fixtures | Existing implementation/test files; no UI | Retain cancellation cleanup and race-safe fixture synchronization; full Go test/build and repeated race gate without weakening assertions. |

The merge has no textual conflicts. Repeated race testing exposed a separate
real lifetime race: asynchronous stream-result accounting read caller headers
from a Gin context already returned to its pool (`TestIssue6332ReasoningReplayTwoTurns`).
The candidate snapshots only Codex caller classification before spawning that
accounting goroutine. It preserves explicit-header precedence, SIWC selection,
parent cancellation and nil-context behavior; it does not rewrite tokens or
delay handler completion. A deterministic concurrent Gin-reuse regression and
the unchanged two-turn reasoning replay fixture cover the repair. The original
failed race log is retained, not hidden by a test-only wait or skip.

Review confirms that local source-digest
checks, final payload rules, error bridge modules, installer, independent search,
Tokyo 128 MiB limit, NAS-HK-Tokyo relay and pinned shared catalog remain intact.
There is no local client/configuration refresh because those files are unchanged.
GPT 500k, other-model capability limits, 80 percent compaction, Fast/Ultrafast
entitlements, actual response tier semantics, histories, provider IDs, consumer
policies and independent image/video/speech workloads are unchanged.

Required gates: Go 1.26.10 full test/build, repeated watcher/auth/WebSocket/plugin
race tests, installed-engine loopback bridge tests, API/updater/source-identity
tests, material/static build, paired manifests and desktop/390x844 navigation.
Exact results belong in the external maintenance receipt. These synthetic tests
do not claim new paid model, long-context or all-consumer production acceptance.
Only the user may install the NAS/Tokyo proxy and paired manager candidates.

## 2026-10-27: official v8.0.44 maintenance candidate

Seventeen upstream commits after v8.0.43 are merged from the real stable tag.
The canonical monorepo and exact-tree compatibility mirror remain distinct
identities. Before preparation both nodes still ran v8.0.41; v8.0.43 artifacts
existed, with NAS online GitHub ancestry verification unavailable. Do not reuse
that older candidate's manifest for this source or bypass its installation gate.

| Upstream commit / feature | Existing provider and consumer chain | Implementation / UI | Validation boundary and disposition |
| --- | --- | --- | --- |
| `712e4970` cancellation before first chunk | Shared text streaming -> local client/workbench/novel/study and other policy-controlled consumers | `handlers_stream.go`; existing error UI | Upstream first-chunk cancellation fixtures, no false success or automatic replay. |
| `e5321f53` per-selection identity | Auth scheduler -> provider execution -> plugin and usage accounting | `conductor_selection`, `executor/types`, pluginhost/usage/redisqueue; no new UI | Shared selection-ID fixtures retain per-attempt attribution; not a new quota source or inferred entitlement. |
| `064e008b` HTTP/WebSocket wire compression | Codex upstream plus negotiated downstream responses for existing clients | `response_compression.go`, Codex executors, response decoding and config v8 mapping; no model-generation setting | Upstream negotiation, streaming flush, decode, fallback and final-payload fixtures. Optional Codex upstream request compression stays disabled unless configured. Local client request compression remains disabled; memory/decompression admission limits are unchanged. This is not context compaction. |
| `43f5386b` Claude reasoning usage | Tokyo/other Claude -> OpenAI Chat usage -> existing consumer usage panels | Claude/OpenAI response translator and usage helpers; no new UI | Token-mapping fixtures preserve actual thinking/output totals, without rewriting usage to suppress compaction. |
| `531721bf` Codex commentary replay | Codex/Claude history translation -> existing tool/agent continuations | Codex/Claude request/response and internal signature sanitization; no UI | Cross-format commentary, signature and replay fixtures; markers are internal, no historical file migration or added paid turn. |
| `058b803b` explicit Authorization CORS | Existing browser API clients -> proxy preflight | `server_middleware.go`; no frontend control | Preflight fixtures name Authorization and requested headers; allowed origins/credential policy are not widened. |
| `6fe654bf` plugin schema-v7 body opt-out | Existing configured plugins -> request interceptor bridge | `rpc_schema`, interceptor adapters; no new plugin UI | Version/opt-out and legacy-body fixtures; existing plugins retain compatible delivery unless they explicitly opt out. |
| `ede07caf`, `37bd18b0` Interactions framing/input | Existing Gemini/Antigravity protocol routes -> Interactions consumers | Interactions translators and structured-output helpers; no new frontend | Bare content-block list and terminal usage followed by DONE fixtures. No new provider or consumer is enabled. |
| `1d45a5f3` Claude 5.5 tool choice | Existing Claude 5.5 -> protocol translator -> Claude execution; shared text-agent consumers | `helps/claude_tool_choice.go`, executor and signature sanitizer; no new UI | Forced any/tool normalizes to auto for routed Claude 5.5 before thinking; final user payload rules still win. Delegated providers keep their contract. This is a compatibility downgrade, not a claim of forced-tool support. |
| `aa4130e6` truncated Gemini stream classification | Gemini/Antigravity accepted HTTP200 -> stream completion -> auth selector | Executor truncation marker and conductor cooldown/retry; existing failure UI | Missing-terminal fixtures verify explicit failure without credential cooldown or paid replay on another account. Real account failures are not fabricated for testing. |
| `8a982a52`, `b214385c`, `11e5f67b`, `999526c7`, merge `ec81e70a` LAN discovery | Optional LAN discovery, not enabled on scoped private NAS/Tokyo ingress by this maintenance | discovery spec/resolver/cache and docs; no new management discovery UI | Bounded split mDNS/TXT schema fixtures; deprecated management advertisement remains ignored. Preserve private listeners and existing network configuration; no network discovery activation. |
| `1f917e77` obsolete sponsorship | Documentation/assets only | Upstream README/assets | No model or runtime capability claim. |

Local compatibility repair: a new red regression demonstrated that default
negotiated compression encodes large JSON 429s for Codex streaming Responses,
which would bypass the installed bridge's bounded plaintext error formatter.
Preserve identity only for that exact method/path/accept/client signature and
429 JSON response when identity is allowed. Status, body and Retry-After remain
unchanged. Ordinary clients, other methods/routes/statuses, successful streams,
existing encodings and explicit identity rejection retain negotiated behavior.
No local bridge reload or compressed-error decoding is introduced.

The first full suite exposed a Home fixture timeout at the newly introduced
initial jitter boundary. The non-drain regression now continuously fails if
the blocking scope starts draining while waiting, and allows the existing
initial retry interval plus one interval of transport/ACK overhead. All registry,
ACK and service-cancellation assertions remain; no runtime backoff is altered.
Original failure logs and before/after compression regression results are kept
with the external maintenance receipt.

Repeated race-mode suites also exposed a terminal-event ordering bug: after the
client received response.completed, deferred HTTP-interrupt cleanup could still
accept an unknown late interrupt and fabricate response.incomplete. Retire local
interrupt delivery before publishing the terminal event and re-arm on the next
response.created; preserve upstream control forwarding and existing authorization.
The unchanged late-interrupt and active-interrupt integration tests are repeated
thirty times with race detection. Separately, the OAuth callback fixture now uses
a unique state ID per run because completed OAuth states intentionally remain
reserved briefly; retain the production TTL and all callback assertions.

Required gates: full Go 1.26.10 test/build, relevant repeated race suites,
installed-engine loopback tests, API/updater/identity tests and static material
build, precise NAS/Tokyo/manager manifests and desktop/390x844 verification.
Catalog, provider IDs, histories, GPT 500k/other-model limits, 80 percent context
compaction, Fast/Ultrafast entitlement, independent media parameters and Tokyo
profile/relay remain unchanged. Paid or full-window inference is not claimed.
Preparation is not installation; proxy/paired manager installation stays manual.

## 2026-10-27: native Responses compaction through the Tokyo compatibility channel

The failing Codex session sent a real compaction_trigger in a streaming Responses
request. The NAS compatibility channel defaulted to Chat Completions, which cannot
retain that trigger; the Tokyo backend consequently produced ordinary output (or
rejected an assistant prefill), not a compaction item. The existing per-provider
wire-api: responses setting preserves the trigger and sealed continuation.

A configuration-only trial exposed a second defect: native Responses passthrough
omitted SSE frame separators. With production bootstrap-retries=1 and pre-first-token
keepalives enabled, buffered control events concatenated into an invalid frame and
failed before the first payload. The executor now retains the double-newline frame
boundary only for Responses-to-Responses passthrough. Cross-protocol translators,
retry policy, cancellation, error handling, usage and model selection are unchanged.
A red regression demonstrated five events collapsing into one invalid frame before
the repair; it verifies independently parseable, unchanged compaction events after it.

Validation includes focused repeated race tests, the full unified preparation gate
and exact-image two-hop fake-upstream tests with production bootstrap settings:
hot reload, exactly one compaction item, thirty Codex-identity repetitions, capsule
replay, Chat JSON/stream compatibility, standalone compact and tool history. No UI
or new model entitlement is required. Small real compaction/continuation and original
session recovery are separate production gates, not implied by synthetic tests.

Both failed configuration-only live probes were rolled back to the original config;
diagnostic fixtures with bootstrap retries disabled are not acceptance evidence.
The user separately authorized a verified NAS rolling release and its paired manager,
then restoration of the specified original session. Do not update Tokyo, force-stop
requests, alter the relay/client/history, disable global bootstrap retries, fabricate
compaction output, or treat this one-time release authorization as a daily policy.
Exact source, image, backup and live results belong in the external repair receipt.
