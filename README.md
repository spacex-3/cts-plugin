# Codex Turn State Probe

A native CLIProxyAPI plugin that discovers `X-Codex-Turn-State` through rotating-proxy probes and normal Codex traffic, caches it by the exact selected auth ID and upstream model, and injects a fresh matching value into later requests.

## Install from the CPA plugin store

Requires a plugin-enabled CLIProxyAPI version compatible with the v7.3.6 plugin ABI. Add this source to the existing `plugins` section in `config.yaml`:

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Refresh the CPA plugin store, search for **Codex Turn State Probe**, and install it. The store selects the archive for the current operating system and architecture and verifies it with `checksums.txt`.

Then add the plugin configuration shown in [`config.example.yaml`](config.example.yaml). Do not duplicate the top-level `plugins` key.

## What it does

- Periodically sends a minimal streaming Codex request through a rotating proxy.
- Uses a dedicated uTLS HTTP/2 connection for every probe attempt.
- Stops reading and closes the connection immediately after finding turn-state metadata.
- Accepts only states matching `target_state_length` (default: `292`).
- Harvests matching state from regular HTTP, SSE, and WebSocket Codex responses.
- Caches state in memory by exact runtime auth ID plus resolved model.
- Replaces `X-Codex-Turn-State` on later matching requests while the state is within `ttl_seconds` (default: one hour).
- Never shares state across accounts or models.

The state cache is process-local. Restarting CPA or reloading the plugin clears it.

## Configuration

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codex-turn-state:
      enabled: true
      priority: 100
      proxy: "proxy.example:8080:username:password"
      auth_ids:
        - "codex-auth-id-1"
      models:
        - "gpt-5.6-sol"
        - "gpt-6-astra"
      interval_seconds: 300
      target_state_length: 292
      ttl_seconds: 3600
      inject: true
      harvest: true
      probe: true
      max_probe_attempts: 3
      max_output_tokens: 16
      prompt: "."
```

### Fields

- `proxy`: rotating proxy endpoint. Supported forms are `host:port:user:password` and standard `http://`, `https://`, or `socks5://` URLs. Bracket IPv6 literals.
- `auth_ids`: exact Codex runtime auth IDs. Empty permits every Codex auth visible to the host.
- `models`: exact upstream model IDs. Defaults to `gpt-5.6-sol` and `gpt-6-astra`.
- `interval_seconds`: delay after one full probe cycle finishes. Default: `300`.
- `target_state_length`: required state length. Default: `292`.
- `ttl_seconds`: maximum cache age for injection. Default: `3600`.
- `inject`: inject fresh cached state into matching requests. Default: `true`.
- `harvest`: collect matching state from normal Codex traffic. Default: `true`.
- `probe`: run background probes. Default: `true`.
- `max_probe_attempts`: attempts per auth/model in one cycle. Default: `3`.
- `max_output_tokens`: probe output limit. Default: `16`.
- `prompt`: minimal probe input. Default: `.`.

When `probe` is enabled, `proxy` must be configured. A wrong-length state consumes an attempt and is not cached.

## Matching behavior

Injection occurs only when all of these match:

1. CPA selected the exact same runtime Codex auth ID.
2. The resolved upstream model is the exact configured model.
3. The cached state still satisfies the configured length and TTL.
4. `inject` is enabled.

An existing header with the same name is replaced for that execution attempt. Round-robin selection of another account does not receive the cached state.

## Status and manual probe

The plugin registers:

```text
/v0/resource/plugins/codex-turn-state/status
```

- `GET .../status`: redacted HTML status.
- `GET .../status?format=json`: JSON status.
- `POST .../status` or `GET .../status?op=probe`: queue an immediate probe cycle.

The status output does not expose proxy credentials, access tokens, or state values.

## Build locally

```bash
go mod download
gofmt -w .
go test -race ./...
go vet ./...
mkdir -p dist
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/codex-turn-state.dylib .
```

Use `.dylib` on macOS, `.so` on Linux, or `.dll` on Windows. Release tags build and publish all supported store archives automatically.

## Operational notes

- Direct probing requires a file-backed Codex credential exposed through the CPA host API. Runtime-only credentials can still participate in harvesting/injection but cannot be probed directly.
- The plugin intentionally does not set post-connection network timeouts.
- Closing a probe stream early limits response consumption, but provider accounting remains controlled by the upstream service.
- Avoid aggressive intervals and comply with provider and proxy-service terms.
- A state of the expected length is still opaque upstream data; length does not guarantee validity or any routing/capacity outcome.

This standalone repository is derived from the CLIProxyAPI `examples/plugin/codex-turn-state` implementation and retains the MIT license.
