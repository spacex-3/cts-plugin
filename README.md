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

- Optionally records a direct, no-proxy baseline and then sends minimal streaming Codex requests through a rotating proxy.
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
      proxy_scheme: "http"
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
      direct_probe: true
      show_state_values: false
      show_account_details: false
      show_injection_headers: false
      probe_log_limit: 200
      max_probe_attempts: 3
      failure_reprobe_threshold: 3
      prompt: "."
```

### Fields

- `proxy`: one or more rotating proxy endpoints, one per line. Supports `host:port:user:password`, provider-style `socks5://host:port:user:password`, and standard `socks5://user:password@host:port` / HTTP(S) URLs. Credentials are URL-encoded internally; bracket IPv6 literals. A nonmatching state advances to the next proxy on the following attempt.
- `proxies`: recommended list form for multiple proxies, one item per entry. It is merged with `proxy`; use this in the CPA plugin config UI when the `proxy` string field collapses pasted newlines.
- `proxy_scheme`: default protocol for `proxy`/`proxies` entries that omit a scheme. Choose `http` or `socks5`; the latter is required for SOCKS5-only providers such as BestGo. Default: `http`.
- `auth_ids`: exact Codex runtime auth IDs. Empty permits every Codex auth visible to the host.
- `probe_auth_ids`: auth IDs that may be probed. Empty probes every auth in the `auth_ids` scope; the status page also supports per-account selection.
- `models`: exact upstream model IDs. Defaults to `gpt-5.6-sol` and `gpt-6-astra`.
- `interval_seconds`: delay after one full probe cycle finishes. Default: `1800`.
- `probe_schedule`: `fixed` probes on a constant interval; `state_aware` skips periodic probes while a fresh state is cached and only resumes shortly before expiry. Default: `fixed`.
- `probe_lead_seconds`: lead time before state expiry used by `state_aware`. Default: `300`.
- `target_state_length`: required state length. Default: `292`.
- `ttl_seconds`: maximum cache age for injection. Default: `3600`.
- `inject`: inject fresh cached state into matching requests. Default: `true`.
- `harvest`: collect matching state from normal Codex traffic. Default: `true`.
- `probe`: run background probes. Default: `true`.
- `direct_probe`: send one no-proxy baseline request before proxy attempts for each auth/model. The baseline is logged but never cached. Default: `false`.
- `show_state_values`: display and retain future full state values in the status page/JSON probe log. Default: `false`; enable only on a protected management endpoint.
- `probe_log_limit`: maximum in-memory attempt records. Default: `200`, maximum: `1000`.
- `max_probe_attempts`: attempts per auth/model in one cycle. Default: `3`.
- `failure_reprobe_threshold`: consecutive production request failures inside the current state window that trigger a targeted reprobe. Default: `3`; a negative value disables this behavior.
- `max_output_tokens`: deprecated compatibility field. It is ignored because Codex upstream rejects token-limit parameters.
- `prompt`: minimal probe input. Default: `.`.

When `probe` is enabled, `proxy` must contain at least one endpoint. A wrong-length state consumes an attempt and is not cached.

## Matching behavior

Injection occurs only when all of these match:

1. CPA selected the exact same runtime Codex auth ID.
2. The resolved upstream model is the exact configured model.
3. A cached state exists (expired fallback remains the default; `use_issued_at` enforces token age).
4. `inject` is enabled.

An existing header with the same name is replaced for that execution attempt. Round-robin selection of another account does not receive the cached state.

## Status and manual probe

The public menu URL `/v0/resource/plugins/codex-turn-state/status` now serves
only a static login shell. It contains no runtime data and rejects all query
operations, including the former public `?format=json` URL. Enter the CPA
management key to load the existing status page. The key stays in page memory,
is sent as an `Authorization: Bearer ...` header, and is cleared on logout/reload.
CPA's existing remote-management policy still applies.

Data and operations are registered without a menu under the **authenticated**
`/v0/management/plugins/codex-turn-state/status` route:

- `GET .../status`: protected HTML status.
- `GET .../status?format=json`: protected JSON status.
- `GET .../status?op=fragment_probe_logs` / `fragment_injections`: protected fragments.
- `POST .../status?op=probe` / `probe_target`: queue probes.
- `POST .../status?op=save_manual_state` / `save_proxies` / `save_probe_accounts`:
  save URL-encoded form bodies. GET mutations return 405.

All status responses use `Cache-Control: no-store`. Default JSON/HTML omit state
values, email/name/label and raw injection headers. Account IDs (which may contain
email addresses) are replaced by stable SHA-256 aliases; the UI resolves them for
account selection and operations. `show_account_details: true` opts into real
identities. `show_injection_headers: true` opts into request headers; state still
requires `show_state_values: true`, and credential headers remain redacted.
Arbitrary upstream error bodies are not returned over HTTP.

The security boundary is CPA's management middleware: the plugin cannot read or
verify the CPA key itself. Do not expose an alternate unauthenticated route to the
management handler. HTTPS is required to protect the key in transit. Authorized
administrators can still see proxy topology and explicitly enabled diagnostic
data. Same-origin scripts and local runtime files remain trusted; account aliases
are pseudonyms, not a cryptographic anonymity guarantee. Runtime persistence still
contains usable state with the existing owner-only file permissions. This change
does not encrypt local storage.

The page shows account cards at the top with a stable per-account color, current state length, live countdown, and requests/successes/total tokens/average TTFT for the current state window, followed by recent probe results and every proxy attempt. Proxy credentials and access tokens are never displayed. Full state values are displayed only when `show_state_values: true`; existing records captured while it was disabled remain hidden.

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

### Optional request-driven lifecycle

Existing `fixed` scheduling and legacy acceptance remain the defaults. To enable
all lifecycle improvements, set these options in the plugin configuration:

```yaml
probe_schedule: on_demand
probe_wait_milliseconds: 1500
probe_timeout_seconds: 60
use_issued_at: true
require_completed: true
error_aware_backoff: true
quota_backoff_seconds: 900
rotate_proxy_start: true
```

`on_demand` has no startup probe or periodic timer. An eligible business request
probes only its selected account/model when state is missing or within
`probe_lead_seconds` (default 300) of expiry. Concurrent requests share one queued
probe. Manual probes and failure-triggered probes remain available. The request
wait is bounded (default 1500 ms; negative means enqueue without waiting); the
worker may continue for `probe_timeout_seconds` after the request resumes. A
larger wait can improve the first request's injection hit rate but increases time
to first byte. At the deadline the request uses the available cache, or proceeds
without injection. `probe: false` and `probe_auth_ids` still govern probing.

`use_issued_at` decodes the public Fernet envelope (version, timestamp and block
layout), without decrypting or verifying its HMAC. It rejects malformed, expired
and implausibly future-dated state, and stops injecting expired state. Length is
still checked against `target_state_length`. All other modes keep the historical
expired-state fallback. Restoring persisted state always preserves `StoredAt`,
including manual entries; enabling timestamp checking also reparses its issue
time, so restarting cannot renew the token's lifetime.

`require_completed` waits for a delimited SSE `response.completed` whose
`response.status` is `completed`, even when state arrived in HTTP headers.
Harvest candidates are isolated by request/account/model and promoted only by a
successful terminal response (HTTP non-stream responses additionally require 2xx
and `status: completed`). Failed, incomplete, truncated and `[DONE]`-only streams
are rejected. WebSocket events use the same terminal check. CPA formats that do
not expose the Codex completion marker cannot be harvested in this mode. This
checks upstream completion, not whether the downstream client read every byte.
Candidate storage is bounded and stale candidates expire after two minutes.

`error_aware_backoff` queues early refresh for 429/502/503/504 and overload errors.
`usage_limit_reached` / `insufficient_quota` take precedence over HTTP status and
suppress all probes for that account for `quota_backoff_seconds` (default 900).
Probe quota failures stop the current attempt loop; transient probe failures use
the remaining `max_probe_attempts`. Backoff is in memory and resets on process
restart. Refreshing state cannot restore account quota. `rotate_proxy_start`
advances the pool's starting proxy once per proxy-probe round.
