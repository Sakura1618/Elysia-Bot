# bot-platform

一个以 Go runtime 为核心、面向长期运行、恢复语义与可观测性的机器人运行平台。

当前仓库已经不是单一 demo，而是一个可本地运行、可测试、可扩展插件、带最小运维与读面能力的运行平台。它包含可执行的 runtime 入口、读面优先的本地 operator console、持久化状态、作业队列、调度器、subprocess 插件宿主、协议适配器，以及一组内置/示例插件。

## 仓库当前包含的主要部分

- `apps/runtime`：默认开发态 runtime 入口。
- `apps/console-web`：React + Vite 的读面优先、本地 bearer token 驱动的 operator console 前端。
- `packages/runtime-core`：runtime、队列、调度、状态存储、console API、审计、RBAC、secrets、subprocess host 等核心语义。
- `adapters/adapter-onebot`：OneBot 入站转换与回复发送能力。
- `adapters/adapter-webhook`：带鉴权、审计与 observability 的 webhook ingress。
- `plugins/`：内置与样例插件，包括 `plugin-echo`、`plugin-ai-chat`、`plugin-admin`、`plugin-scheduler`、`plugin-workflow-demo`、`plugin-template-smoke`。
- `tests/e2e` 与 `tests/fault-injection`：端到端与故障注入验证。

## 当前真实能力

### Runtime 与状态

- 默认 runtime 可直接从 `deploy/config.dev.yaml` 启动。
- SQLite 是当前主要状态存储，覆盖 event journal、idempotency、plugin 状态、job、schedule、alert、audit 等运行态数据。
- Postgres 已有最小 store / schema / smoke path，可用于 runtime smoke-store 路径验证，但不是当前主存储。
- `/healthz` 已是结构化健康检查，不再是单纯的硬编码 liveness。
- `/metrics` 暴露 Prometheus 风格指标。

### 队列、调度与恢复

- 已有持久化 JobQueue，支持 enqueue、timeout、dead-letter、operator retry 与恢复可见性。
- 已有持久化 Scheduler，支持 delay / cron / one-shot 计划的最小恢复链路。
- runtime 启动时会恢复已有 job / schedule / adapter / plugin 读面所需状态。

### 插件系统

- 支持进程内插件与 subprocess 插件宿主。
- 已有 manifest 兼容性预检、实例配置边界校验、plugin enabled overlay、plugin status snapshot、plugin config persistence 等基础能力。
- `plugin-echo` 已作为最小 persisted plugin-owned config 路径示例。
- `plugin-template-smoke` 是当前最小插件开发模板与 smoke 入口。

### Adapter 与实例读面

- runtime 现在支持通过配置声明多个 OneBot bot 实例，并把每个实例持久化为独立 adapter instance。
- `/api/console` 与 Console Web 已能显示 adapter 实例级别的状态与配置摘要，而不只是 adapter 类型汇总。

### 控制面写路径

runtime 当前已经提供一批最小 operator 写入口，主要用于验证控制面写路径，而不是完整控制台产品：

- plugin enable / disable
- plugin config update（当前只对 `plugin-echo` 暴露）
- schedule cancel
- queued job pause / resume / cancel
- dead-letter job retry

这些写路径现在默认以 `Authorization: Bearer <token>` 作为本地 operator 身份入口，并具备最小 RBAC、audit 与稳定返回 envelope，但仍然是 v0 级控制面能力，不代表完整控制台。

当前这条 auth 基线的真实边界是：bearer token 只负责把请求绑定到一个已配置 actor identity，当前 RBAC 仍然从持久化 snapshot state 读取 actor ID、roles、permissions 与 scope；这里还没有 JWT 解析、SSO、login UI 或完整 session 产品面。

### Console Web

`apps/console-web` 当前是 **live API 驱动、读面优先的本地 operator console**：

- 直接请求 `/api/console`
- 通过浏览器本地保存的 operator bearer token，以 bearer transport 调用 `/api/console` 与现有 `/demo/*` 写路径
- 当前 actor ID、roles、permissions 与 plugin scope 仍由 runtime 持久化 snapshot state 决定，不从 bearer token 自己解析 claims
- 展示 runtime / adapter / plugin / job / schedule / log / config 等只读信息
- 支持 `log_query`、`job_query`、`plugin_id` 这类 URL 同步过滤
- 已消费 plugin recovery、enabled overlay、persisted config、publish metadata 等运行态事实

它不是完整控制面，也不包含 JWT / SSO、login UI、真实登录、批量操作、路由化详情页或实时推送体系。

## 默认 runtime 开发入口

```bash
npm run dev:runtime
```

默认读取 `deploy/config.dev.yaml`。当前开发态入口会暴露这些主要路径：

- `GET /healthz`
- `GET /api/console`
- `GET /metrics`
- `POST /demo/onebot/message`
- `POST /demo/ai/message`
- `POST /demo/jobs/enqueue`
- `POST /demo/jobs/timeout?id=<job-id>`
- `POST /demo/jobs/{job-id}/pause`
- `POST /demo/jobs/{job-id}/resume`
- `POST /demo/jobs/{job-id}/cancel`
- `POST /demo/jobs/{job-id}/retry`
- `POST /demo/schedules/echo-delay`
- `POST /demo/schedules/{schedule-id}/cancel`
- `POST /demo/plugins/{plugin-id}/enable`
- `POST /demo/plugins/{plugin-id}/disable`
- `POST /demo/plugins/{plugin-id}/config`
- `GET /demo/replies`
- `GET /demo/state/counts`

`deploy/config.dev.yaml` 默认仍使用 `ai_chat.provider: mock` 方便本地直接启动，但已经同时包含最小真实 provider 入口：把 `ai_chat.provider` 切到 `openai_compat`、保留 `secrets.ai_chat_api_key_ref`，并提供一个 `https://` endpoint（本地开发/测试只对 `localhost` / loopback 保留 `http://` 例外）。

同一个开发态配置现在也把本地 operator bearer-auth 基线显式写进 repo owned config：

- `operator_auth.tokens` 是当前正式的 operator identity 入口
- 每个 token 通过 `token_ref` 绑定环境变量 secret，通过 `actor_id` 绑定 repo config 中已有的 actor
- 当前 actor ID、roles、permissions 与 plugin scope 仍以 `rbac.actor_roles` / `rbac.policies` 为真值，并会持久化为 runtime snapshot state 供 console read 与 operator write 路径使用
- 当 `operator_auth.tokens` 已配置时，console read、Console Web 与当前 operator 写路径都会走 bearer transport，不再接受裸 actor header 作为主入口
- 只有在 operator auth 未配置的兼容场景下，runtime 才会回退到 `X-Bot-Platform-Actor` header

这只是本地 bearer token 基线，不应理解为已经具备 JWT claims 解析、SSO 集成、login UI、登录态管理或完整的认证产品面。

## 常用命令

```bash
npm install
npm run lint
npm run test
npm run format
npm run dev
npm run dev:runtime
npm run dev:runtime:check-config
npm run plugin:scaffold -- -id plugin-example
npm run plugin:manifest:write -- -plugin ./plugins/plugin-template-smoke
npm run plugin:manifest:check -- -plugin ./plugins/plugin-template-smoke
npm run plugin:package -- -plugin ./plugins/plugin-template-smoke
npm run test:plugin-template:smoke
npm run test:postgres:smoke
```

说明：

- `npm run dev`：启动 `apps/console-web`
- `npm run dev:runtime`：启动 Go runtime
- `npm run dev:runtime:check-config`：只做 runtime 配置预检，不启动 HTTP server
- `npm run plugin:scaffold -- -id plugin-example`：从 `plugin-template-smoke` 生成新的 repo-local 插件目录，并自动更新 `go.work`
- `npm run plugin:manifest:write -- -plugin <dir>`：把插件源码里的 `Manifest()` 写成 `manifest.json`
- `npm run plugin:manifest:check -- -plugin <dir>`：校验 `manifest.json` 没有偏离源码真值
- `npm run plugin:package -- -plugin <dir>`：输出轻量 repo-local `dist/` 打包产物（当前包含生成 manifest 与 README）
- `npm run test`：运行当前纳入工作区的 Go 测试集合
- `npm run test:plugin-template:smoke`：运行插件模板开发路径的聚焦 smoke 验证
- `npm run test:postgres:smoke`：运行受环境变量控制的 Postgres live smoke 测试

## 插件开发入口

当前最小插件开发入口已经收口到：

- `plugins/plugin-template-smoke/README.md`
- `packages/plugin-sdk/cmd/plugin-dev`
- `npm run test:plugin-template:smoke`

这条路径适用于仓库内“脚手架 -> 改 `Manifest()`/逻辑 -> 生成 manifest -> smoke”的最小插件开发流。它不是完整脚手架系统，也不是开放插件市场。

## 当前范围边界

为了避免 README 继续承担 backlog 说明，这里只保留当前范围边界，不列开发 TODO：

- SQLite 仍是当前主要运行态存储；Postgres 只进入较小的 smoke/store 路径。
- Console Web 仍是读面优先的控制台，不是完整产品化控制面。
- 插件系统已具备真实运行与恢复/排障基础，但还不是成熟第三方插件生态。
- AI 链路当前已经支持一条收口后的真实 provider 路径：`ai_chat.provider=openai_compat` + runtime-owned `secrets.ai_chat_api_key_ref`，但这仍只是单一的窄集成面，不应按完整 provider 平台、多供应商框架或生产级密钥管理系统来理解。
- 后续未完成事项统一放在 `docs/TODO.md`，README 不再承担开发任务清单角色。
