# plugin-template-smoke

`plugin-template-smoke` 是当前仓库为新插件开发者保留的最小 repo-local 插件模板。

它现在配合 `packages/plugin-sdk/cmd/plugin-dev` 作为当前仓库内的最小脚手架 / manifest 生成 / 本地打包入口，而不是继续依赖手工复制后再手工维护 `manifest.json`。

repo 级插件开发主路径、成熟度分类、验证口径与非目标，统一见 `docs/topics/plugin-development-main-path.md`。权限声明、config schema、可观测性与 publish 约定，统一见 `docs/topics/plugin-development-conventions.md`。这份 README 只保留模板本身的复制、生成、打包与 smoke 细节。

## 最小复制入口

当前不再推荐手工复制目录。优先走 repo-local 脚手架：

```bash
npm run plugin:scaffold -- -id plugin-example
```

这条命令会：

- 从 `plugins/plugin-template-smoke` 生成新的 repo 内插件目录
- 按新插件 ID / 名称重写 `plugin.go`、测试与 `go.mod`
- 自动把新插件路径写进仓库根目录下的 `go.work`
- 生成初始 `manifest.json`

## 复制后优先改哪些常量

生成后的插件目录里，先改 `plugin.go` 顶部这四个常量：

- `TemplatePluginID`
- `TemplatePluginName`
- `TemplatePluginModule`
- `TemplatePluginSymbol`

这样可以让 `Manifest()` 继续作为源码真值，并让 `New()`、`Definition()` 与 `manifest_test.go` 对生成产物的校验保持同步。

当前模板里的 `Manifest()` 与生成出来的 `manifest.json` 都会展示一个最小 `publish` 元数据块，用来声明发布来源与 runtime 兼容范围。

当前最小插件自有状态路径，也先只支持像 `plugin-echo` 这样把**持久化配置**作为 plugin-owned input/state seed 暴露到 `/api/console` 读模型；它应与 runtime/operator-owned enabled overlay、以及 runtime-owned status snapshot 明确区分，而不是把它们混成一个通用插件状态系统。

## 当前真实开发入口

最小创建与验证路径是：

1. 运行 `npm run plugin:scaffold -- -id plugin-example` 生成新的 repo-local 插件目录。
2. 修改新插件目录里的 `plugin.go` 业务逻辑与 `Manifest()`。
3. 如果 `Manifest()` 有变化，运行 `npm run plugin:manifest:write -- -plugin ./plugins/plugin-example` 刷新 `manifest.json`。
4. 运行 `npm run plugin:smoke -- -plugin ./plugins/plugin-example`；这条命令会顺序执行 `manifest check -> package -> go test`。如果要把新插件接入 runtime 或 `tests/e2e` 直接 import，再按实际需要补注册 / `go.mod` wiring。

`manifest.json` 不再是手工真值来源；它现在是 `Manifest()` 的生成物。

## manifest / package / smoke 命令

```bash
npm run plugin:manifest:write -- -plugin ./plugins/plugin-template-smoke
npm run plugin:manifest:check -- -plugin ./plugins/plugin-template-smoke
npm run plugin:package -- -plugin ./plugins/plugin-template-smoke
npm run plugin:smoke -- -plugin ./plugins/plugin-template-smoke
```

- `manifest write`：把 `Manifest()` 生成到当前插件目录下的 `manifest.json`
- `manifest check`：校验已提交的 `manifest.json` 仍与 `Manifest()` 一致
- `package`：生成一个轻量 repo-local `dist/` 目录，包含生成后的 `manifest.json` 与最小 companion artifact（当前是 `README.md`）
- `smoke`：顺序执行 `manifest check -> package -> go test`，用同一条命令验证 repo-local 插件开发闭环

## 聚焦验证命令

开发模板入口时，优先跑这一条：

```bash
npm run test:plugin-template:smoke
```

对第一个真实非模板插件路径，当前参考命令是：

```bash
npm run plugin:smoke -- -plugin ./plugins/plugin-echo
```

这条命令覆盖：

- `Manifest()` 生成的 `manifest.json` 是否仍与仓库内已提交产物一致
- 事件型插件最小回复路径
- 缺失 reply handle 的坏输入
- reply service 错误冒泡
- 当前第一个真实 repo-owned 非模板 smoke / reference path，说明同一条 `plugin:smoke` 命令也已用于 `plugin-echo` 这类真实插件

## 已知限制

- 当前模板只覆盖 `OnEvent(...)` 事件型插件入口，不直接生成 `OnJob(...)`、`OnSchedule(...)`、`OnCommand(...)` 多入口样板。
- 当前脚手架只覆盖 repo 内插件目录生成、manifest 生成/check 与轻量本地 `dist/` 输出；它不是插件市场，也不是远程发布服务。
- 当前 smoke 仍是聚焦模板 gate：既覆盖 runtime 的 `DirectPluginHost` 进程内注册 / dispatch / reply 合约，也覆盖 subprocess host 的 round-trip、health check 与 invalid-handshake fail-closed 边界；但这还不等于完整 subprocess host 生命周期已经成为插件开发默认入口。
