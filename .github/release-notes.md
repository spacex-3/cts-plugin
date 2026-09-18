Fix Codex probe requests that were rejected with `Unsupported parameter: max_output_tokens`.

## Fixed in v0.2.1

- Stop sending `max_output_tokens` to the native Codex Responses endpoint.
- Match CPA's Codex request normalization by including `parallel_tool_calls` and `reasoning.encrypted_content`.
- Keep `max_output_tokens` as an ignored compatibility setting so existing configurations continue to load.
- Continue to support provider-style SOCKS5 syntax, direct baselines, and per-attempt state logs from v0.2.0.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Then merge `config.example.yaml` into the existing CPA configuration.
