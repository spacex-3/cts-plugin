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

### 配置项

- `proxy`：轮换代理。支持 `host:port:user:password`，以及标准 `http://`、`https://`、`socks5://` URL；IPv6 地址需要方括号。
- `auth_ids`：精确的 Codex 运行时账号 ID。留空表示允许所有可见 Codex 账号。
- `models`：精确的上游模型 ID。默认是 `gpt-5.6-sol`、`gpt-6-astra`。
- `interval_seconds`：上一轮完整探测结束后，到下一轮的等待时间。默认 `300`。
- `target_state_length`：只缓存指定长度的 state。默认 `292`。
- `ttl_seconds`：缓存可用于注入的最长时间。默认 `3600`。
- `inject`：向后续匹配请求注入缓存。默认 `true`。
- `harvest`：从正常 Codex 流量采集。默认 `true`。
- `probe`：启用后台轮换代理探测。默认 `true`。
- `max_probe_attempts`：每轮中每个账号+模型最多尝试次数。默认 `3`。
- `max_output_tokens`：探测请求的最大输出 token。默认 `16`。
- `prompt`：最小探测输入。默认 `.`。

启用 `probe` 时必须配置 `proxy`。长度不符合要求的 state 会消耗一次尝试，但不会写入缓存。

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

页面不会显示代理用户名/密码、Access Token 或 state 原文。

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
- 每次探测尝试都会创建独立 uTLS HTTP/2 连接，便于轮换代理服务分配新出口。
- 发现 state 后会立即停止读取并关闭连接；最终计费和用量仍由上游决定。
- 不要设置过短的探测间隔，并遵守上游和代理服务条款。
- state 是不透明的上游数据；长度符合要求不等于一定有效，也不保证任何路由、容量或账号效果。

本独立仓库基于 CLIProxyAPI 的 `examples/plugin/codex-turn-state` 实现整理，并保留 MIT 许可证。
