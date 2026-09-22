v0.6.0 shipped cookie injection; this release makes it visible and catches the one thing that silently cancels it.

## Fixed in v0.6.1

- **Cookie injection silently did nothing — now diagnosed.** CPA's Codex executor does not forward client headers upstream; it copies a fixed whitelist (`x-codex-turn-state`, `x-codex-turn-metadata`, `session_id`, `User-Agent`, `Originator`, ...) and **`Cookie` is not on it**. A cookie header written by the plugin was therefore dropped before the request left CPA, which is why `已注入` climbed while nothing changed. The status page now checks every Codex auth file for the one declaration that opens the path and names the accounts that lack it:

  ```json
  { "access_token": "...", "headers": { "Cookie": "$Cookie" } }
  ```

  Without it the page shows an amber banner with the affected accounts, and each account card reads `Cookie 转发: 未开启（注入的 Cookie 到不了上游）`.

## Changed in v0.6.1

- **The injection table has a dedicated `Cookie 请求头` column** next to `State 请求头`, showing the cookies actually attached to that request (`__cflb=***; __oailb=***`) plus their age. Values stay masked.
- **The request-header JSON records the injected cookie.** Previously `Cookie` only appeared when the inbound client request already carried one, so an injected cookie was invisible; the log now always records `"Cookie": "[redacted]"` when the plugin attached it.
- `Cookie 转发` is reported per account in the JSON status view as well (`cookie_forward: on | missing | custom | unknown`).

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the plugin store, and install **Codex Turn State Probe**:

```text
https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

Restart CPA after updating so the new library is loaded.
