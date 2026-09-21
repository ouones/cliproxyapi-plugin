# Command Code CLIProxyAPI 插件

把 Command Code 接入 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)，并自动同步 Command Code 的实时模型目录。

插件 ID 是 **command-code**。它是 CLIProxyAPI 加载的原生动态库，不单独启动 HTTP 服务，也不需要另外运行一个代理进程。

## 先看这里：你要做什么

| 需求 | 操作 | 是否需要重启 CLIProxyAPI |
| --- | --- | --- |
| 第一次安装插件 | 安装动态库、配置 API key、重启宿主 | 需要一次 |
| 更新插件版本 | 再次运行一键安装脚本，或重新构建并替换动态库 | 需要一次 |
| 更新 Command Code 模型列表 | 调用插件配置 PATCH 接口 | 不需要 |

插件更新和模型列表刷新是两件事：更新插件需要重新加载动态库；插件已经加载后，刷新模型列表只需要触发 plugin.reconfigure。

## 兼容性

| 组件 | 要求 |
| --- | --- |
| CLIProxyAPI | v7.3.8 |
| 操作系统（一键安装） | Linux amd64 |
| 插件 C ABI | 1 |
| 插件 JSON schema | 6 |
| Command Code wire | 1.58.0 |

Command Code API key 必须是 user_... 格式。管理 API 使用的是 CLIProxyAPI 的 management key，两者不是同一个值。

## 第一次安装：Linux amd64 一键安装

在已经运行 CLIProxyAPI 的 Linux amd64 主机上执行：

~~~bash
curl -fsSL https://raw.githubusercontent.com/ouones/cliproxyapi-plugin/master/scripts/install-online.sh | sudo bash
~~~

在线安装需要 `curl`、`sha256sum`、`install` 和向插件目录写入文件的权限。缺少依赖或权限不足时脚本会直接退出，不会跳过校验继续安装。

脚本会：

1. 自动寻找唯一的 CLIProxyAPI 插件目录；
2. 下载 GitHub Release 中的 command-code-linux-amd64.so；
3. 校验 SHA256；
4. 原子替换插件文件，并在替换旧版本时留下时间戳备份。

脚本不会修改 CLIProxyAPI 配置、API key、其他插件，也不会自动重启服务。

安装器必须能检测到唯一的插件目录。多实例、宿主尚未启动或使用特殊目录时，请显式指定目录：

~~~bash
curl -fsSL https://raw.githubusercontent.com/ouones/cliproxyapi-plugin/master/scripts/install-online.sh \
  | sudo bash -s -- --plugin-dir /path/to/cliproxyapi/plugins
~~~

如果使用 Docker，请传入宿主机上持久化挂载的插件目录；不要把文件写入容器临时层。

安装固定版本：

~~~bash
curl -fsSL https://raw.githubusercontent.com/ouones/cliproxyapi-plugin/master/scripts/install-online.sh \
  | sudo bash -s -- --plugin-dir /path/to/cliproxyapi/plugins --version v0.1.0
~~~

一键安装器只支持 Linux amd64，并且下载的是 GitHub Release，不是 Git 仓库当前工作树。仅把代码推送到 master 不会自动生成可供安装器下载的新版本；必须先发布对应的 Release。

安装完成后重启 CLIProxyAPI：

~~~bash
sudo systemctl restart cliproxyapi
~~~

如果使用 Docker，请重启对应容器。服务名或容器名按你的实际部署修改。

## 配置插件

在 CLIProxyAPI 配置文件中启用插件：

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

plugins.dir 是宿主插件目录；相对路径按 CLIProxyAPI 的工作目录解析。首次安装后重启宿主，宿主才会发现并注册动态库。插件注册时会通过宿主提供的 HTTP transport 获取 Command Code 实时模型目录。

## 更新插件版本

### 已通过一键安装器安装

直接再次执行安装命令即可升级到最新已发布版本：

~~~bash
curl -fsSL https://raw.githubusercontent.com/ouones/cliproxyapi-plugin/master/scripts/install-online.sh \
  | sudo bash -s -- --plugin-dir /path/to/cliproxyapi/plugins
~~~

升级后重启 CLIProxyAPI，使进程加载新的 .so 文件。旧文件会以类似下面的名称保留在插件目录中：

~~~text
command-code.so.backup-20260921T120000Z
~~~

相同 SHA256 的重复安装是幂等操作，不会重复创建备份。

### 从源码构建并同步

如果你维护的是源码副本，当前模型目录功能已经合并到 master。在源码目录执行：

~~~bash
git fetch origin
git switch master
git pull --ff-only origin master
~~~

Linux amd64 构建并安装：

~~~bash
export PATH="/usr/local/go/bin:$PATH"
./scripts/build-plugin.sh linux amd64
sudo ./scripts/install-plugin.sh \
  --artifact dist/linux/amd64/command-code.so \
  --plugin-dir /path/to/cliproxyapi/plugins
~~~

然后重启 CLIProxyAPI。构建需要 Go 1.26 和可用的 C 编译器；build-plugin.sh 使用 cgo 生成动态库。

Windows amd64：

~~~powershell
pwsh -NoProfile -File .\scripts\build-plugin.ps1 -GOOS windows -GOARCH amd64
pwsh -NoProfile -File .\scripts\install-plugin.ps1 -ArtifactPath .\dist\windows\amd64\command-code.dll -PluginDir C:\path\to\cliproxyapi\plugins
~~~

macOS Apple Silicon：

~~~bash
./scripts/build-plugin.sh darwin arm64
./scripts/install-plugin.sh \
  --artifact dist/darwin/arm64/command-code.dylib \
  --plugin-dir /path/to/cliproxyapi/plugins
~~~

手动替换动态库前应停止 CLIProxyAPI，替换完成后再启动；Windows 尤其不能在 DLL 仍被进程占用时覆盖文件。

## 不重启宿主，刷新模型列表

插件已经加载后，通过 CLIProxyAPI 管理 API 的插件配置 PATCH 触发刷新：

~~~bash
curl -fsS -X PATCH \
  -H 'Authorization: Bearer <management-key>' \
  -H 'Content-Type: application/json' \
  http://127.0.0.1:8080/v0/management/plugins/command-code/config \
  -d '{"api-key":"user_..."}'
~~~

说明：

- Authorization 中填写 CLIProxyAPI 的 management key；
- JSON 中填写 Command Code 的 user_... API key；
- PATCH 只更新请求中提供的配置字段，不会清空其他插件配置；
- 如果 API key 没有变化，也可以再次提交当前值来主动触发刷新。

宿主保存配置后会调用 plugin.reconfigure。插件成功获取目录后，会原子替换完整模型快照：新增模型会出现，上游已删除的模型会消失。上游超时、错误、畸形响应或空目录不会清空最后一次有效目录。

## 离线安装

如果已经有构建好的动态库，可以只使用安装脚本，不需要联网下载 Release：

~~~bash
./scripts/install-plugin.sh \
  --artifact /path/to/command-code.so \
  --plugin-dir /path/to/cliproxyapi/plugins
~~~

Windows：

~~~powershell
pwsh -NoProfile -File .\scripts\install-plugin.ps1 -ArtifactPath .\dist\windows\amd64\command-code.dll -PluginDir C:\path\to\cliproxyapi\plugins
~~~

离线安装脚本只复制指定动态库，不会下载、校验 Release，也不会重启宿主。替换完成后必须重启 CLIProxyAPI。

## 常见问题

### 安装器提示无法找到唯一插件目录

显式传入宿主实际使用的插件目录：

~~~bash
curl -fsSL https://raw.githubusercontent.com/ouones/cliproxyapi-plugin/master/scripts/install-online.sh \
  | sudo bash -s -- --plugin-dir /absolute/path/to/plugins
~~~

### 安装成功但模型没有出现

确认以下事项：

1. 已重启 CLIProxyAPI；
2. plugins.enabled 为 true；
3. 动态库位于配置中的 plugins.dir；
4. API key 以 user_ 开头；
5. 动态库平台和宿主架构匹配。

### 模型列表没有更新

重新提交上面的 PATCH 请求，并确认使用的是 CLIProxyAPI management key。刷新失败时插件会保留旧目录，不会用空目录覆盖现有模型；这时检查宿主网络、API key 和上游服务后再重试。

### 一键安装器提示不支持当前平台

在线安装器只提供 Linux amd64 Release。Windows、macOS 或其他架构请从源码构建对应动态库，再使用离线安装脚本替换。

## 开发者验证

在 Linux 或 Ubuntu-24.04 WSL2 中从源码运行测试和静态检查：

~~~bash
go test ./...
go vet ./...
~~~

构建 Linux amd64 插件：

~~~bash
./scripts/build-plugin.sh linux amd64
~~~

兼容性定义见 [compatibility.json](compatibility.json)。插件开发参考：[CLIProxyAPI Plugin Development](https://help.router-for.me/plugin/development)、[Model Registrar](https://help.router-for.me/plugin/model-registrar)、[Host Callbacks](https://help.router-for.me/plugin/host-callbacks)。
