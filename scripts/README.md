# NAS one-click release

`nas-release.sh` fast-forwards the local `yeutech-capability-v15` checkout to
its reviewed GitHub state, then publishes it to the NAS `novel-ai-proxy`
service. Upstream changes must first be merged and reviewed in
`huaye37/CLIProxyAPI-YEUTECH`.

## Preconditions

- The local checkout is clean.
- The checked-out branch is `yeutech-capability-v15`.
- The NAS SSH alias `yeutech-nas` is usable.
- The Mac has `git`, `tar`, legacy `scp`, `ssh`, and `textutil`.

## Run

```sh
./scripts/nas-release.sh
```

The command uploads source only. It never uploads the NAS `config`, `auth`, or
runtime logs. On NAS it saves the active compose file and image tag, builds a
commit-addressed image, recreates only `novel-ai-proxy`, and waits for its Docker
health check. If build, start, or health fails, it restores the saved compose
file and recreates the previous image.

The release backup is retained at
`/volume1/docker/novel-ai-proxy/backups/one-click-<timestamp>-<commit>/`.

## Updating from upstream

On GitHub, create a branch from `yeutech-capability-v15`, merge the desired
`router-for-me/CLIProxyAPI` update, resolve conflicts, and run the applicable
tests. Merge that pull request into `yeutech-capability-v15`. Then run the
one-click release command above. It fetches and fast-forwards the local branch
automatically before building and switching NAS; it does not point NAS at
upstream `latest`.
