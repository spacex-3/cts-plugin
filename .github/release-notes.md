Keep the admission filter conservative: 312-character states stay out unless you opt in.

## Changed in v0.5.4

- `accepted_blocks` goes back to `[10, 12]` (≈292 / 332) as the default. v0.5.2 widened it to include `11` (≈312) on the strength of a third-party report that the gpt-5.6 / gpt-6 era returns 312; that claim contradicts both field observation (direct exits return 292, proxied exits return 312) and another implementation that targets these same models at 292. Since a 312 ticket could displace a known-good 292 one, the filter now rejects 312 by default and the operator opts in with `accepted_blocks: [10, 11, 12]`.
- The status page chip now renders the accepted shapes as `10≈292 / 12≈332` instead of bare block numbers, and the rejection message names the shape it saw, so the choice is visible rather than implicit.

## Fixed in v0.5.3

- Repeated proxy entries are no longer deduplicated. A rotating residential pool returns a new exit IP per connection, so listing the same endpoint three times means three egress slots; the plugin used to collapse them into one. Listing it once and raising `attempts_per_route` behaves the same way.
- `README.md`, `README_CN.md`, and `config.example.yaml` now show all three accepted shapes for the proxy fields (a multi-line JSON array, a YAML list, and one entry per line in the legacy `proxy` string) instead of only the JSON-array form.

## Fixed in v0.5.2

- Probing no longer requires a proxy pool: with `direct_probe: true` and no proxies, the round now runs direct-only. Before this, an empty pool skipped probing entirely with `at least one proxy is required for probing`.
- One unparsable proxy line no longer aborts the whole round. Valid entries keep working, skipped entries are logged with their line number, and the round is only skipped when nothing is usable.
- Pasting a JSON array (the shape the config editor uses for `proxies`) into the legacy `proxy` string field now works instead of failing with `proxy must be [host]:port:user:password`.
- Proxy entries without credentials are accepted: `host:port` and `host:port:user` are valid next to `host:port:user:password`, and passwords may contain colons.
- `accepted_blocks` now defaults to `[10, 11, 12]`. `11` blocks ≈ 312 characters is the current full-strength shape for the gpt-5.6 / gpt-6 era, so the previous `[10, 12]` default rejected every probe against those models. Set `[10, 12]` to restore the stricter behaviour.

## Added in v0.5.2

- The status page explains plugin-raised failures instead of the generic "Operation failed; inspect local plugin logs for details": missing/unusable proxy configuration, unreachable egress, upstream HTTP status, and rejected state shapes (with the received length and block count) each get their own message.
- Every failed probe attempt is written to the log with `route`, `proxy_index`, `attempt`, and the error, with proxy credentials redacted, so a failing egress can be identified without guessing.
- The status page shows the accepted block counts as its own chip (also `accepted_blocks` in the JSON view).

## Fixed in v0.5.1

- `attempts_per_route` is now declared in the plugin registration metadata, so it appears in the CPA plugin configuration editor instead of only in `config.yaml`.
- The status page shows the per-route attempt budget as its own chip.
- The `direct_probe` description now matches the actual behaviour: an accepted direct state is cached and ends the round rather than only being logged.

## Added in v0.5.0

- The status page and JSON now count, per account+model, requests that carried a cached ticket (`injections`), requests that passed every gate but left with no state (`bare_requests`), and whether a harvested response handed back the ticket already held (`ticket_echoes`) or a different one (`ticket_changes`). A non-zero `bare_requests` is the visible signal that injection is silently failing open, and the echo/change split shows whether the upstream returns the ticket it was given or reissues one per turn.
- The counters are persisted in `runtime.json` next to cached states, probe logs, and injection records, so a reload keeps the history.
- `attempts_per_route` (default `1`, maximum `10`): retry the direct route or a single proxy before the probe moves on to the next egress, inside the `max_probe_attempts` budget. Raise it when one attempt per egress keeps returning a rejected state length. The default keeps the previous rotation.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Then merge `config.example.yaml` into the existing CPA configuration.
