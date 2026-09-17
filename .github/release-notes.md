Add direct baseline comparison, complete per-attempt probe logs, and provider-style SOCKS5 proxy syntax support.

## New in v0.2.0

- Accept `socks5://host:port:user:password` in addition to standard proxy URLs.
- `direct_probe: true` records one no-proxy baseline for every auth/model before proxy attempts.
- Every proxy attempt records timestamp, route, attempt number, state length, target match, cache result, and error.
- `show_state_values: true` displays future full state values in the status page and JSON.
- `probe_log_limit` bounds in-memory logs (default 200, maximum 1000).

Direct baseline states are never inserted into the production injection cache. Proxy credentials and access tokens remain redacted.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Then merge `config.example.yaml` into the existing CPA configuration.
