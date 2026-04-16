# plugin-template-smoke

`plugin-template-smoke` 是当前仓库为新插件开发者保留的最小复制入口。

它不是脚手架 CLI，也不是完整第三方插件 SDK；它只负责把当前真实边界收口成一条可复制、可注册、可运行、可测试的 smoke 路径。

## 最小复制入口

建议从以下四个文件开始复制：

- `plugin.go`
- `plugin_test.go`
- `template_test.go`
- `manifest.json`

如果你要在当前 monorepo 内新建插件目录，还需要同步调整 [`go.mod`](plugins/plugin-template-smoke/go.mod)。

## 复制后优先改哪些常量

先改 [`plugin.go`](plugins/plugin-template-smoke/plugin.go) 顶部这四个常量：

- `TemplatePluginID`
- `TemplatePluginName`
- `TemplatePluginModule`
- `TemplatePluginSymbol`

这样可以让 [`New()`](plugins/plugin-template-smoke/plugin.go)、[`Definition()`](plugins/plugin-template-smoke/plugin.go) 暴露的 manifest，以及 [`TestTemplateManifestConstantsStayInSync()`](plugins/plugin-template-smoke/template_test.go) 对静态 `manifest.json` 的关键 developer-entry 字段校验保持同步。

当前模板里的代码 manifest 与静态 `manifest.json` 也都会展示一个最小 `publish` 元数据块，用来声明发布来源与 runtime 兼容范围。

当前最小插件自有状态路径，也先只支持像 `plugin-echo` 这样把**持久化配置**作为 plugin-owned input/state seed 暴露到 `/api/console` 读模型；它应与 runtime/operator-owned enabled overlay、以及 runtime-owned status snapshot 明确区分，而不是把它们混成一个通用插件状态系统。

## 当前真实开发入口

最小创建与验证路径是：

1. 复制 `plugins/plugin-template-smoke` 到新的插件目录。
2. 修改 [`plugin.go`](plugins/plugin-template-smoke/plugin.go)、[`manifest.json`](plugins/plugin-template-smoke/manifest.json)、[`go.mod`](plugins/plugin-template-smoke/go.mod) 与测试文件中的插件标识。
3. 把新模块接入 [`go.work`](go.work)；如果要被 [`tests/e2e`](tests/e2e) 直接 import，再补 [`tests/e2e/go.mod`](tests/e2e/go.mod) 的 `require/replace`。
4. 运行插件单测与 e2e smoke，确认 runtime 注册与事件分发链路仍成立。

## 聚焦验证命令

开发模板入口时，优先跑这一条：

```bash
go test ./plugins/plugin-template-smoke ./tests/e2e -run "TestPluginTemplateSmoke|TestTemplateManifestConstantsStayInSync"
```

这条命令覆盖：

- 静态 `manifest.json` 是否仍与 `plugin.go` / `Definition().Manifest` 的关键 developer-entry 字段一致
- 事件型插件最小回复路径
- 缺失 reply handle 的坏输入
- reply service 错误冒泡
- runtime create -> register -> dispatch -> reply 的最小 smoke 链

## 已知限制

- 当前模板只覆盖 `OnEvent(...)` 事件型插件入口，不直接生成 `OnJob(...)`、`OnSchedule(...)`、`OnCommand(...)` 多入口样板。
- [`manifest.json`](plugins/plugin-template-smoke/manifest.json) 仍是独立静态文件；当前由 [`TestTemplateManifestConstantsStayInSync()`](plugins/plugin-template-smoke/template_test.go) 把 [`plugin.go`](plugins/plugin-template-smoke/plugin.go) / `Definition().Manifest` 作为关键 developer-entry 字段的真值来源，但这不是新的 CLI、脚手架或完整逐字段生成器。
- 当前 smoke 使用 [`DirectPluginHost`](packages/runtime-core/runtime.go:899) 做进程内验证，证明的是 runtime 注册与 dispatch 合约，不是完整 subprocess host 生命周期。
