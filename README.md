# Command Code CLIProxyAPI 插件

这是 CLIProxyAPI 原生动态库插件的注册切片。插件 ID 为 `command-code`，固定使用：

- CLIProxyAPI `v7.3.8`
- C ABI `1`
- 插件 JSON schema `6`
- Command Code wire `1.58.0`

版本契约记录在 [compatibility.json](./compatibility.json)。插件启动时通过宿主的 `host.http.do` 检查 `command-code` npm 包版本；发现漂移只写告警，始终继续使用已验证的 `1.58.0` wire 版本。

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    command-code:
      enabled: true
      priority: 1
      api-key: "user_..."
```

`api-key` 必须以 `user_` 开头。插件只在进程内保存该值，不把它写入注册响应或日志。

## 构建

需要与 CLIProxyAPI `v7.3.8` 一致的 Go 工具链（当前模块声明 Go `1.26.0`）。原生插件必须按目标平台构建：

```bash
cd plugin/command-code
go test ./...
go build -buildmode=c-shared -o ../../plugins/linux/amd64/command-code.so .
```

Windows 使用 `command-code.dll`，macOS 使用 `command-code.dylib`。构建产生的 `.h` 文件不是运行时插件，不需要提交。

当前切片完成插件注册、模型注册、凭证配置边界，以及 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 三条 executor 路径。executor 通过宿主 `host.http.*` 发起 Command Code 请求；只有收到合法完成信号才产生成功尾部，截断、上游错误和断连会显式失败。

宿主负责客户端请求体限制、并发限制和连接生命周期。插件不启动 HTTP server，也不直接使用独立 transport；所有上游请求都通过 `HostHTTPClient` 的 `host.http.do` / `host.http.do_stream` 回调。宿主发出 `plugin.quiesce` 后，插件拒绝新请求但允许已接受的流排空；shutdown 会等待这些异步流和版本漂移检查结束。

Linux amd64 的测试、动态库构建和 C ABI 加载 smoke 由 `.github/workflows/cliproxyapi-plugin-validation.yml` 执行。手动验证可运行：

```bash
cd plugin/command-code
go test ./...
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -o command-code.so .
```

参考：[CLIProxyAPI Plugin Development](https://help.router-for.me/plugin/development)、[Model Registrar](https://help.router-for.me/plugin/model-registrar)、[Executor](https://help.router-for.me/plugin/executor)、[Host Callbacks](https://help.router-for.me/plugin/host-callbacks)。
