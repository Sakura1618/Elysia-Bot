# plugin-development-main-path

这页把当前仓库里与 repo-local 插件开发主路径、成熟度边界、manifest 真值规则、验证口径相关的事实收口到一个地方。

当前真值以这些 repo-owned 文件为准：`README.md`、`docs/roadmap/README.md`、`plugins/plugin-template-smoke/README.md`、`package.json`、`.github/workflows/nightly-validation.yml`。

权限声明、config schema、可观测性与 publish 约定，统一见 [`plugin-development-conventions`](./plugin-development-conventions.md)。

## 1. 当前主路径事实

- 当前默认的插件开发路线，是仓库内、repo-local 的最小闭环，不是面向仓库外分发的生态入口。
- `plugins/plugin-template-smoke` 是当前最小插件模板，`packages/plugin-sdk/cmd/plugin-dev` 是这条路径对应的命令入口，`package.json` 已把它们收口成 `npm run plugin:*` 命令。
- 当前主路径按这个顺序走：
  1. `npm run plugin:scaffold -- -id plugin-example`
  2. 修改新插件目录里的 `Manifest()` 与业务逻辑
  3. 如果 `Manifest()` 有变化，运行 `npm run plugin:manifest:write -- -plugin ./plugins/plugin-example`
  4. `npm run plugin:smoke -- -plugin ./plugins/plugin-example`
- 这条路径对应的是仓库内新插件从脚手架、源码改动、manifest 生成与校验、轻量打包到最小验证的主链，不要求先接入远程发布、远程宿主或更宽生态接口。

## 2. `Manifest()` 是源码真值

- 当前规则很明确，`Manifest()` 是源码真值，`manifest.json` 是生成产物。
- `npm run plugin:manifest:write -- -plugin <dir>` 的作用，是把源码里的 `Manifest()` 写成插件目录下的 `manifest.json`。
- `npm run plugin:manifest:check -- -plugin <dir>` 的作用，是校验已提交的 `manifest.json` 仍然没有偏离 `Manifest()`。
- 这意味着 `manifest.json` 不应再被当成手工维护的配置真值；日常改动先改 `Manifest()`，再生成、再校验。

## 3. 当前成熟度分类

### Main path

- repo-local `plugin:scaffold -> 改 Manifest()/逻辑 -> 必要时 plugin:manifest:write -> plugin:smoke`，是当前默认插件开发主路径。
- `plugin-template-smoke`、`plugin-dev` 命令面、`npm run test:plugin-template:smoke`，以及通过 `npm run test:plugin:echo` 走同一条 smoke 命令的 `plugin-echo`，一起构成当前仓库对这条路径的默认支持。

### Smoke / non-default

- 模板 smoke 当前既用进程内 `DirectPluginHost` 证明 runtime register / dispatch / reply 合约成立，也通过 `runtime_smoke_test.go` 覆盖 subprocess host 的 round-trip / health / invalid-handshake fail-closed 边界；但这不等于完整 subprocess host 生命周期已经成为插件开发默认入口。
- 新插件如果要继续接入 runtime、`tests/e2e` 或更深的 repo wiring，可以按需要补注册、补 import、补 `go.mod` wiring；这是已存在的扩展路径，但不是 day-one 默认流。
- `plugin:package` 生成的 `dist/` 与最小 `publish` 元数据，当前应理解为 repo-local 轻量打包边界，而不是远程分发产品面。

### Not now

- 当前不把这条文档写成插件市场路线图。
- 当前不把 remote plugin runtime、remote host 正式产品化写成已承诺主线。
- 当前不把面向广泛第三方作者的插件生态支持，或对现有外部插件生态的兼容，写成当前默认能力面。

## 4. 当前验证口径

- 本地开发时，最直接的命令链是：必要时先跑 `plugin:manifest:write`，再跑 `plugin:smoke`；后者会顺序执行 `manifest check`、`plugin:package` 与插件目录内的 `go test`。
- 模板与主路径回归时，优先跑 `npm run test:plugin-template:smoke`。真实 repo-owned 非模板插件的第一条同路径验证，是 `npm run test:plugin:echo`。
- nightly evidence 也已经存在。`.github/workflows/nightly-validation.yml` 里的 `plugin-matrix` 每晚会跑 `plugin-echo`、`plugin-admin`、`plugin-ai-chat`、`plugin-scheduler`、`plugin-workflow-demo`、`plugin-template-smoke` 这些 repo-owned 插件验证命令。
- 因此，当前插件开发主路径不是只有 README 文字说明，它已经有本地命令面和 nightly plugin-matrix 两层验证证据。

## 5. 当前非目标

- 这份 topic doc 不把当前仓库描述成插件市场。
- 它不把 repo-local 模板、manifest 生成、轻量 package 叙述成远程插件运行时或远程发布服务。
- 它不把当前仓库包装成广泛第三方插件生态平台。
- 它也不把为了未来生态可能需要的更大抽象，提前写成现在的默认插件开发契约。
