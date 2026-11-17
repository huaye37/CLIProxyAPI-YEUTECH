# Codex standalone Images entry repair (2026-11-17)

Codex's standalone `image_gen` extension uses the active provider's
`images/generations` and `images/edits` endpoints, not `/responses`.
The local OpenAI base URL points to port 18401; its authentication bridge
forwards these paths to `llm-api.yeutech.cn`. The NAS nginx allowlist previously
excluded both endpoints, producing an HTML nginx 404 before the proxy handler.
The native fallback script's provider-only override still inherited this base URL.

Both nginx templates now admit exactly `/v1/images/generations` and
`/v1/images/edits`. Management, arbitrary Images paths, existing upstream port,
request limits, buffering, authentication, and timeouts remain unchanged.
The current proxy already implements both endpoints and advertises `gpt-image-2`.
No proxy binary, manager image, controller, model choice, or credential changed.

## Deployment

`enable-image-routes.py` is a narrow host configuration migration, not a proxy
upgrader. With the shared maintenance lock held, independently read the current
NAS config SHA256, then run the script on NAS as root with
`--expected-sha256 <observed hash>`. Without `--apply` it only prints the planned
hashes. With `--apply`, it preserves a private backup, atomically changes only
the known allowlist, validates the full nginx configuration, and gracefully
reloads it. Validation/reload failure restores the original configuration.
Never upload an old whole nginx template over a newer live configuration.

The 2026-11-17 deployment changed SHA256
`4e21548fd64a006d492723ce1101da75b565fc4c1638e7c196cfd66d90e26492` to
`b84958a030140d63651d9988e9ea9ad0d3ba5939270013bbef0b7dc0ded91004`.
Rollback copy:
`/volume1/docker/yeutech-api-manager/data/node-channel-backups/image-routes-fylmr513/yeutech-llm-api.conf`.
Restore only after confirming the current hash still matches this deployment;
validate nginx and gracefully reload. Do not overwrite subsequent changes.

## Verification and maintenance boundary

- Four targeted tests cover exact migration, idempotence, rejected unknown
  layouts, existing routes, new image routes, and blocked management paths.
- Both actual local bridge POSTs with `gpt-image-2` and an empty prompt now
  return JSON 400 `prompt is required`, instead of nginx HTML 404. Empty prompts
  stop before executor dispatch; no inference or image generation was invoked.
- Both unauthenticated domain POSTs return 401. Arbitrary image and management
  paths remain 404; `/v1/models` returns 200 with 58 entries.
- All three proxy slots, gateway, manager, and Agent retain identical image IDs,
  start timestamps, and restart counts across the reload.
- This proves entry routing and validation, not paid end-to-end image generation
  or account entitlement. No image was generated during this repair.

Future maintenance must retain these allowlist entries. This commit is a host
route repair, not a rebuilt/staged proxy candidate; the existing binary release
identity and manager candidate are unchanged. Integrate this change when
preparing a future full candidate rather than relabeling existing images.
