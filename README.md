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
- Accepts every state the upstream returns. Length and Fernet block count are reported on the status page, never used to refuse a ticket (see "Why the shape filter is gone").
- Harvests state from regular HTTP, SSE, and WebSocket Codex responses.
- Captures the account-level routing cookies (`__cflb`, `__oailb`) that now travel with a ticket, and injects them whether or not a ticket is cached.
- Caches state in memory by exact runtime auth ID plus resolved model.
- Replaces `X-Codex-Turn-State` on later matching requests while the state is within `ttl_seconds` (default: five minutes — a measured 292 only lasted about 200 seconds).
- Ages a cached ticket out on `ttl_seconds`. A returned shape is not a refusal signal and no longer retires anything.
- Never shares state across accounts or models. Routing cookies are account-level, so every model of one account reuses the same live pair, exactly as the upstream issues them.

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
      interval_seconds: 120
      ttl_seconds: 300
      inject: true
      harvest: true
      probe: true
      direct_probe: true
      inject_cookies: true
      harvest_cookies: true
      probe_send_cookies: false
      cookie_ttl_seconds: 300
      probe_schedule: state_aware
      probe_lead_seconds: 180
      show_state_values: false
      show_account_details: false
      show_injection_headers: false
      probe_log_limit: 200
      max_probe_attempts: 3
      attempts_per_route: 1
      failure_reprobe_threshold: 3
      prompt: "."
```

### Fields

- `proxy`: one or more rotating proxy endpoints, one per line. Supports `host:port:user:password`, provider-style `socks5://host:port:user:password`, and standard `socks5://user:password@host:port` / HTTP(S) URLs. Credentials are URL-encoded internally; bracket IPv6 literals. A nonmatching state advances to the next proxy on the following attempt.
- `proxies`: recommended list form for multiple proxies, one item per entry. It is merged with `proxy`; use this in the CPA plugin config UI when the `proxy` string field collapses pasted newlines. Pasting a JSON array into `proxy` works too.
- Proxy entry format: `host:port:user:password` (credentials optional, so `host:port` is fine), `scheme://user:password@host:port`, or a plain `host:port:user:password` line prefixed with the scheme. A single unparsable entry is skipped with a log line instead of aborting the round.
- Repeating the same address is allowed and is no longer deduplicated: a rotating residential endpoint that hands out a new exit IP per connection counts once per listed entry. Listing it once and raising `attempts_per_route` is equivalent.

### Filling the proxy fields

The `proxies` field is an array; all three shapes below are valid (a multi-line JSON array pasted into the editor works too):

```json
["us.rrp.example:10000:user-zone:pw", "us.rrp.example:10000:user-zone:pw"]
```

```yaml
proxies:
  - "us.rrp.example:10000:user-zone:pw"
  - "socks5://user:pw@us.rrp.example:10000"
```

```yaml
# legacy field: one entry per line, a pasted JSON array also works
proxy: |
  us.rrp.example:10000:user-zone:pw
  us.rrp.example:10000:user-zone:pw
```

For SOCKS5-only providers such as BestGo, set `proxy_scheme: socks5` when the entries carry no scheme.
- `proxy_scheme`: default protocol for `proxy`/`proxies` entries that omit a scheme. Choose `http` or `socks5`; the latter is required for SOCKS5-only providers such as BestGo. Default: `http`.
- `auth_ids`: exact Codex runtime auth IDs. Empty permits every Codex auth visible to the host.
- `probe_auth_ids`: auth IDs that may be probed. Empty probes every auth in the `auth_ids` scope; the status page also supports per-account selection.
- `models`: exact upstream model IDs. Defaults to `gpt-5.6-sol` and `gpt-6-astra`.
- `interval_seconds`: delay after one full probe cycle finishes. Default: `120`. A ticket only lives minutes now, so a 30-minute interval leaves long stretches of requests going out bare.
- `probe_schedule`: `fixed` probes on a constant interval; `state_aware` skips periodic probes while a fresh state is cached and only resumes shortly before expiry; `on_demand` probes only when a request needs it. Default: `state_aware`.
- `probe_lead_seconds`: lead time before state expiry used by `state_aware`/`on_demand`. Default: `180`. Set it to `0` to drive the same threshold from `state_refresh_seconds` instead.
(`target_state_length` and `accepted_blocks` were removed in 0.6.3: the status page shows `收票门槛: 不限`.)
- `ttl_seconds`: maximum cache age for injection. Default: `300` (five minutes). A measured 292 only survived about 200 seconds, so a shorter default is the safer one: injecting an expired ticket cannot make an answer better. The status page reports the measured combo lifetime so the value can be tuned from data.
- `inject_cookies`: send the account-level routing cookies. Default: `true`. Cookies no longer depend on a cached ticket: they are injected even when the cache is empty (the injection log records the source as `cookie-only`). **The cookies only reach the upstream when the auth file declares `"headers": {"Cookie": "$Cookie"}`** — see the requirement section above.
- `harvest_cookies`: collect `__cflb`/`__oailb` from upstream responses (production traffic and probes). Default: `true`. Only those two names are ever read, stored or displayed.
- `probe_send_cookies`: make probes carry the account's current live routing cookies. Default: `false` — a probe is a cold request. The jar is keyed by account while probes rotate egresses, so re-sending a cookie captured on one exit from another exit asks the upstream for a ticket for a route the connection is not on, which is a known 312 source. A cold probe cannot contradict itself and its response still hands back the fresh ticket plus its cookie pair; turn this on only to mirror production traffic on probes. The status page chip `探测凭据` shows which mode is active and every probe log line records `cookies_sent`.
- `cookie_ttl_seconds`: hard cap on how long a routing cookie is kept. Default: `300`. A shorter upstream `Max-Age`/`Expires` wins.
(0.6.3 removed `invalidate_on_reject`: it rested on "an unexpected shape means the upstream refused the ticket", and measurement retired that criterion.)
- `state_refresh_seconds`: age at which a cached ticket counts as renewable for `state_aware`/`on_demand`. Default: `0`, meaning `probe_lead_seconds` decides.
- `inject`: inject fresh cached state into matching requests. Default: `true`.
- `harvest`: collect matching state from normal Codex traffic. Default: `true`.
- `probe`: run background probes. Default: `true`.
- `direct_probe`: send a no-proxy request before the proxy attempts for each auth/model; an accepted result is cached and ends the round. Default: `true`, because the direct egress is what produced accepted tickets in practice. Set it to `false` to keep probes on the proxy pool only. Combined with an empty proxy pool this becomes direct-only probing.
- `show_state_values`: display and retain future full state values in the status page/JSON probe log. Default: `false`; enable only on a protected management endpoint.
- `probe_log_limit`: maximum in-memory attempt records. Default: `200`, maximum: `1000`.
- `max_probe_attempts`: attempts per auth/model in one cycle. Default: `3`.
- `attempts_per_route`: how many times one egress is tried before the probe moves on to the next one, inside the `max_probe_attempts` budget. Retries are driven by a failed attempt (no state, non-2xx, transport error), no longer by an unexpected length. Default: `1`, maximum: `10`.
- `failure_reprobe_threshold`: consecutive production request failures inside the current state window that trigger a targeted reprobe. Default: `3`; a negative value disables this behavior.
- `max_output_tokens`: deprecated compatibility field. It is ignored because Codex upstream rejects token-limit parameters.
- `prompt`: minimal probe input. Default: `.`.

When `probe` is enabled, there must be at least one usable egress: a proxy entry, or direct probing (on by default since 0.6). A wrong-length state consumes an attempt and is not cached.

### Tickets and cookies (0.6 and later)

Measured on one account and egress: the ticket and the cookies are independent. The ticket carries the qualification, the cookies carry the routing.

- A ticket-plus-cookie pair stayed good for about **200 seconds** (nine consecutive good answers up to 191.7s; the upstream started answering 312 at ~267s). It is not an hour.
- Tickets and cookies do not need to be paired: swapping tickets inside one conversation works, and deliberate mismatches work too. The plugin therefore keeps one cookie pool per **account** and reuses the newest live pair for every model.
- Cookies expire on their own, and they are the more likely half to die first. The plugin tracks ticket age and cookie age separately and reports the last invalidation reason per model.

The defaults work together: `state_aware` renewal and cold probes that mint a fresh ticket and cookie pair instead of recycling the previous one. Tickets live until `ttl_seconds`; 0.6.3 removed "upstream refused our ticket" invalidation because the criterion behind it (an unexpected shape) tracks the response shape, not the ticket's fate.

### Why the shape filter is gone (0.6.3)

0.6.0–0.6.2 had an admission filter: only states whose Fernet block count appeared in `accepted_blocks` (default `[10, 12]`, i.e. 292/332) were cached, and a 312 (11 blocks) was read as the upstream refusing the ticket. Measurement retired that premise:

- **The shape changes on its own.** The same gateway with the same routing cookie returned 292 on one request and 312 two minutes later.
- **The shape is not carryable.** Injecting a captured 292 back never returned that 292 (3 pairs out of 3 changed the ticket; the control group without any state behaved identically).
- **Length tracks whether that turn produced reasoning content.** 312 responses carry a `reasoning` output item with an encrypted reasoning block, 292 responses do not; neither tracks which gateway served the request.

The consequence was harsh: while the upstream was handing back 312 everywhere, the filter failed **every probe** and invalidated the cache on **every production request**, so the ticket pool could never fill. 0.6.3 removes the filter, and removes `invalidate_on_reject` with it since it rested on the same criterion. Tickets now live until `ttl_seconds`; length and block count stay visible on the status page as diagnostics.

### Requirement: the account must forward cookies

CPA's Codex executor does **not** forward client headers upstream. It copies a fixed whitelist (`x-codex-turn-state`, `x-codex-turn-metadata`, `session_id`, `User-Agent`, `Originator`, ...) and **`Cookie` is not on it**. A cookie header written by the plugin is therefore dropped before the request leaves CPA: the page will show `已注入` climbing while `带Cookie注入` stays at zero, and probes are wasted.

To open that path, add one custom header to each Codex auth file:

```json
{
  "access_token": "...",
  "headers": { "Cookie": "$Cookie" }
}
```

`$Cookie` is CPA's substitution syntax: it copies the request's `Cookie` header (the one this plugin fills in) onto the upstream request. Without it the status page raises an amber banner naming the affected accounts and each account card reads `Cookie 转发: 未开启`.

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

The page shows account cards at the top with a stable per-account color, current state length, live countdown, and requests/successes/total tokens/average TTFT for the current state window, followed by recent probe results and every proxy attempt. Each card also counts five things the state value cannot show: `已注入` (requests that carried a cached ticket), `带Cookie注入` (those that also carried the routing cookies), `裸发` (requests that passed every gate but left with no state), `回票相同` (harvested responses that handed back the exact ticket already held) and `换票` (responses that carried a different one). A non-zero `裸发` means injection is silently failing; `带Cookie注入` well below `已注入` means no cookies have been captured; mostly `回票相同` means the upstream returns the ticket it was given, mostly `换票` means it reissues one per turn. Under the account label the page lists the live cookie names, their age and provenance (never their values), and each model card reports the measured combo lifetime plus the last invalidation reason (upstream refusal, TTL, or a suspected cookie expiry). Proxy credentials and access tokens are never displayed. Full state values are displayed only when `show_state_values: true`; existing records captured while it was disabled remain hidden.

## Troubleshooting "Operation failed; inspect local plugin logs for details"

That text is the redacted fallback used only when the plugin cannot safely echo the original error to the page. Since 0.5.2 the errors the plugin raises itself are translated instead:

| Page message | Meaning |
| --- | --- |
| `未配置代理，且 direct_probe 未开启` | Probing needs an egress. Add a proxy, or enable `direct_probe` (on by default since 0.6) for direct-only probing. |
| `代理配置里没有一条能解析` | Fill one `host:port:user:password` per line (credentials optional) or paste a JSON array. Unparsable lines are skipped and logged with their line number. |
| `探测出口连接失败` | The proxy is unreachable, throttled, or the scheme is wrong (SOCKS5-only providers such as BestGo need `proxy_scheme: socks5`). See logs for the egress and cause. |
| `上游返回 HTTP 4xx/5xx` | Upstream rejected the probe; the account backs off per `error_aware_backoff` / `quota_backoff_seconds`. |

Logs live in the CPA log page or `<runtime dir>/logs/main.log`; grep for `codex-turn-state`. Failed probe attempts now log `route` / `proxy_index` / `attempt` / `error` each time, with proxy credentials redacted.

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
`probe_lead_seconds` (default 180) of expiry. Concurrent requests share one queued
probe. Manual probes and failure-triggered probes remain available. The request
wait is bounded (default 1500 ms; negative means enqueue without waiting); the
worker may continue for `probe_timeout_seconds` after the request resumes. A
larger wait can improve the first request's injection hit rate but increases time
to first byte. At the deadline the request uses the available cache, or proceeds
without injection. `probe: false` and `probe_auth_ids` still govern probing.

`use_issued_at` decodes the public Fernet envelope (version, timestamp and block
layout), without decrypting or verifying its HMAC. It rejects malformed, expired
and implausibly future-dated state, and stops injecting expired state. Those are
envelope-level checks and say nothing about length. All other modes keep the historical
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

The public login shell first reads the host management key from
`localStorage["cli-proxy-auth"]` for automatic sign-in; it only reads that value,
never writes it back, and falls back to manual entry on failure.
