# roadmap

这页是当前仓库的路线图与文档治理索引。

它不重复 README 的能力说明，也不代替 `docs/TODO.md` 的主链 backlog。它只负责三件事：当前阶段判断、主路径与非主路径分类、文档归属边界。

当前判断基于仓库内已经存在的真值：`README.md`、`docs/TODO.md`、`AGENTS.md`、`apps/console-web/README.md`、`plugins/plugin-template-smoke/README.md`、`deploy/config.dev.yaml`、`package.json`、`.github/workflows/nightly-validation.yml`。

当前已拆出的专题文档：[`docs/topics/operator-console-auth.md`](../topics/operator-console-auth.md)、[`docs/topics/plugin-development-main-path.md`](../topics/plugin-development-main-path.md)、[`docs/topics/plugin-development-conventions.md`](../topics/plugin-development-conventions.md) 与 [`docs/topics/runtime-storage-main-path.md`](../topics/runtime-storage-main-path.md)，分别用于收口 operator console / bearer auth / limited control surface、repo-local 插件开发主路径 / `Manifest()` 真值规则 / nightly 验证口径、repo-local 插件作者需要跟随的权限 / config schema / observability / publish 约定，以及 runtime storage 主路径 / Postgres 非默认验证路径 / 相邻 provider 分类的成熟度边界。

## 当前阶段：先收口，再扩张

当前仓库的首要目标，不是继续铺更宽的能力面，而是把已经进入主链的运行、恢复、观测、插件开发与最小 operator 路径收口成一条更稳定的主路径。

这和 `AGENTS.md` 里的优先级一致：先保证主链可运行、可验证，其次保证恢复语义与边界清晰，再谈体验优化与扩展。

这意味着：

- 只有被默认配置、默认入口、当前 README、聚焦测试或 CI 同时支撑的能力，才算当前主路径。
- 已验证但不是默认入口的能力，要明确标成 smoke 或非默认路径。
- 只有骨架、局部结构或概念预留的东西，不写成当前承诺。
- 远期层次可以保留，但只能作为分层指引，不能包装成现在就要兑现的蓝图。

## 如何判断一项能力属于哪一层

### 1. 主路径，main path

主路径是当前 repo-owned 的默认开发、运行与验证路线。写进 README 时，可以直接作为“当前真实能力”叙述。

当前主路径事实如下。

#### Go runtime 是当前主执行入口

- `package.json` 把 `npm run dev:runtime` 定义为 `go run ./apps/runtime -config ./deploy/config.dev.yaml`
- `README.md` 明确把 `apps/runtime` 标为默认开发态 runtime 入口

#### SQLite 是当前主运行态存储

- `deploy/config.dev.yaml` 默认写的是 `runtime.sqlite_path: data/dev/runtime.sqlite`
- 同一份配置把 `runtime.smoke_store_backend` 设为 `sqlite`
- `README.md` 明确写了 SQLite 是当前主要状态存储，Postgres 不是当前主存储
- 更具体的 storage 主路径、Postgres smoke / acceptance 分类、以及相邻的 `mock` / `openai_compat` provider 边界，统一见 [`docs/topics/runtime-storage-main-path.md`](../topics/runtime-storage-main-path.md)

#### OneBot + Webhook 是当前主 adapter 路径

- `adapters/adapter-onebot` 与 `adapters/adapter-webhook` 都在根 README 里作为当前主要组成部分出现
- `deploy/config.dev.yaml` 默认 bot instance 走 OneBot，`/demo/onebot/message` 也是默认开发态入口的一部分
- `deploy/config.dev.yaml` 同时保留 `secrets.webhook_token_ref`，对应当前 webhook ingress 路径

这里的含义是：当前仓库的主线协议接入面，以 OneBot 消息路径和 Webhook ingress 为准。超出这条线的广泛平台扩张，不属于现在的 main path。

#### Console Web 是读面优先的本地 operator console

- `apps/console-web/README.md` 明确写的是“读面优先的本地 operator console”
- 当前读模型来自 `/api/console`
- 当前写操作只走现有 runtime `/demo/*` operator endpoints
- 更具体的 operator console / auth 成熟度边界，统一见 [`docs/topics/operator-console-auth.md`](../topics/operator-console-auth.md)

所以，当前主路径里包含 Console Web，但它的角色是本地读面优先、有限写入，不是完整控制面产品。

#### repo-local 插件开发流是当前插件主路径

- `plugins/plugin-template-smoke/README.md` 把 `plugin-template-smoke` 定义为最小 repo-local 插件模板
- `package.json` 提供 `plugin:dev`、`plugin:scaffold`、`plugin:manifest:write`、`plugin:manifest:check`、`plugin:package`、`plugin:smoke`
- 根 README 把 `plugin-template-smoke`、`packages/plugin-sdk/cmd/plugin-dev`、`npm run test:plugin-template:smoke` 收口成当前最小插件开发入口
- nightly validation 也把 `plugin-template-smoke` 放进 plugin matrix

因此，当前插件主路径是 repo 内脚手架、manifest 生成与校验、轻量 package、smoke 验证。这不是插件市场，也不是远程插件分发体系。

更具体的命令顺序、`Manifest()` 真值规则、nightly 验证口径与非目标，统一见 [`docs/topics/plugin-development-main-path.md`](../topics/plugin-development-main-path.md)。

### 2. 已验证，但非默认，或 smoke 路径

这类能力已经有测试、CI 或配置入口支持，但不是当前仓库默认叙事中心，也不是默认开发入口。

当前主要包括：

- Postgres smoke / acceptance 路径
  - `package.json` 提供 `test:postgres:smoke` 与 `test:postgres:acceptance`
  - `.github/workflows/nightly-validation.yml` 每晚跑 Postgres smoke 与 acceptance
  - 但 `README.md` 和 `deploy/config.dev.yaml` 都明确 SQLite 才是当前主存储
- 有限 operator 写面
  - 根 README 与 `apps/console-web/README.md` 都明确了 plugin enable / disable、`plugin-echo` config update、schedule cancel、job pause / resume / cancel / retry 这些最小写入口
  - 这条线是已验证的，但用途是证明当前控制面写路径成立，不等于“完整控制台已经是默认主线”
- `openai_compat` 的窄 real-provider 路径
  - `deploy/config.dev.yaml` 默认仍是 `ai_chat.provider: mock`
  - 根 README 只把 `openai_compat` 描述成当前单一、收口后的真实 provider 路径，而不是默认启动配置

这类能力可以继续增强，但文档上要诚实标注为 non-default 或 smoke，不把它们写成当前 repo 的默认主故事。

### 3. 骨架，或非主线，skeleton / not-mainline

这类东西可能已经有包结构、命名、局部实现或验证入口，但当前不应被叙述成默认路线。

当前常见判断方式是：有边界，不等于已成为主线。

在这个仓库里，至少要这样理解：

- subprocess plugin host 是当前 runtime 边界的一部分，但插件开发主线仍然是 repo-local `plugin-template-smoke` + `plugin-dev` + smoke
- Console Web 已经有局部写入口和详情页路由，但仍然是读面优先、本地 operator console，不是完整控制面主线
- plugin package、publish metadata 等发布边界已经开始出现，但当前主线仍然是 repo 内开发与验证，不是开放插件生态

这类内容可以存在，也值得继续收口，但 README 和 roadmap 不应把它们写成“当前默认能力面已经切换过去”。

### 4. Not now

Not now 是明确不进入当前承诺的边界提示，不是隐藏 backlog。

当前直接沿用 `docs/TODO.md` 的边界：

- remote plugin runtime / remote host 正式产品化
- 多节点或分布式执行
- 完整插件市场
- 多租户
- 完整控制台产品化
- 低代码 / 可视化 workflow 编排
- 超出当前 OneBot + Webhook 主线的广泛平台扩张
- 兼容现有外部插件生态
- 超出当前主链所需范围的全局 failure taxonomy、retry、reconnect、backoff 一体化扩张
- 为远期生态提前做大抽象

这些内容可以影响今天的边界设计，但它们不是当前阶段的交付承诺。

## 文档归属规则

文档按下面的职责分工维护。

- `README.md`：入口说明，当前能力摘要，当前范围边界
- `docs/TODO.md`：只保留仍未完成的主链收口 backlog
- `docs/roadmap/README.md`：当前阶段判断、主路径与成熟度分类、文档治理口径
- `docs/adr/*`：只记录已经被有意选定、需要长期保留的架构决策

补充规则：

- 当 README、roadmap、TODO 冲突时，先回到代码、配置、测试脚本、CI 和组件级 README 找真值，再修文档
- 不把 backlog 细节倒回 README
- 不把探索期讨论提前写成 ADR
- 不把 roadmap 写成 feature list，也不把 TODO 写成远期愿景墙

## 长期层次，只作为分层指引

`内核年 / 生态年 / 平台年` 是长期分层方式，不是当前承诺，也不是日历时间表。

- 内核年：优先收口 runtime-core、状态与恢复语义、调度、观测、adapter 边界、plugin host 合同
- 生态年：在内核边界稳定后，再系统化插件开发约定、参考插件、发布约束与生态接口
- 平台年：在前两层稳定后，再扩更强的 operator surface、治理工具、集成面与更完整的平台能力

当前仓库仍应按“先收口，再扩张”处理，也就是先保证内核主链和最小插件开发闭环更稳，再决定哪些生态面和平台面值得升级为默认路线。
