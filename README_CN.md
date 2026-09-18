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
      show_state_values: true
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
4. state 长度正确且没有超过 TTL；
5. `inject: true`。

匹配时会替换已有的同名请求头。轮询负载均衡如果选中了另一个账号，不会得到这个账号的 state。

## 状态页与手动探测

```text
/v0/resource/plugins/codex-turn-state/status
```

- `GET .../status`：脱敏 HTML 状态页。
- `GET .../status?format=json`：JSON 状态。
- `POST .../status` 或 `GET .../status?op=probe`：立即排队执行一轮探测。

页面顶部显示每个账号的彩色卡片，包含当前 state 长度、实时倒计时，以及当前窗口内的请求数、成功数、总 Token 和平均首字时间；下面显示最近探测和逐次代理尝试。命中目标长度的记录显示绿色，未命中或失败显示红色。页面永远不会显示代理用户名/密码或 Access Token。只有配置 `show_state_values: true` 后，后续捕获到的 state 原文才会进入页面和 JSON 日志；启用之前的记录不会恢复原文。

状态页顶部显示下一次定时探测倒计时，默认每 30 分钟执行一轮。探测日志支持按账号、模型和匹配结果筛选，并默认每页 10 条；注入记录同样默认每页 10 条。

插件启动时如果已经有未过期的缓存 state，会先跳过立即探测，等待下一次 30 分钟周期；只有在没有可用 state 时才会立即探测。`ttl_seconds` 是已缓存 state 可用于注入的时间，`interval_seconds` 是主动探测的间隔，两者互不影响。

一旦拿到过一次 292 state，即使超过 `ttl_seconds`，插件也不会主动清除；只要还没有新的 292 替换，旧 state 会继续注入，页面会以黄色提示“沿用旧 state”。只有新一次成功探测才会替换并重置倒计时。

注入记录会显示每次请求的时间、账号、模型、推理强度、端点路径（如 `/v1/responses`）、同步/流式、TPS、Token、首字时间、延迟、结果、注入的 `X-Codex-Turn-State` 请求头、完整脱敏请求头和 state 来源。表格每行单行显示，长内容默认截断，鼠标悬停可查看完整内容；探测日志和注入记录均有独立刷新按钮。

探测日志和注入记录使用固定列宽表格，不会超出页面宽度；两者的独立刷新按钮只刷新对应区域，不重新加载整个页面。

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
