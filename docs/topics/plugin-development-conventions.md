# plugin-development-conventions

这页收口当前仓库里 repo-local 插件作者需要跟随的开发约定。它不重复主路径命令顺序，而是聚焦 `Manifest()` 里那些现在已经有代码边界、有验证口径、也最容易写偏的约束。

当前真值以这些 repo-owned 文件为准：`docs/topics/plugin-development-main-path.md`、`plugins/plugin-template-smoke/README.md`、`packages/plugin-sdk/plugin.go`、`plugins/plugin-template-smoke/plugin.go`、`plugins/plugin-echo/plugin.go`、`plugins/plugin-admin/plugin.go`、`plugins/plugin-ai-chat/plugin.go`、`plugins/plugin-scheduler/plugin.go`、`plugins/plugin-workflow-demo/plugin.go`。

## 1. 权限声明

- 权限声明写在 `Manifest().Permissions`，它描述的是插件向 runtime 声明自己需要哪些能力，不是给某个 operator 或 actor 直接发放权限。
- 当前 repo-owned 插件的权限写法，已经覆盖了几类最小能力面：
  - `reply:send`，见 `plugin-template-smoke`、`plugin-echo`、`plugin-ai-chat`、`plugin-scheduler`、`plugin-workflow-demo`
  - `job:enqueue`、`job:run`、`session:write`，见 `plugin-ai-chat`
  - `schedule:manage`，见 `plugin-scheduler`
  - `plugin:enable`、`plugin:disable`、`plugin:replay`、`plugin:rollout`，见 `plugin-admin`
- `packages/plugin-sdk/plugin.go` 当前会校验 manifest permission 格式，repo 里的权限字符串都按 `resource:action` 形状声明。
- 这层 permission 是插件 manifest contract。runtime/operator RBAC 仍然是另一层边界，真值在 runtime 的 operator identity、`rbac.actor_roles` 与 `rbac.policies`。`plugin-admin` 就是当前最直接的例子，它虽然在 manifest 里声明了 `plugin:*` 能力，但执行 enable、disable、replay、rollout 时仍会按当前 actor 和 target 再走一次 runtime 授权。
- 对 repo-local 新插件来说，最稳妥的做法，是只声明当前实现真的会用到的 runtime 能力，不把未来可能需要的 operator 权限提前写进 `Manifest()`。

## 2. Config schema

- `Manifest()` 仍是源码真值，`manifest.json` 只是 `npm run plugin:manifest:write` 生成出来的产物。改 config contract 时，先改 `Manifest()`；如果 manifest 变了，先生成，再用 `plugin:smoke` 做默认闭环验证。
- 当前 runtime 对 `ConfigSchema` 的预期，是 object-shaped schema。顶层 `type` 应是 `"object"`，并用 `properties` 描述字段；有必填项时，用 `required` 数组声明属性名。
- 当前 repo-owned 例子里，`plugin-template-smoke` 与 `plugin-echo` 都把配置写成一个对象，里面有 `prefix` 这个 string 属性。没有自有配置的插件，可以像 `plugin-admin`、`plugin-scheduler`、`plugin-workflow-demo` 那样省略 `ConfigSchema`。
- 当前边界不是完整 JSON Schema 平台。runtime 现在围绕 object、property、required、type、default、enum 这一小段结构做兼容性预检，重点是让 manifest 与 instance config 在进入 subprocess dispatch 前就失败。
- 这条 fail-fast 边界已经被当前测试钉住了。如果顶层 schema 不是 object，或者实例配置不满足声明类型与必填约束，dispatch 会在 subprocess 副作用、reply callback、stdout/stderr 输出之前被拒绝。
- 对 repo-local 插件作者来说，最安全的做法，是把 `ConfigSchema` 维持成清晰的对象输入契约，不把 runtime-owned enabled overlay、status snapshot，或别的运行态事实混进插件自有配置。

## 3. 可观测性

- 当前 repo 里插件相关的主 ID，已经收口成 `trace_id`、`event_id`、`run_id`、`correlation_id`、`plugin_id` 这组字段。新插件不需要再发明第二套主 ID 名称。
- 这组 ID 今天已经出现在几条 repo-owned 证据链里：
  - reply metadata。runtime dispatch 与 `plugin-ai-chat`、`plugin-workflow-demo` 都会把这组字段带进 reply handle metadata。
  - audit evidence。`plugin-admin` 记录 audit 时，会把 execution context 里的 `TraceID`、`EventID`、`RunID`、`CorrelationID`、`PluginID` 落到 audit entry，`/api/console` 今天也会把这些 audit 事实投影出来。
  - plugin read model。`/api/console` 的 plugin 列表今天会暴露 manifest 权限、`configSchema`、publish 元数据、persisted config、enabled overlay、status snapshot、dispatch result evidence 等事实；`plugin-echo` 的持久化配置就是当前最小 plugin-owned state seed 例子。
  - runtime trace / log evidence。当前测试已经要求同一条插件链路在 dispatch、plugin invoke、reply send、subprocess failure 等位置保持同源 `trace_id`、`event_id`、`plugin_id` 证据。
- 这意味着 repo-local 插件作者当前应该优先复用 runtime 已给出的 execution context 与 reply metadata，不把 observability 写成插件私有、外部看不见的字段堆。
- 如果插件需要补自己的业务 metadata，应该在不覆盖这组 repo-owned ID 的前提下追加。

## 4. Publish 约束

- manifest 里的 `Publish` 块，当前只承担 repo-local provenance 与 compatibility metadata，不是插件市场、远程发布服务，或外部分发 contract。
- 这块现在至少包含三个字段：
  - `sourceType`，发布来源类型。SDK 当前定义了 `git` 与 `archive`，repo-owned 插件现在都使用 `git`。
  - `sourceUri`，发布来源 URI。当前 repo-owned 插件都指向本仓库里对应插件目录的 GitHub tree 地址。
  - `runtimeVersionRange`，当前插件声明自己兼容的 runtime 版本区间，例如 `>=0.1.0 <1.0.0`。
- `packages/plugin-sdk/plugin.go` 当前会对这块做 manifest 校验，缺字段、无效 URI，或无效 version range 都会直接失败。
- `plugin:package` 生成的 `dist/` 与 `/api/console` 的 plugin projection，今天都会把这块 metadata 带出来，所以它已经是 repo 内可见事实；但它还不等于一个远程安装、拉取、签名或 marketplace publish 协议。
- 对 repo-local 插件作者来说，写这块 metadata 的目标，是说明“这份插件代码来自哪里，按什么 runtime version range 被当前仓库接受”，不是为未来外部生态提前设计发布协议。

## 5. 本地验证

- 当前最小验证顺序不变：
  1. `npm run plugin:scaffold -- -id plugin-example`
  2. 修改 `Manifest()` 与插件逻辑
  3. 如果 manifest 变了，跑 `npm run plugin:manifest:write -- -plugin ./plugins/plugin-example`
  4. 跑 `npm run plugin:smoke -- -plugin ./plugins/plugin-example`
- `plugin:smoke` 当前会顺序执行 `manifest check`、`plugin:package` 与插件目录内的 `go test`，是 repo-local 插件开发闭环的最短回归命令。
- `plugin:manifest:check` 仍然可用于单独确认已提交的 `manifest.json` 没有偏离源码真值，但它不是当前默认闭环里的独立必跑步骤。
- 模板与脚手架回归，优先看 `npm run test:plugin-template:smoke`。真实非模板最小参考路径，优先看 `npm run test:plugin:echo`。
- repo 内 nightly 也已经有 `plugin-matrix`，会跑 `plugin-echo`、`plugin-admin`、`plugin-ai-chat`、`plugin-scheduler`、`plugin-workflow-demo`、`plugin-template-smoke`，所以本地验证与 nightly 证据是一套连续口径。

## 6. 非目标

- 这份约定不把当前仓库写成插件市场。
- 它不把 `Publish` 元数据扩写成 remote runtime、remote host、remote install、marketplace listing，或外部分发 contract。
- 它不把当前 `ConfigSchema` 边界描述成完整 JSON Schema 实现。
- 它不把 plugin-owned persisted config、runtime-owned enabled overlay、runtime-owned status snapshot 混成一个通用状态模型。
- 它不承诺对广泛第三方作者、外部插件生态，或远期多宿主发布链路的完整支持。
