# console-web

`apps/console-web` 现在是一个 **读面优先的本地 operator console**，而不再只是单页只读预览。

repo 级 operator console / auth 成熟度边界统一见 [`../../docs/topics/operator-console-auth.md`](../../docs/topics/operator-console-auth.md)。这里主要保留 Console Web 自己的运行、验证与交互面说明。

它仍然坚持当前仓库的本地 console 边界：

- 读模型来自现有 `GET /api/console`
- 写操作走现有 runtime `/demo/*` operator endpoints
- 当前 repo 默认开发基线里，浏览器内的 operator identity 使用 **本地/dev bearer token**，通过 `Authorization: Bearer <token>` 传递
- 当前 actor ID、roles、permissions、plugin scope 仍从 runtime persisted snapshot state 解析，浏览器不自己解释 token claims

## 当前能力

- 轻量级路由化控制台：
  - `/`
  - `/plugins/:pluginId`
  - `/jobs/:jobId`
  - `/schedules/:scheduleId`
  - `/adapters/:adapterId`
  - `/workflows/:workflowId`
- 本地 operator identity 面板：
  - browser-local bearer token
  - token 绑定到当前 runtime 已配置的 actor identity
  - 当前 snapshot 中的 roles / permissions 可视化
  - 当前 RBAC 真值仍来自 persisted snapshot state 里的 actor ID / roles / permissions / plugin scope，不是前端自己解释 token claims
- 刷新模型：
  - 手动 refresh
  - last fetched / runtime generated 时间显示
  - 可关闭的轻量自动 refresh
- 当前仓库已支持的 operator 写操作：
  - plugin enable / disable
  - plugin-echo config update
  - queued job pause / resume / cancel
  - dead-letter job retry
  - schedule cancel
- 更完整的读面证据：
  - alerts
  - audits
  - recovery
  - workflows
  - replay / rollout declarations
  - operator capability / limitation meta

## 本地运行

先启动 runtime：

```bash
npm run dev:runtime
```

再启动 Console Web：

```bash
npm run dev --workspace @bot-platform/console-web
```

Vite dev server 已做最小代理，默认把 `/api`、`/demo`、`/metrics`、`/healthz` 转到本地 runtime `http://127.0.0.1:8080`，方便浏览器手动 QA。

当前本地开发基线需要在启动 runtime 的同一个 shell 中提供至少一个 operator token env，例如：

```powershell
$env:BOT_PLATFORM_OPERATOR_TOKEN = 'dev-operator-token'
npm run dev:runtime
```

如果你要验证更细的权限边界，可以继续提供 `BOT_PLATFORM_OPERATOR_CONFIG_TOKEN`、`BOT_PLATFORM_OPERATOR_JOB_TOKEN`、`BOT_PLATFORM_OPERATOR_SCHEDULE_TOKEN`、`BOT_PLATFORM_OPERATOR_VIEWER_TOKEN`。这些 token 仍然只映射到 repo config 里声明的 actor ID，实际 roles / permissions 来自 runtime 持久化 RBAC snapshot。

兼容性上，只有 runtime 没有配置 `operator_auth.tokens` 的时候，旧的 `X-Bot-Platform-Actor` header 才会继续作为回退路径存在。当前 repo 默认开发配置已经把 bearer token 写成正式基线，所以本地 QA 应按 bearer transport 的 operator console 来理解 Console Web。

## 验证

```bash
npm run test --workspace @bot-platform/console-web
npm run build --workspace @bot-platform/console-web
```

## 设计边界

- 这是 **本地 operator console**，不是完整控制台产品
- browser-local bearer token 只是对当前 runtime 本地 operator auth 基线的诚实 UI 包装
- plugin config editor 故意只收窄到 `plugin-echo` 当前已存在的 persisted config 合同
- 所有写操作都走 read-after-write refetch，不做 optimistic authority 假象
- 不扩展成新的 control-plane API，也不重构 runtime 为前端服务
- 更完整的 repo 级 operator console / auth 边界见 [`../../docs/topics/operator-console-auth.md`](../../docs/topics/operator-console-auth.md)
