# Codex Turn State Probe

这是一个 CLIProxyAPI 原生动态库插件：通过轮换代理主动探测 `X-Codex-Turn-State`，也可从正常 Codex 流量中自动采集；随后按“实际选中的账号 ID + 上游模型”缓存，并在 TTL 有效期内注入后续匹配请求。

## 通过 CPA 插件商店安装

需要启用了原生动态库插件、且兼容 v7.3.6 插件 ABI 的 CLIProxyAPI。把下面的源添加到现有 `config.yaml` 的 `plugins` 配置中：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/spacex-3/cts-plugin/main/registry.json
```

刷新 CPA 管理后台的插件商店，搜索 **Codex Turn State Probe** 并安装。商店会自动选择当前系统和架构的 Release 压缩包，并使用 `checksums.txt` 校验。

安装后，把 [`config.example.yaml`](config.example.yaml) 中的插件配置合并到 CPA 配置。不要重复创建顶层 `plugins`。

## 和公开的固定值插件有什么不同

两者都会在 CPA 已经选定实际 Codex 凭据后，按凭据处理 `X-Codex-Turn-State`，也都使用 CPA 原生动态库插件 ABI 和商店 Release 格式。

本插件的主要差异：

- 不是在管理页面手工粘贴固定 state；而是使用轮换代理主动探测。
- 还会从正常 HTTP、SSE、WebSocket Codex 响应自动采集。
- 缓存键是精确的运行时账号 ID **加模型**，不会跨账号或模型共享。
- 有长度过滤和 TTL；默认只接受长度 `292`，默认一小时后失效。
- 缓存仅存在于 CPA 进程内，CPA 重启或插件重载后清空。
- 管理资源用于查看脱敏状态和手动触发探测，不用于保存固定 state。

## 配置

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

### 配置项

- `proxy`：一个或多个轮换代理，每行一个。支持 `host:port:user:password`、代理商常见的 `socks5://host:port:user:password`，以及标准 `socks5://user:password@host:port` / HTTP(S) URL；凭据会在内部自动 URL 编码，IPv6 地址需要方括号。未命中目标长度时，下一次尝试自动轮询下一行代理。
- `proxies`：推荐的多代理列表配置，每项一个代理；插件会把它和 `proxy` 合并。CPA 管理页对这个字段按 JSON 数组解析，因此请填写为 `["host:port:user:pw", "host2:port:user:pw"]`，不要直接逐行粘贴裸文本。
- `proxy_scheme`：`proxy`/`proxies` 条目未写协议前缀时采用的默认协议。可选 `http` 或 `socks5`；BestGo 这类 SOCKS5 节点必须选 `socks5`。默认 `http`。
- `auth_ids`：精确的 Codex 运行时账号 ID。留空表示允许所有可见 Codex 账号。
- `probe_auth_ids`：只参与探测的账号。留空表示探测 `auth_ids` 范围内的全部账号；也可在状态页逐账号勾选并保存。
- `models`：精确的上游模型 ID。默认是 `gpt-5.6-sol`、`gpt-6-astra`。
- `interval_seconds`：上一轮完整探测结束后，到下一轮的等待时间。默认 `1800`，即 30 分钟。
- `probe_schedule`：探测调度方式。`fixed` 为固定间隔；`state_aware` 在已持有新鲜 state 时跳过周期探测，等接近过期再续期。默认 `fixed`。
- `probe_lead_seconds`：`state_aware` 模式下，在 state 过期前提前多少秒开始探测。默认 `300`。
- `target_state_length`：只缓存指定长度的 state。默认 `292`。
- `accepted_blocks`：按 Fernet 密文块数接受的 state 类型。默认 `[10, 12]`：`10` 对应 Pro/Plus（292），`12` 对应 Team（332）；`11`/`13` 等异常态拒绝。合法 Fernet 优先按块数判断，非 Fernet 退回 `target_state_length`。
- `ttl_seconds`：缓存可用于注入的最长时间。默认 `3600`。
- `inject`：向后续匹配请求注入缓存。默认 `true`。
- `harvest`：从正常 Codex 流量采集。默认 `true`。
- `probe`：启用后台轮换代理探测。默认 `true`。
- `direct_probe`：每个账号+模型在代理尝试前额外执行一次不使用代理的基线请求。基线仅记录，不写入注入缓存。默认 `false`。
- `show_state_values`：在状态页和 JSON 日志中保留并显示之后捕获到的完整 state。默认 `false`；只应在受保护的管理入口启用。
- `probe_log_limit`：内存中保留的探测日志条数。默认 `200`，最大 `1000`。
- `max_probe_attempts`：每轮中每个账号+模型最多尝试次数。默认 `3`。
- `failure_reprobe_threshold`：当前 state 倒计时窗口内，生产请求连续失败达到该次数后自动对该账号+模型重新探测。默认 `3`；负数表示关闭。
- `max_output_tokens`：已弃用的兼容配置。Codex 上游拒绝 token 限制参数，因此插件会忽略该项。
- `prompt`：最小探测输入。默认 `.`。

启用 `probe` 时 `proxy` 必须至少配置一条。长度不符合要求的 state 会消耗一次尝试，但不会写入缓存。

## 是否会注入到当前账号后续所有 CPA 请求

会注入到**后续所有满足条件的 Codex 执行尝试**，但不是无条件覆盖全部请求。必须同时满足：

1. CPA 本次实际选中的运行时账号 ID 与缓存账号完全一致；
2. 解析后的上游模型与缓存模型完全一致；
3. 账号和模型仍在配置范围中；
4. 存在可用缓存（默认保留过期回退，`use_issued_at` 开启后严格检查真实年龄）；
5. `inject: true`。

匹配时会替换已有的同名请求头。轮询负载均衡如果选中了另一个账号，不会得到这个账号的 state。

## 状态页与手动探测

公开菜单地址 `/v0/resource/plugins/codex-turn-state/status` 只返回静态登录壳，
不含运行数据；所有查询操作（包括原来的 `?format=json`）都被拒绝。输入 CPA
管理密钥后加载原状态页，密钥只保留在本页内存，通过 `Authorization: Bearer ...`
请求头发送，退出或刷新后清除。CPA 的远程管理限制仍然生效。

数据与操作改为无 Menu 的 **受保护管理路由**：
`/v0/management/plugins/codex-turn-state/status`。

- `GET .../status`：受保护的 HTML 状态页。
- `GET .../status?format=json`：受保护的 JSON 状态。
- `GET .../status?op=fragment_probe_logs` / `fragment_injections`：受保护的局部刷新。
- `POST .../status?op=probe` / `probe_target`：排队探测。
- `POST .../status?op=save_manual_state` / `save_proxies` / `save_probe_accounts`：
  用 URL 编码表单请求体保存。GET 写操作返回 405，state、代理凭据不再放入 URL。

所有响应设置 `Cache-Control: no-store`。默认 HTML/JSON 不返回 state 原文、账号
邮箱/名称/标签、注入请求头；可能含邮箱的 auth ID 也统一替换成稳定 SHA-256 别名，
页面选择账号和操作会解析回真实 ID。`show_account_details: true` 显示真实身份；
`show_injection_headers: true` 显示请求头，但其中 state 仍受 `show_state_values`
控制，凭据头始终隐藏。上游错误原文不会经 HTTP 返回。

鉴权由 CPA 管理中间件负责，插件无法读取或自行验证 CPA 管理密钥。不可另设绕过
CPA 的无鉴权管理入口；应通过 HTTPS 保护传输中的密钥。获授权管理员仍能看到代理
拓扑和显式开启的诊断数据。同源脚本和本地文件属于信任边界，账号别名仅为假名化，
不提供不可猜测的匿名保证。运行缓存仍按原来的仅文件所有者可读写权限保存可用
state，本改动不加密本地存储。

登录壳会优先读取 CPA 前端 `localStorage["cli-proxy-auth"]` 中的宿主管理密钥并自动登录；
仅读取、不写回，失败时回退到手动输入。

自动登录支持 CPA 面板的 `enc::v1::` 和 `enc::v2::` 混淆值，并优先取 `.state.managementKey`。

页面顶部显示每个账号的彩色卡片，包含当前 state 长度、实时倒计时，以及当前窗口内的请求数、成功数、总 Token 和平均首字时间；下面显示最近探测和逐次代理尝试。命中目标长度的记录显示绿色，未命中或失败显示红色。页面永远不会显示代理用户名/密码或 Access Token。只有配置 `show_state_values: true` 后，后续捕获到的 state 原文才会进入页面和 JSON 日志；启用之前的记录不会恢复原文。

状态页顶部显示下一次定时探测倒计时，默认每 30 分钟执行一轮。探测日志支持按账号、模型和匹配结果筛选，并默认每页 10 条；注入记录同样默认每页 10 条。

插件启动时如果已经有未过期的缓存 state，会先跳过立即探测，等待下一次 30 分钟周期；只有在没有可用 state 时才会立即探测。`ttl_seconds` 是已缓存 state 可用于注入的时间，`interval_seconds` 是主动探测的间隔，两者互不影响。

一旦拿到过一次 292 state，即使超过 `ttl_seconds`，插件也不会主动清除；只要还没有新的 292 替换，旧 state 会继续注入，页面会以黄色提示“沿用旧 state”。只有新一次成功探测才会替换并重置倒计时。

注入记录会显示每次请求的时间、账号、模型、推理强度、端点路径（如 `/v1/responses`）、同步/流式、TPS、Token、首字时间、延迟、结果、注入的 `X-Codex-Turn-State` 请求头、完整脱敏请求头和 state 来源。表格每行单行显示，长内容默认截断，鼠标悬停可查看完整内容；探测日志和注入记录均有独立刷新按钮。

探测日志和注入记录使用固定列宽表格，不会超出页面宽度；两者的独立刷新按钮只刷新对应区域，不重新加载整个页面。

账号状态里每个模型卡片都有“仅探测此模型”按钮，可单独补测某一个账号+模型，不会触碰已经拿到 292 的其他模型。

页面里的“代理池粘贴”支持直接逐行粘贴原始格式，例如：

```text
us.rrp.bestgo.work:10000:USER-zone-custom-region-US:password
us.rrp.bestgo.work:10000:USER-zone-custom-region-US:password
```

选择协议为 `socks5` 后保存，无需改成 JSON 数组。保存内容会写入本机插件数据目录，CPA 重启后自动加载。

缓存 state、探测日志和注入记录也会持久化到插件数据目录的 `runtime.json`，更新或重载插件后仍会恢复；此前版本升级前丢失的内存记录无法找回。

## 本地构建与验证

```bash
go mod download
gofmt -w .
go test -race ./...
go vet ./...
mkdir -p dist
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/codex-turn-state.dylib .
```

macOS 使用 `.dylib`，Linux 使用 `.so`，Windows 使用 `.dll`。推送 `v*` 标签后，GitHub Actions 会构建并发布 CPA 商店所需的跨平台压缩包和校验文件。

## 注意事项

- 主动探测需要 CPA host API 可读取的文件型 Codex 凭据。仅存在于运行时的凭据不能直接探测，但仍可参与正常流量采集和注入。
- 开启 `direct_probe` 后，页面会先记录一次 `direct` 直连基线，再记录每一次实际发生的 `proxy` 尝试；每次代理尝试都会创建独立 uTLS HTTP/2 连接。
- 发现 state 后会立即停止读取并关闭连接；最终计费和用量仍由上游决定。
- 不要设置过短的探测间隔，并遵守上游和代理服务条款。
- state 是不透明的上游数据；长度符合要求不等于一定有效，也不保证任何路由、容量或账号效果。

本独立仓库基于 CLIProxyAPI 的 `examples/plugin/codex-turn-state` 实现整理，并保留 MIT 许可证。

### 可选的请求驱动生命周期

默认仍使用 `fixed` 定时探测和原有采集行为。按需模式及严格校验可显式开启：

```yaml
probe_schedule: on_demand
probe_wait_milliseconds: 1500
probe_timeout_seconds: 60
on_demand_cooldown_seconds: 600
use_issued_at: true
require_completed: true
error_aware_backoff: true
quota_backoff_seconds: 900
rotate_proxy_start: true
```

`on_demand` 不做启动探测、不运行周期定时器；有请求时，仅在所选账号/模型缺少
state 或进入 `probe_lead_seconds`（默认 300 秒）续期窗口时排队探测。同一桶的
并发请求共用探测。手动探测、请求失败触发的重探仍可用，`probe: false` 和
`probe_auth_ids` 仍生效。请求默认最多等 1500 毫秒，负值表示仅排队不等；超时后
使用已有缓存或不注入，后台探测最多继续到 60 秒总超时。增大等待时间提高首个
请求命中率，但会增加首包延迟。

`on_demand_cooldown_seconds`：在 `on_demand` 下，某账号+模型连续 3 轮探测都未命中
目标 state 后进入冷却。冷却期间请求门禁和 `error_aware_backoff` 补探都不会再为该
账号+模型发起探测；命中后立即清除冷却。默认 600 秒，防止持续失败的账号在高频请求下
反复探测触发风控。

`use_issued_at` 无需密钥解析 Fernet 信封的版本、签发时间和块布局；不会解密或验证
HMAC。保持目标长度检查，并拒绝无效、过期、明显来自未来的 state。开启后过期
state 不再注入；默认仍保留旧版过期 state 回退。恢复缓存始终保留原 `StoredAt`，
包括手动 state；开启此项还会重新解析真实签发时间，重启不会让令牌变年轻。

`require_completed` 要求 SSE 正常分帧结束的 `response.completed`，且
`response.status=completed`；响应头先带 state 也不能提前成功。采集候选按请求、
账号、模型隔离，只在成功终态提升；非流式响应还要求 HTTP 2xx 和 `status=completed`。
失败、截断、incomplete、只有 `[DONE]` 的流均不接受，WebSocket 同理。CPA 转换后
不含 Codex 完成标记的格式会放弃采集。此处确认上游完成，无法确认客户端读完每个
字节。候选数量/大小受限，两分钟后过期。

`error_aware_backoff` 对 429/502/503/504、overload 提前排队重探；额度错误
`usage_limit_reached` / `insufficient_quota` 优先于 HTTP 状态，按账号停止所有探测
默认 900 秒。探测中的额度失败会终止本轮重试，临时错误在 `max_probe_attempts`
范围内继续。退避只保存在内存，进程重启会重置；刷新 state 无法恢复账号额度。
`rotate_proxy_start` 每轮代理探测递增起点，默认关闭。
