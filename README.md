# bot-platform

一个面向长期运行、故障恢复与可观测性的机器人运行平台。

## 当前状态

基于仓库内现有代码、测试与文档证据，当前更准确的状态是：**Stage 0、Stage 1 已完成；Stage 2、Stage 3、Stage 4 已落地最小版；Stage 5 部分完成。**

- **已完成**
  - **Stage 0：框架骨架**。统一事件模型、插件契约、runtime 接口、日志/配置与 ADR 骨架均已有代码、测试与文档证据。
  - **Stage 1：最小闭环**。OneBot 入站、runtime dispatch、ReplyHandle、plugin-echo、SQLite state 与首条 E2E 已形成完整闭环。
- **v0 已落地**
  - **Stage 2：可靠执行**。Job、Job Queue、Scheduler、subprocess plugin-host、plugin-scheduler 与 fault injection 已有最小实现和测试；其中 job / schedule 恢复、SQLite round-trip、日志 / metrics / trace 的最小出口已可验证，但整体仍不是独立完整的 worker / 调度服务。
  - **Stage 3：可运维**。Metrics、Tracing、Console API、Console Web、plugin-admin 已落地；其中 Console Web 已接 live Console API，但整体仍是只读 v0。
  - **Stage 4：业务验证**。adapter-webhook、plugin-ai-chat、workflow-engine、plugin-workflow-demo、Postgres migration 已有最小实现；其中 Postgres 仍只是最小 schema 与访问层，不应表述为稳定主存储能力。
- **部分实现**
  - **Stage 5：生产化**。audit、replay、secrets、rollout、最小 RBAC 已有局部边界实现与测试；这些能力仍是局部收口，不是统一全局策略系统。

### 状态说明

- 仓库已经具备多条可运行主链路，但不少能力仍属于 **v0 / 最小版 / 局部雏形 / 只读能力**。
- README 中的“已落地最小版”仅表示已有代码、测试与文档证据，不等同于“完整支持”或“生产完成”。
- 当前更准确的表述是：**仓库已经具备可开发官方/业务插件的最小平台能力**，但**还不是成熟插件生态，也不是完整插件平台**。
- 当前 `Console Web` 是 live API 驱动的只读面板，不是完整控制台。
- 当前 `RBAC`、`audit / replay / secrets / rollout`、transport / client fault injection 都已有局部边界，但都不应描述为完整统一系统。

## 目录结构

```text
apps/
adapters/
deploy/
docs/
packages/
plugins/
schemas/
tests/
```

## 本地开发

```bash
npm install
npm run lint
npm run test
npm run format
npm run dev
npm run dev:runtime
```

当前根目录 `package.json` 的顶层脚本含义：

- `npm run dev`：代理到 `apps/console-web`
- `npm run dev:runtime`：启动默认 runtime 开发入口，读取 `deploy/config.dev.yaml`
- `npm run test:plugin-template:smoke`：运行模板复制 / 改名 / 注册 smoke 路径的聚焦验证
- `npm run test`：运行当前 `go.work` 已接入模块的 Go 测试
- `npm run lint`：执行当前仓库 Go 源码的 `gofmt -l` 检查
- `npm run format`：执行当前仓库 Go 源码的 `gofmt -w`

它们仍不是“全仓统一脚本”，当前不代表已覆盖 repo-wide JS/TS lint、前端测试或所有未来工作区类型。

## 默认 runtime 开发入口

```bash
npm run dev:runtime
```

默认读取 `deploy/config.dev.yaml`，并暴露以下最小路径：

- `GET /healthz`
- `GET /api/console`
- `GET /metrics`
- `POST /demo/onebot/message`
- `POST /demo/ai/message`
- `POST /demo/jobs/enqueue`
- `POST /demo/jobs/timeout?id=<job-id>`
- `POST /demo/schedules/echo-delay`
- `GET /demo/replies`
- `GET /demo/state/counts`

当前入口已经默认接入：

- SQLite 状态存储
- event journal 与 idempotency key 记录
- 开发态 JobQueue
- 最小 in-process Scheduler runner
- 最小 AI job 演示链路

这些入口用于验证“插件可开发 + 异步执行可验证 + 故障链可观测”的当前边界，不代表已具备完整独立 worker、完整调度服务或真实外部 provider 集成。

## 当前插件能力边界

就当前仓库状态而言，更准确的插件能力描述是：

- 已具备最小官方/业务插件开发能力
- 已具备最小插件运行、失败排障、恢复验证与只读运行面能力
- 已有一批官方样板插件，可作为参考实现

但当前仍不应表述为：

- 成熟插件生态
- 开放第三方插件平台
- 完整插件控制台
- 完整远程 plugin runtime
- 完整独立 worker / 调度 / 控制面系统

Q6-1 ~ Q6-73-3 之后，插件运行面已继续沿最小边界推进 subprocess 实例配置：

- subprocess bootstrap 失败会分类为 `start` / `handshake`
- 握手成功后立即崩溃会分类为 `failure_stage=dispatch` + `failure_reason=crash_after_handshake`
- manifest compatibility preflight 拒绝会分类为 `failure_stage=compatibility` + `failure_reason=manifest_mode_mismatch|manifest_unsupported_api_version|manifest_missing_entry_target|manifest_invalid_config_schema|manifest_missing_required_config|manifest_invalid_config_property`
- 当前实例配置链路已有八条 launch-before-reject failure chain：
  - top-level value type mismatch：`failure_reason=instance_config_value_type_mismatch` + `compatibility_rule=instance_config_top_level_value_type`
  - top-level required missing：`failure_reason=instance_config_missing_required_value` + `compatibility_rule=instance_config_top_level_required`
  - **第一条 nested typed value type mismatch**：当 manifest 声明 `settings.type="object"`、`settings.properties.prefix.type="string"`，且实例配置显式给出 `settings.prefix=true` 时，会以 `failure_reason=instance_config_value_type_mismatch` + `compatibility_rule=instance_config_nested_value_type` 在实例配置装载阶段失败
  - **第一条 deeper nested typed value type mismatch**：当 manifest 声明 `settings.type="object"`、`settings.labels.type="object"`、`settings.labels.prefix.type="string"`，且实例配置显式给出 `settings.labels.prefix=true` 时，会以 `failure_reason=instance_config_value_type_mismatch` + `compatibility_rule=instance_config_deeper_nested_value_type` 在实例配置装载阶段失败
  - **第一条 deeper nested array actual value type mismatch**：当 manifest 声明 `settings.type="object"`、`settings.labels.type="object"`、`settings.labels.prefix.type="string"`，且实例配置显式给出 `settings.labels.prefix=["oops"]` 时，也会继续以 `failure_reason=instance_config_value_type_mismatch` + `compatibility_rule=instance_config_deeper_nested_value_type` 在实例配置装载阶段失败，只是 `actual_type=array`
  - **第一条 deeper nested object actual value type mismatch**：当 manifest 声明 `settings.type="object"`、`settings.labels.type="object"`、`settings.labels.prefix.type="string"`，且实例配置显式给出 `settings.labels.prefix={"bad":true}` 时，也会继续以 `failure_reason=instance_config_value_type_mismatch` + `compatibility_rule=instance_config_deeper_nested_value_type` 在实例配置装载阶段失败，只是 `actual_type=object`
  - **第一条 deeper nested required missing**：当 manifest 声明 `settings.type="object"`、`settings.labels.type="object"`、`settings.labels.required=["prefix"]`、`settings.labels.prefix.type="string"`，且实例配置显式给出 `settings.labels={}` 时，会以 `failure_reason=instance_config_missing_required_value` + `compatibility_rule=instance_config_deeper_nested_required` 在实例配置装载阶段失败
  - **第一条 deeper nested enum out-of-set**：当 manifest 声明 `settings.type="object"`、`settings.labels.type="object"`、`settings.labels.prefix.type="string"`、`settings.labels.prefix.enum=["hello","world"]`，且实例配置显式给出 `settings.labels.prefix="oops"` 时，会以 `failure_reason=instance_config_enum_value_out_of_set` + `compatibility_rule=instance_config_deeper_nested_enum` 在实例配置装载阶段失败

当前还已经明确确认：

- top-level manifest `default` 在当前实例配置装载阶段不会自动 merge / 注入到 `hostRequest.instance_config` 中
- top-level manifest `enum` 当前不会校验显式实例值是否属于枚举成员
- nested object 下声明的 nested manifest `default` 当前不会自动注入缺失的 nested 实例值
- nested object 下声明的 nested manifest `enum` 当前不会校验显式 nested 实例值是否属于枚举成员
- nested object 下声明的 `required=["prefix"]` 在实例配置显式给出 `settings={}` 时当前仍不会触发 launch-before-reject
- deeper nested `settings.labels.prefix.default="hello"` + `InstanceConfig.settings.labels={}` 当前仍不会在实例配置装载阶段自动注入，也不会产生新的 caller-facing reject；request / built fixture / runtime 证据都会继续看到空的 `labels={}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.prefix.type="string"` + `InstanceConfig.settings.labels.naming.prefix=true` 当前不会在实例配置装载阶段继续下钻校验；request / built fixture / runtime 证据都会继续看到原始坏值 `{"settings":{"labels":{"naming":{"prefix":true}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.prefix.type="string"` + `InstanceConfig.settings.labels.naming.prefix=["oops"]` 当前同样不会在实例配置装载阶段递归命中 deeper array-valued actual mismatch；request / built fixture / runtime 证据都会继续看到原始数组值 `{"settings":{"labels":{"naming":{"prefix":["oops"]}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.prefix.type="string"` + `InstanceConfig.settings.labels.naming.prefix={"bad":true}` 当前同样不会在实例配置装载阶段递归命中 deeper object-valued actual mismatch；request / built fixture / runtime 证据都会继续看到原始对象值 `{"settings":{"labels":{"naming":{"prefix":{"bad":true}}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.type="object"` + `InstanceConfig.settings.labels.naming=true` 当前也不会在实例配置装载阶段拒绝 object-typed 节点自身 actual type mismatch；request / built fixture / runtime 证据都会继续看到原始坏值 `{"settings":{"labels":{"naming":true}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `InstanceConfig.settings.labels.naming={}` 当前同样不会在实例配置装载阶段递归命中 deeper `required` missing；request / built fixture / runtime 证据都会继续看到原始空 object `{"settings":{"labels":{"naming":{}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.prefix.enum=["hello","world"]` + `InstanceConfig.settings.labels.naming.prefix="oops"` 当前同样不会在实例配置装载阶段递归命中 deeper `enum` out-of-set；request / built fixture / runtime 证据都会继续看到原始 out-of-set 值 `{"settings":{"labels":{"naming":{"prefix":"oops"}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.prefix.default="hello"` + `InstanceConfig.settings.labels.naming={}` 当前同样不会在实例配置装载阶段递归命中 deeper `default` only，也不会注入 `prefix="hello"`；request / built fixture / runtime 证据都会继续看到原始空 object `{"settings":{"labels":{"naming":{}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.default="hello"` + `InstanceConfig.settings.labels.naming={}` 当前同样既不会在实例配置装载阶段递归命中 deeper `required`，也不会读取 deeper `default` 注入 `prefix="hello"`；request / built fixture / runtime 证据都会继续看到原始空 object `{"settings":{"labels":{"naming":{}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `InstanceConfig.settings.labels.naming={}` 当前同样既不会在实例配置装载阶段递归命中 deeper `required`，也不会进入 deeper `enum` gate；request / built fixture / runtime 证据都会继续看到原始空 object `{"settings":{"labels":{"naming":{}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming={}` 当前同样既不会在实例配置装载阶段递归命中 deeper `enum` gate，也不会读取 deeper `default` 注入 `prefix="hello"`；request / built fixture / runtime 证据都会继续看到原始空 object `{"settings":{"labels":{"naming":{}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming={}` 当前同样既不会在实例配置装载阶段递归命中 deeper `required` / `enum` gate，也不会读取 deeper `default` 注入 `prefix="hello"`；request / built fixture / runtime 证据都会继续看到原始空 object `{"settings":{"labels":{"naming":{}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels={}` 当前同样既不会在实例配置装载阶段递归命中 `naming.required` / `enum` gate，也不会读取 deeper `default` 注入 `prefix="hello"`；request / built fixture / runtime 证据都会继续看到原始 child omission payload `{"settings":{"labels":{}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings={}` 当前同样既不会在实例配置装载阶段创建中间 `labels` object，也不会递归命中 `labels.naming.required` / `enum` gate，或读取 deeper `default` 注入 `prefix="hello"`；request / built fixture / runtime 证据都会继续看到原始 parent omission payload `{"settings":{}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig={}` 当前同样既不会在实例配置装载阶段创建缺失的 root `settings` object，也不会创建中间 `labels` / `naming` object、递归命中 deeper `required` / `enum` gate，或读取 deeper `default` 注入 `prefix="hello"`；由于 [`marshalSubprocessInstanceConfig()`](packages/runtime-core/plugin_host_subprocess.go:239) 在空 map 上直接返回 `nil`，既有 `event` request / built fixture / runtime 证据与新增的 `command` / `job` / `schedule` request / built fixture 证据都会看到 `instance_config` 字段整体缺失并继续正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig=nil` 当前与 `InstanceConfig={}` 保持同样的 transport 行为：实例配置装载阶段同样不会创建缺失的 root `settings` object 或中间 `labels` / `naming` object、不会递归命中 deeper `required` / `enum` gate，或读取 deeper `default` 注入 `prefix="hello"`；由于 Go 的 nil map 在 [`marshalSubprocessInstanceConfig()`](packages/runtime-core/plugin_host_subprocess.go:239) 中同样满足 `len(instanceConfig)==0`，既有 `event` request / built fixture / runtime 证据与新增的 `command` / `job` / `schedule` request / built fixture 证据都会继续看到 `instance_config` 字段整体缺失并正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming.prefix="oops"` 当前同样既不会在实例配置装载阶段递归命中 deeper `required` / `enum` gate，也不会读取 deeper `default` 改写坏值；request / built fixture / runtime 证据都会继续看到原始显式坏值 `{"settings":{"labels":{"naming":{"prefix":"oops"}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming.prefix=["oops"]` 当前同样既不会在实例配置装载阶段递归命中 deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写数组坏值；request / built fixture / runtime 证据都会继续看到原始显式数组值 `{"settings":{"labels":{"naming":{"prefix":["oops"]}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming.prefix=true` 当前同样既不会在实例配置装载阶段递归命中 deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 wrong-type 坏值；request / built fixture / runtime 证据都会继续看到原始显式 wrong-type 值 `{"settings":{"labels":{"naming":{"prefix":true}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming.prefix={"bad":true}` 当前同样既不会在实例配置装载阶段递归命中 deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 object-valued 坏值；request / built fixture / runtime 证据都会继续看到原始显式对象值 `{"settings":{"labels":{"naming":{"prefix":{"bad":true}}}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming=true` 当前同样既不会在实例配置装载阶段递归命中 deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 object-node 坏值；request / built fixture / runtime 证据都会继续看到原始显式 object-node 值 `{"settings":{"labels":{"naming":true}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming="oops"` 当前同样既不会在实例配置装载阶段递归命中 `naming` 节点自身 actual type、deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 string-valued object-node 坏值；request / built fixture / runtime 证据都会继续看到原始显式 string-valued object-node 值 `{"settings":{"labels":{"naming":"oops"}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming=7` 当前同样既不会在实例配置装载阶段递归命中 `naming` 节点自身 actual type、deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 number-valued object-node 坏值；request / built fixture / runtime 证据都会继续看到原始显式 number-valued object-node 值 `{"settings":{"labels":{"naming":7}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming=null` 当前同样既不会在实例配置装载阶段递归命中 `naming` 节点自身 actual type、deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 null-valued object-node 坏值；request / built fixture / runtime 证据都会继续看到原始显式 null-valued object-node 值 `{"settings":{"labels":{"naming":null}}}` 并进入正常 dispatch
- 超出当前 helper 深度的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.type="string"` + `enum=["hello","world"]` + `default="hello"` + `InstanceConfig.settings.labels.naming=["oops"]` 当前同样既不会在实例配置装载阶段递归命中 `naming` 节点自身 actual type、deeper `required` / `type` / `enum` gate，也不会读取 deeper `default` 改写 array-valued object-node 坏值；request / built fixture / runtime 证据都会继续看到原始显式 array-valued object-node 值 `{"settings":{"labels":{"naming":["oops"]}}}` 并进入正常 dispatch

这只是把**Q6-43 / Q6-44 / Q6-45 / Q6-46 / Q6-47 的 deeper nested 局部 reject chain、相邻的 deeper nested default-only 非 reject 边界，以及 Q6-49 / Q6-50 / Q6-51 / Q6-52 / Q6-53 / Q6-54 / Q6-55 / Q6-56 / Q6-57 / Q6-58 / Q6-59 / Q6-60 / Q6-61 / Q6-62 / Q6-63 / Q6-64 / Q6-65 / Q6-66 / Q6-67 / Q6-68 / Q6-69 / Q6-70 / Q6-71 / Q6-72 / Q6-73-1 / Q6-73-2 / Q6-73-3 二十七条超出当前 helper 深度的 typed value mismatch / array-valued actual mismatch / object-valued actual mismatch / object-node mismatch / required missing / enum out-of-set / default-only / mixed `required` + `default` / mixed `enum` + `default` / mixed `required` + `enum` / mixed `required` + `enum` + `default` / 同一 triple schema 显式 bad value / 同一 triple schema 显式 array-valued bad value / 同一 triple schema 显式 wrong-type bad value / 同一 triple schema 显式 object-valued bad value / 同一 triple schema 显式 object-node bad value / 同一 triple schema 显式 array-valued object-node bad value / 同一 triple schema 显式 string-valued object-node bad value / 同一 triple schema 显式 number-valued object-node bad value / 同一 triple schema 显式 null-valued object-node bad value / 同一 triple schema child omission / 同一 triple schema parent omission / 同一 triple schema root omission / 同一 triple schema nil-vs-empty root omission transport 等价性边界，以及同一 root omission 边界在 `command` / `job` / `schedule` requestType 上的最近邻 transport 对照**收口成真实行为，不等于实例配置系统已经支持完整递归校验、任意深度 `required` / `enum` / `default` 协同、任意深度 `type` / `default` / `enum` 校验、任意深度 `required/default`、任意深度 `enum/default`、任意深度 `required/enum/default` 协同或 default merge。

当前推荐验证命令：

```bash
go test ./packages/runtime-core -run "Test(BuildSubprocessHostRequestPreservesBeyondSupportedDeeperNested(TypeMismatch|ArrayValueMismatch|ObjectValueMismatch|ObjectNodeMismatch|RequiredMissing|EnumOutOfSet|RequiredDefaultBoundary|RequiredEnumBoundary|EnumDefaultBoundary|RequiredEnumDefaultBoundary|RequiredEnumDefaultExplicitBadValue|RequiredEnumDefaultExplicitArrayValuedBadValue|RequiredEnumDefaultExplicitWrongTypeBadValue|RequiredEnumDefaultExplicitObjectValuedBadValue|RequiredEnumDefaultExplicitObjectNodeBadValue|RequiredEnumDefaultExplicitArrayValuedObjectNodeBadValue)|BuildSubprocessHostRequestDoesNotInjectBeyondSupportedDeeperNestedManifestDefaultIntoInstanceConfig|BuiltSubprocessFixture(ReceivesBeyondSupportedDeeperNested(TypeMismatch|ArrayValueMismatch|ObjectValueMismatch|ObjectNodeMismatch|RequiredMissing|EnumOutOfSet|RequiredDefaultBoundary|RequiredEnumBoundary|EnumDefaultBoundary|RequiredEnumDefaultBoundary|RequiredEnumDefaultExplicitBadValue|RequiredEnumDefaultExplicitArrayValuedBadValue|RequiredEnumDefaultExplicitWrongTypeBadValue|RequiredEnumDefaultExplicitObjectValuedBadValue|RequiredEnumDefaultExplicitObjectNodeBadValue|RequiredEnumDefaultExplicitArrayValuedObjectNodeBadValue)|DoesNotReceiveInjectedBeyondSupportedDeeperNestedManifestDefault)|RuntimeDispatch(PassesBeyondSupportedDeeperNested(TypeMismatchIntoSubprocessHostRequest|ArrayValueMismatchIntoSubprocessHostRequest|ObjectValueMismatchIntoSubprocessHostRequest|ObjectNodeMismatchIntoSubprocessHostRequest|RequiredMissingIntoSubprocessHostRequest|EnumOutOfSetIntoSubprocessHostRequest|RequiredDefaultBoundaryIntoSubprocessHostRequest|RequiredEnumBoundaryIntoSubprocessHostRequest|EnumDefaultBoundaryIntoSubprocessHostRequest|RequiredEnumDefaultBoundaryIntoSubprocessHostRequest|RequiredEnumDefaultExplicitBadValueIntoSubprocessHostRequest|RequiredEnumDefaultExplicitArrayValuedBadValueIntoSubprocessHostRequest|RequiredEnumDefaultExplicitWrongTypeBadValueIntoSubprocessHostRequest|RequiredEnumDefaultExplicitObjectValuedBadValueIntoSubprocessHostRequest|RequiredEnumDefaultExplicitObjectNodeBadValueIntoSubprocessHostRequest|RequiredEnumDefaultExplicitArrayValuedObjectNodeBadValueIntoSubprocessHostRequest)|DoesNotInjectBeyondSupportedDeeperNestedManifestDefaultIntoSubprocessHostRequest))$" -count=1
npm run test:plugin-template:smoke
```

## 插件开发最小入口

当前仓库内新插件开发与后续任务推进的推荐入口已经收口到：

- `plugins/plugin-template-smoke/README.md`
- `docs/TODO.md`

其中：

- `plugins/plugin-template-smoke` 是当前唯一明确面向“复制 -> 改名 -> 注册 -> smoke”的模板目录。
- `plugins/plugin-template-smoke/plugin.go` 提供最小事件型插件骨架。
- `plugins/plugin-template-smoke/template_test.go` 会读取静态 `manifest.json`，并把 `plugin.go` / `Definition().Manifest` 作为关键 developer-entry 字段的一致性真值来源。
- `tests/e2e/plugin_template_chain_test.go` 提供 create -> register -> runtime dispatch -> reply 的稳定 smoke 证据。

针对这条模板开发入口，当前推荐的聚焦验证命令是：

```bash
npm run test:plugin-template:smoke
```

## 当前已知限制

- 当前 job 恢复只覆盖 `pending / retrying / running` 三类状态的 runtime 启动分类；并未引入独立 worker 租约、执行心跳或更细的 crash replay 语义。
- 当前 schedule 恢复仍聚焦“启动重载 + 缺失 dueAt 的最小重建 + invalid persisted plan 跳过统计”；尚未引入 lease、misfire policy、catch-up/backfill 或更完整调度语义。
- AI job 当前仍是单进程开发态触发，不包含独立 worker lease、并发租约或分布式消费语义。
- Console 当前主要基于 runtime 内存态 + 最近一次 dispatch 结果，尚未形成独立持久化插件读模型。
- `GET /api/console` 当前声明的是已有运行时事实与局部策略边界，不等同于完整鉴权、完整 replay namespace/policy、完整 rollout 控制面或统一 transport taxonomy。
- 当前插件模板只覆盖最小事件型入口；job / schedule / command 插件仍需参考现有官方插件按需扩展。
- `plugins/plugin-template-smoke/manifest.json` 仍是独立静态文件；当前已由 `plugins/plugin-template-smoke/template_test.go` 把 `plugin.go` / `Definition().Manifest` 作为关键 developer-entry 字段的对齐真值来源，但仓库仍未提供独立的 manifest 生成器或脚手架系统。
- 插件失败排障当前已对多条最小失败/边界链提供闭环证据；当前已把 instance-config recursive reject chain 从一层 nested 再向 deeper nested scalar mismatch、相邻的 deeper nested array / object actual value mismatch、deeper nested required missing 与 deeper nested enum out-of-set 推进一步，并把相邻的 deeper nested default-only 收口为“不注入、不拒绝”的真实边界；同时也已确认超出当前 helper 深度的 `settings.labels.naming.prefix=true`、`settings.labels.naming.prefix=["oops"]`、`settings.labels.naming.prefix={"bad":true}`、`settings.labels.naming=true`、`settings.labels.naming=["oops"]`、`settings.labels.naming={}`（配合 deeper `required`）、`settings.labels.naming.prefix="oops"`（配合 deeper `enum=["hello","world"]`）、`settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.default="hello"` + `settings.labels.naming={}`、`settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `settings.labels.naming={}`、`settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `settings.labels.naming={}`、`settings.labels={}`（同一 triple schema 下 `naming` child omission），以及同一 triple schema 下的 `settings.labels.naming.required=["prefix"]` + `settings.labels.naming.properties.prefix.enum=["hello","world"]` + `default="hello"` + `settings.labels.naming.prefix="oops"|["oops"]|true` 与 `settings.labels.naming=["oops"]|null` 这组 mixed / triple / explicit-bad-value / child-omission boundary，都会停止在当前 helper 覆盖边界并继续透传到 subprocess request / fixture / runtime。以上都不等于完整 recursive validator、任意深度 `type` / `enum` 校验、任意深度 `required/default`、任意深度 `enum/default`、任意深度 `required/enum/default` 协同或 default merge；更深层级递归系统化、default merge 系统、除本次已确认的 mixed / triple / explicit bad value / explicit array-valued bad value / explicit wrong-type bad value / explicit object-valued bad value / explicit object-node bad value / explicit array-valued object-node bad value / child omission boundary 外同一 beyond-helper object child 上其他多规则叠加边界，以及共享 runtime+host metrics registry 下的 error dedupe 仍未覆盖。
