Report what turn-state injection actually does per account, and let one probe egress retry before the rotation moves on.

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
