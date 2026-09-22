The admission filter is gone. Any state the upstream returns is now cached and injected, and routing cookies no longer wait for a ticket.

## The measurement behind this release

0.6.0–0.6.2 accepted a ticket only when its Fernet block count appeared in `accepted_blocks` (default `[10, 12]` ≈ 292/332). A 312 (11 blocks) was read as *the upstream refusing the ticket we injected*, and the cache entry was dropped on that very response. Three observations retired that premise:

- **The shape changes on its own.** The same gateway, the same routing cookie and the same two-minute window returned 292 on one request and 312 on the next. Two gateways flipped in opposite directions within the same minute.
- **The shape is not carryable.** Injecting a captured 292 back never returned that 292 — 3 pairs out of 3 changed the ticket, and the control group without any state behaved identically. Even a captured 312 came back as a different 312.
- **Length tracks whether that turn produced reasoning content.** 312 responses carry a `reasoning` output item with an encrypted reasoning block (`gAAAAA…`) and 13–24 reasoning tokens; 292 responses carry none. It never tracked which gateway served the request, which is what a routing metric would have to do.

## Changed in v0.6.3

- **No admission filter.** `accepted_blocks` and `target_state_length` are gone, along with the "turn state rejected (length …, blocks …)" error path. That error fired on **every** probe while the upstream was handing back 312 everywhere, which is why probing showed `Operation failed; inspect local plugin logs for details` and the ticket pool stayed empty. Length and block count are still reported on the status page (chip `收票门槛: 不限`) as diagnostics, never as gates.
- **Cookies no longer wait for a ticket.** The interceptor used to short-circuit when the cache was empty, so a request between two successful probes carried neither the state nor the cookies that had already been captured. Now the Cookie header is built and injected independently; the injection log records the source as `cookie-only` so the two paths stay distinguishable.
- **`invalidate_on_reject` removed.** It only ever fired on a shape mismatch, so it retired working tickets — and in today's upstream mode it would have invalidated on every single request. Tickets now live until `ttl_seconds`, and the cookie pool ages out on `cookie_ttl_seconds`. The combo-lifetime table keeps reporting TTL-driven expiries.
- **Retries are failure-driven.** `attempts_per_route` and `max_probe_attempts` still apply, but they retry on a failed attempt (no state, non-2xx, transport error) rather than on an unexpected length. The first state that comes back wins.

## Unchanged

Everything from 0.6.1 and 0.6.2 still applies: cold probes by default, the `探测凭据` chip, `cookies_sent` on every probe log line, the `Cookie 请求头` column in the injection table, and the requirement that each Codex auth file declares `"headers": { "Cookie": "$Cookie" }` before the upstream receives any cookie at all — the status page banners the accounts that still lack it.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

CPA hot-reloads plugins when they are installed from the store; check the log for `plugin hot reloaded` and the new version number. Restart CPA only if the reload did not happen.
