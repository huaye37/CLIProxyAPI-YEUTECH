# NAS update controller, 2026-09-25

This directory is the versioned source for the standalone NAS update controller.
The platform working copy at `../yeutech-api-platform-redesign-20260914/updater`
is synchronized from these files. `ui-lines.json` narrowly patches update-related
lines in the actual production manager asset without replacing unrelated UI work.

## Boundaries

- Checking an upstream release does **not** merge it. GitHub comparison reports
  whether the maintained branch contains the official tag. Installation fetches
  the official tag again and checks ancestry against the exact cloned commit.
- Source integration runs separately from installation. The Codex heartbeat
  named `CLIProxyAPI 上游同步` checks stable releases daily, merges in an isolated
  checkout, runs the full Go suite and build, then updates the maintained branch
  only after success. Conflicts or failed checks remain reviewable and are not
  deployed. The heartbeat does not switch any running proxy instance.
- The GitHub OAuth credential lacks `workflow` scope, so `.github/workflows`
  remains unchanged by the source sync. Repository-hosted scheduled workflow
  changes require a separate GitHub authorization; they are not silently
  substituted for the Codex heartbeat.
- Deployment refuses inconsistent active-lane configurations and slots that are
  active or have requests on any registered gateway. Counts from old gateways
  may over-report activity; they must not be cleared just to free a slot.
- Only new requests follow replacement pointers. Old backends remain running,
  including on a partial-switch rollback. Long-lived sockets can remain on an
  old version. Installation completion does not mean those sockets have drained.
- The service launcher discovers the exact controller process instead of trusting
  a stale PID file. The process takes an exclusive lock and binds its port before
  changing persisted job state. A duplicate launch cannot poison the active job.
- `release_control_only.py` publishes only the controller and manager UI. It never
  calls `/update` or `/rollback`, and checks inference container identities and
  start times before and after. The original interrupted job is not resumed.

## Verification

Run `python3 -m unittest discover -s deploy/update-controller -v` and
`sh -n deploy/update-controller/service.sh`.

Nine unit tests passed. Three platform appearance-contract tests passed. A local
real Chrome preview at `http://127.0.0.1:28320` exercised blocked installation and
the eligible-update confirmation at desktop and 390x844 sizes. Update API data
was mocked; no install request was submitted, and no page JavaScript errors
were observed. Playwright used installed Chrome because its bundled browser was
absent. The Browser plugin was unavailable.

Production controller and manager release: `20260925-update-control-v2`.
The initial manager asset probe omitted the required Host header, returned 403,
and rolled back the manager only. The corrected probe used `api.yeutech.cn`; the
served asset matched the built asset byte-for-byte. All inference container IDs,
start times, and running states matched the pre-release snapshot afterward.
Real production browser interaction and an actual proxy upgrade remain untested;
the user explicitly retained control of the upgrade action. No Go sources changed;
Go compilation was unavailable because this machine has no `go` executable.

Recovery files are on the NAS under
`/volume1/docker/yeutech-api-manager/backups/20260925-update-control-v2`.
Do not restore the old broken service launcher or restart inference containers
as part of a UI/controller rollback.

## Source-merge status (2026-09-25)

The update page now shows a separate source-merge result after `检查更新`:
verified merged, verified not merged, or unverified. A merged result requires
the controller's GitHub ancestry check for the latest official release, a
check timestamp, and the exact maintained-branch commit; it does not imply
that the running proxy has been deployed. Installation readiness is shown
separately. The production manager/controller release is
`20260925-merge-status-v4`; it did not invoke proxy update or rollback, and
the inference container identities and start times were unchanged. The
production `/check` response verified `v7.3.17` at commit `3d054dc6`, while
deployment remains blocked by divergent inference-entry configurations.

The first online browser reload still executed the previous `app.js` because
the public static route allowed a 24-hour browser cache and the HTML kept the
old asset query. `20260925-merge-status-cache-v5` changed the manager's JS/CSS
responses to `Cache-Control: no-cache` and bumped both query versions. In the
authenticated Chrome page, clicking `检查更新` then showed `已合并 v7.3.17`,
commit `3a4b683099b4`, a fresh check time, and a disabled deploy button with
the configuration-divergence reason. The manager-only v5 release left every
inference container identity and start time unchanged.

## Active configuration unification (2026-09-25)

The two gateway pointers previously used `blue` and `amber` with different
images and configurations. The differences were limited to the four ChatGPT Web
compatibility routes and `codex.orphan-delegation-compatibility` in `amber`.
The initial config-only preflight stopped without switching because the images
also differed. `20260925-config-unification-v9` then safely reused the already
running `amber` image and configuration in the idle `green` slot and moved both
gateway pointers to it. All four gateway processes report port 18318; the new
backend returns 41 model capabilities, including the three ChatGPT Web models.
The original `blue` and `amber` containers were not stopped or restarted, so
their established requests could finish. A pointer/config backup is at
`/volume1/docker/novel-ai-proxy/update-backups/20260925-config-unification-v9`.
The upstream v7.3.17 source is integrated and installation is ready, but this
operation did **not** deploy that new proxy version; the user retains the web
upgrade action. The controller still records a failed *previous* update job,
which is not the result of this unification.
