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
