Automatically probe and harvest `X-Codex-Turn-State`, cache it by exact Codex auth ID and model, and inject fresh matching state into later CPA requests.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Then merge `config.example.yaml` into the existing CPA configuration and set the rotating proxy, auth IDs, and models.

## Safety and scope

- State never crosses auth IDs or models.
- Default accepted length is 292 and default TTL is one hour.
- Proxy credentials, tokens, and state values are not rendered in status output.
- Cache is process-local and is cleared on restart or plugin reload.
- No real upstream credentials are exercised by CI.
