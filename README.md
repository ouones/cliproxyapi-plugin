# Command Code CLIProxyAPI 插件

这是可直接安装到 CLIProxyAPI 的原生动态库插件。插件 ID 为 command-code，当前兼容契约为：

- CLIProxyAPI v7.3.8
- C ABI 1
- 插件 JSON schema 6
- Command Code wire 1.58.0

版本契约见 [compatibility.json](compatibility.json)。本仓库只包含插件源码、协议测试、构建脚本和安装脚本，不包含独立代理服务或宿主程序。

## 宿主配置

在 CLIProxyAPI 的配置文件中启用插件：

~~~yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    command-code:
      enabled: true
      priority: 1
      api-key: "user_..."
~~~

api-key 必须以 user_ 开头。插件只在进程内保存该值，不把它写入注册响应或日志。将动态库复制到宿主的 plugins 目录后，必须重启 CLIProxyAPI，宿主才会重新发现并注册插件。

## 构建

需要与 CLIProxyAPI v7.3.8 兼容的 Go 工具链。本模块声明 Go 1.26.0。所有命令都从本仓库根目录执行。

Linux amd64：

~~~bash
./scripts/build-plugin.sh linux amd64
~~~

Windows amd64：

~~~powershell
pwsh -NoProfile -File ./scripts/build-plugin.ps1 -GOOS windows -GOARCH amd64
~~~

macOS Apple Silicon：

~~~bash
./scripts/build-plugin.sh darwin arm64
~~~

构建产物分别为 dist/linux/amd64/command-code.so、dist/windows/amd64/command-code.dll 和 dist/darwin/arm64/command-code.dylib。构建时会生成并清理同目录下的 C 头文件；头文件不是运行时插件，不应提交。

## 安装

推荐使用安装脚本。安装脚本只接收一个已有动态库和一个显式宿主插件目录，只覆盖目标目录中的 command-code 动态库，不读取或修改宿主配置、API key 或其他插件文件。

Linux 或 macOS：

~~~bash
./scripts/install-plugin.sh --artifact dist/linux/amd64/command-code.so --plugin-dir /path/to/cliproxyapi/plugins
~~~

Windows：

~~~powershell
pwsh -NoProfile -File ./scripts/install-plugin.ps1 -ArtifactPath ./dist/windows/amd64/command-code.dll -PluginDir C:/path/to/cliproxyapi/plugins
~~~

也可以手动复制产物：

~~~bash
cp dist/linux/amd64/command-code.so /path/to/cliproxyapi/plugins/command-code.so
~~~

Windows 使用 Copy-Item 将 command-code.dll 复制到宿主 plugins 目录，macOS 使用 command-code.dylib。完成复制后重启宿主。

## 本地验证

常规 Go 验证：

~~~bash
go test ./...
go vet ./...
~~~

自动化 CI 还会执行 Linux amd64 的 c-shared 构建、ELF64 架构检查，以及使用 testdata/load-plugin-smoke.c 编译并运行 C ABI loader smoke。对应 workflow 为 .github/workflows/validate.yml。

这些检查验证插件源码、执行器、宿主回调和动态库 ABI。真实宿主发现与注册测试需要匹配的 CLIProxyAPI v7.3.8 可执行文件；当该可执行文件不可用时，本独立仓库不宣称已完成真实宿主注册测试。

## 运行边界

插件不启动 HTTP server，也不直接创建独立 transport。所有上游请求都通过宿主提供的 host.http.do、host.http.do_stream 和相关流回调完成。插件支持 OpenAI Chat Completions、OpenAI Responses 与 Anthropic Messages 三条 executor 路径，并遵守宿主的请求、并发和连接生命周期管理。

参考：[CLIProxyAPI Plugin Development](https://help.router-for.me/plugin/development)、[Model Registrar](https://help.router-for.me/plugin/model-registrar)、[Executor](https://help.router-for.me/plugin/executor)、[Host Callbacks](https://help.router-for.me/plugin/host-callbacks)。
