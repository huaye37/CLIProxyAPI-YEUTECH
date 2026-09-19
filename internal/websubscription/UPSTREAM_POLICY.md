# Web Subscription Upstream Policy

The web-subscription integration is an independent CLIProxyAPI extension. It does not vendor or fork a reference bridge into the proxy core.

## Compatibility boundaries

1. `internal/websubscription` owns protocol-neutral turns, tool validation, driver contracts, capability probes, and fault isolation. It must not import browser automation libraries or a reference bridge package.
2. CLIProxyAPI integration should use the public plugin API (`ModelProvider`, `ProviderExecutor`, and translators) rather than adding web-specific branches to existing provider executors.
3. Gemini Web and ChatGPT Web implementations are replaceable drivers. A driver may be native or may communicate with a pinned sidecar process, but it must implement the same session and capability contracts.
4. A failed or signed-out web driver reports its own models as unavailable. It must not affect Codex, Antigravity, API-key, or other plugin routes.

## Updating CLIProxyAPI

- Fetch and merge the official `router-for-me/CLIProxyAPI` upstream normally.
- Keep YEUTECH integration commits limited to new packages, plugin wiring, configuration, and capability projection whenever possible.
- Run the web-subscription contract tests plus the upstream translator and server build checks after every merge.
- Resolve plugin API changes in a narrow adapter layer; do not spread plugin ABI types through the driver core.

## Updating reference projects

Reference projects are inputs to compatibility review, not Git parents of this repository. Track each adopted behavior with its project URL, pinned revision or release, license, and the local contract tests that cover it.

For each reference update:

1. Review its protocol, selectors, session lifecycle, anti-bot flow, and license changes.
2. Port only the required behavior into the relevant driver or update the pinned sidecar version.
3. Run captured-response tests without credentials.
4. Run an explicit real-account acceptance test before marking the updated driver selectable.

This keeps official CLIProxyAPI merges and reference-driver updates independent and reversible.
