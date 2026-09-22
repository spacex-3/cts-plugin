Probes now go out cold by default. A probe is how a fresh ticket is minted, and it must not recycle a routing cookie captured somewhere else.

## Changed in v0.6.2

- **`probe_send_cookies` now defaults to `false`.** The cookie jar is keyed by account while probes rotate egresses, so a probe that carried a cookie captured on exit A while connecting from exit B asked the upstream for a ticket for a route the connection was not on — the shape that answers 312. A cold probe cannot contradict itself, and its response still hands back the fresh ticket *and* its cookie pair, which is captured as before whenever `harvest_cookies` is on. Set `probe_send_cookies: true` only to make probes mirror production traffic exactly.

## Added in v0.6.2

- **Status page chip `探测凭据`**: `冷启动（不带 Cookie）` or `携带 Cookie（热启动）` — the first thing to look at when every proxied probe answers 312.
- **Every probe log line records `cookies_sent`** (captured ticket, rejected ticket, direct and proxied failures), so the log states whether that attempt actually carried cookies instead of leaving it to inference. `grep codex-turn-state <CPA>/logs/main.log` is enough.

## Unchanged

Everything from 0.6.1 still applies: cookies are captured and injected with the state, the injection table shows the `Cookie 请求头` column, and the upstream only receives cookies once each Codex auth file declares `"headers": { "Cookie": "$Cookie" }` — the status page banners the accounts that still lack it.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

CPA hot-reloads plugins when they are installed from the store; check the log for `plugin hot reloaded` and the new version number. Restart CPA only if the reload did not happen.
