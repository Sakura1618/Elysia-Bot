# bot-platform

一个以 Go runtime 为核心、面向长期运行、恢复语义与可观测性的机器人运行平台。

当前仓库已经不是单一 demo，而是一个可本地运行、可测试、可扩展插件、带最小运维与读面能力的运行平台。它包含可执行的 runtime 入口、只读控制台、持久化状态、作业队列、调度器、subprocess 插件宿主、协议适配器，以及一组内置/示例插件。

## 仓库当前包含的主要部分

- `apps/runtime`：默认开发态 runtime 入口。
- `apps/console-web`：React + Vite 的只读控制台前端。
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
- dead-letter job retry

这些写路径已经具备最小 RBAC、audit 与稳定返回 envelope，但仍然是 v0 级控制面能力，不代表完整控制台。

### Console Web

`apps/console-web` 当前是 **live API 驱动的只读控制台**：

- 直接请求 `/api/console`
- 展示 runtime / adapter / plugin / job / schedule / log / config 等只读信息
- 支持 `log_query`、`job_query`、`plugin_id` 这类 URL 同步过滤
- 已消费 plugin recovery、enabled overlay、persisted config、publish metadata 等运行态事实

它不是完整控制面，也不包含真实登录、批量操作、路由化详情页或实时推送体系。

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
- `POST /demo/jobs/{job-id}/retry`
- `POST /demo/schedules/echo-delay`
- `POST /demo/schedules/{schedule-id}/cancel`
- `POST /demo/plugins/{plugin-id}/enable`
- `POST /demo/plugins/{plugin-id}/disable`
- `POST /demo/plugins/{plugin-id}/config`
- `GET /demo/replies`
- `GET /demo/state/counts`

## 常用命令

```bash
npm install
npm run lint
npm run test
npm run format
npm run dev
npm run dev:runtime
npm run dev:runtime:check-config
npm run test:plugin-template:smoke
npm run test:postgres:smoke
```

说明：

- `npm run dev`：启动 `apps/console-web`
- `npm run dev:runtime`：启动 Go runtime
- `npm run dev:runtime:check-config`：只做 runtime 配置预检，不启动 HTTP server
- `npm run test`：运行当前纳入工作区的 Go 测试集合
- `npm run test:plugin-template:smoke`：运行插件模板开发路径的聚焦 smoke 验证
- `npm run test:postgres:smoke`：运行受环境变量控制的 Postgres live smoke 测试

## 插件开发入口

当前最小插件开发入口已经收口到：

- `plugins/plugin-template-smoke/README.md`
- `npm run test:plugin-template:smoke`

这条路径适用于仓库内“复制 -> 改名 -> 注册 -> smoke”的最小插件开发流。它不是完整脚手架系统，也不是开放插件市场。

## 当前范围边界

为了避免 README 继续承担 backlog 说明，这里只保留当前范围边界，不列开发 TODO：

- SQLite 仍是当前主要运行态存储；Postgres 只进入较小的 smoke/store 路径。
- Console Web 仍是读面优先的控制台，不是完整产品化控制面。
- 插件系统已具备真实运行与恢复/排障基础，但还不是成熟第三方插件生态。
- AI 链路当前是 demo / mock 风格 provider 路径，不应按完整生产 provider 集成来理解。
- 后续未完成事项统一放在 `docs/TODO.md`，README 不再承担开发任务清单角色。
