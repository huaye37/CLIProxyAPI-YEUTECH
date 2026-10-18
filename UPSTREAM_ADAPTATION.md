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

Official behavior checked against xAI `developers/tools/tool-usage-details` and
the installed Codex engine plus OpenAI's core/session/context_window.rs and
core/context_manager/history.rs. Live end-to-end and activation evidence must
be recorded separately from these deterministic tests; do not resume the user's
interrupted thread as a test. See deploy/MAINTENANCE_CN.md for rollout boundaries.
