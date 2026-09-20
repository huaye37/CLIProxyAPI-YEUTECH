# Cross-task delegation runtime correction

## Cause and scope

The failed destination task carried a `function_call_output` with namespace
`codex_app`, name `send_message_to_thread`, and no `call_id`. The deployed amber
binary already supported this shape, but `codex.orphan-delegation-compatibility`
was absent from its configuration and false in the live management snapshot.

An isolated Responses request with user/assistant history followed by precisely
this delegation shape reproduced HTTP 400, `Requests ending with a model turn
are not supported.` No course instructions or user task were replayed.

## Deployed correction

Enabled `codex.orphan-delegation-compatibility: true` in
`/volume1/docker/novel-ai-proxy/config-amber/config.yaml`.
Backup: `config.before-delegation-1789915257278509606.yaml` in that directory.
An atomic file replacement alone did not reload the running process. Used the
private management config API followed by an unchanged debug-setting save to
invoke the explicit runtime reload hook. Debug remained false.

The process logged `codex.orphan-delegation-compatibility: false -> true` at
2026-09-20 22:44:04 (NAS local time). The amber container's start time remained
`2026-09-20T01:50:51.51824795Z`. No proxy/container restart or lane switch occurred.
The updater copies the active slot config when preparing its next slot, retaining
this setting during normal subsequent updates. A rollback to an older config
must explicitly retain the compatibility setting.

## Acceptance

- Before reload: identical isolated probe returned HTTP 400 and the model-turn error.
- After reload, gateway port 18310: HTTP 200, `response.completed`, exact delegated
  marker received, no stream error; 7.29 seconds.
- HTTPS `https://llm-api.yeutech.cn`: same acceptance criteria passed; 2.25 seconds.
- These probes used Gemini 3.8 Flash High, no tools, and no business-side effects.
- Existing course tasks were not resumed or edited. Full course-task/tool execution
  is not claimed as acceptance of this change.

## Runbook

`scripts/nas-delegation-compat.py` runs on the NAS, reads credentials there without
printing them, and defaults to read-only configuration inspection.
`--probe` executes an isolated delegation-shaped request. `--apply` backs up and
enables an absent Codex block, then explicitly reloads the runtime. `--reload`
activates an already written setting. An existing Codex block needing edits is
rejected for manual review. `--url` can select the HTTPS inference entry.

No Go code was modified; no Go build or unit-test success is claimed.
