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

Production controller and manager release: `20260925-update-control-v1`.
The initial manager asset probe omitted the required Host header, returned 403,
and rolled back the manager only. The corrected probe used `api.yeutech.cn`; the
served asset matched the built asset byte-for-byte. All inference container IDs,
start times, and running states matched the pre-release snapshot afterward.
Real production browser interaction and an actual proxy upgrade remain untested;
the user explicitly retained control of the upgrade action. No Go sources changed;
Go compilation was unavailable because this machine has no `go` executable.

Recovery files are on the NAS under
`/volume1/docker/yeutech-api-manager/backups/20260925-update-control-v1`.
Do not restore the old broken service launcher or restart inference containers
as part of a UI/controller rollback.
