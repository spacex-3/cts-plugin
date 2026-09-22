A turn state is no longer enough on its own: the routing cookies now decide how long a ticket holds.

## Changed in v0.6.0

- **Default TTL is now five minutes**, down from one hour. A measured 292 kept working for nine consecutive answers up to 191.7 seconds and the upstream started answering 312 at about 267 seconds, so an hour-long cache mostly meant injecting a dead ticket. `ttl_seconds: 3600` restores the old value.
- **Direct probing is on by default.** The direct egress is what produced accepted tickets in practice; set `direct_probe: false` to keep probes on the proxy pool only.
- **Probe scheduling defaults to `state_aware`** with a 2-minute interval and a 180-second renewal lead, instead of `fixed` with 30-minute intervals. With a five-minute ticket, a 30-minute interval leaves long stretches of requests going out bare.
- Probes, activation and status wording now reflect that a ticket lives minutes, not an hour.

## Added in v0.6.0

- **Routing cookie support.** The plugin captures `__cflb` and `__oailb` from upstream responses (production traffic and probes) and injects them together with the state. Only those two names are read, stored or forwarded; every other cookie is ignored. `inject_cookies`, `harvest_cookies`, `probe_send_cookies`, `cookie_ttl_seconds` control the behaviour, and the cookies are account-level, so one live pair is reused by every model of that account.
- **`invalidate_on_reject` (default on).** When a request that carried our injected state comes back with a state the plugin rejects — a 312, for example — the cached entry is dropped immediately and a reprobe is queued, instead of injecting the dead ticket until the TTL expires. Bare requests cannot trigger this, so their 312s never retire a good ticket. This is the upstream telling us the ticket is dead for free, and it is more accurate than any fixed TTL.
- **Measured combo lifetime.** The plugin now records how long each (ticket + cookie) pair actually survived, why it ended (upstream refusal attributed to the ticket or to the older cookie, or simply the TTL), and reports the average/minimum/last lifetime per account+model, so `ttl_seconds` can be tuned from data rather than guessed.
- **Injection accounting for cookies.** The status page counts `带Cookie注入` next to `已注入` / `裸发` / `回票相同` / `换票`, and lists the live cookie names, their age and provenance per account. Cookie values are never rendered; the JSON view exposes names, lengths and ages instead.
- `state_refresh_seconds` expresses the renewal point as "ticket older than N seconds" when `probe_lead_seconds` is not the right knob.
- `cookie_ttl_seconds` caps how long a routing cookie is kept; a shorter upstream `Max-Age`/`Expires` always wins.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Then merge `config.example.yaml` into the existing CPA configuration. The new defaults apply as soon as the plugin reloads; set the old values explicitly to keep the previous behaviour.
