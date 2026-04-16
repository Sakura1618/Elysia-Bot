# TODO

## 使用规则
- 只保留还要做的事，不保留历史总结。
- 每个任务都要能直接开工，避免“继续研究”“统一梳理”这类空任务。
- 同时只允许 1 个 active。
- next 保留 3~7 个。
- 更远期但明确要做的，放 backlog。
- 已完成任务直接从本文件删除，不做归档。

---

## Active

### T6. 插件配置管理最小闭环
- 目标：把当前 manifest / config schema 边界推进到“平台可管理”的最小状态。
- 最小完成形态：
  - 至少 1 个插件配置可被读取、校验、持久化、再次加载
  - 错配时有 caller-facing error
  - 不扩成完整可视化配置系统

---

## Next

### T7. runtime/operator 写操作最小入口
- 目标：在只读 Console 之外，补最小 operator action。
- 候选切片：
  - 手动重试一个失败 job
  - 手动取消一个 schedule
  - 手动 reload 一个 plugin
- 完成标准：
  - 至少 1 个动作真实可用
  - 有基本审计或日志证据
  - 不扩成完整 control plane

---

## Backlog

### B1. 多平台 / adapter 实例管理边界
- 目标：让 adapter 不只是“代码里能接”，而是“平台里可管理”。
- 子任务方向：
  - adapter 实例配置模型
  - adapter 连接状态读面
  - 多 bot / 多账号状态聚合
  - health/readiness 聚合到 adapter 级别

### B2. bot / adapter / plugin 生命周期状态页
- 目标：形成成熟机器人项目常见的统一运行状态面。
- 最小关注项：
  - bot 是否在线
  - adapter 是否健康
  - plugin 是否启用
  - 最近一次失败原因 / 时间

### B3. 插件开发者入口标准化
- 目标：把 `plugin-template-smoke` 提升为正式开发入口。
- 子任务方向：
  - 创建插件流程统一
  - manifest / README / smoke test 模板统一
  - 开发文档最小化到模板和命令本身
  - “复制 -> 改名 -> 注册 -> smoke” 变成稳定路径

### B4. 插件发布 / 分发规范
- 目标：先建立发布规范，再考虑市场。
- 子任务方向：
  - 插件元数据规范
  - 版本兼容范围声明
  - 安装来源声明
  - 最小发布校验

### B5. 插件兼容性 / 版本管理增强
- 目标：把现在的 compatibility 边界推进到生态可用级别。
- 子任务方向：
  - runtime version ↔ plugin version
  - adapter capability ↔ plugin requirement
  - manifest compatibility error 更稳定可解释

### B6. RBAC 从局部边界推进到 operator action
- 目标：把“谁能执行什么动作”收口到统一权限模型。
- 子任务方向：
  - plugin reload permission
  - console action permission
  - schedule/job action permission
  - deny audit 继续保持统一

### B7. secrets 进入主路径
- 目标：让 secret 不只是局部配置，而是正式成为 plugin / provider / adapter 配置来源。
- 子任务方向：
  - secret resolution 接入 plugin config
  - secret missing / invalid caller-facing error
  - secret source 与使用边界可观测

### B8. 告警 / 通知最小闭环
- 目标：让关键故障不只存在日志里。
- 候选切片：
  - job 持续失败告警
  - plugin host crash 告警
  - adapter 连接异常告警

### B9. replay / retry / dead-letter operator workflow
- 目标：把失败后的人工处理路径做成平台能力。
- 子任务方向：
  - retry failed job
  - inspect failure reason
  - replay selected event / request
  - dead-letter visibility

### B10. fault injection 继续贴近真实外部依赖
- 目标：把 fault injection 从当前最小边界继续推进。
- 子任务方向：
  - provider failure
  - outbound webhook/client failure
  - plugin-host failure
  - persistence failure

### B11. Postgres 可信度增强
- 目标：保持 Postgres 作为 v0 边界，但增强真实可用性。
- 子任务方向：
  - migration / bootstrap 更稳定
  - 最小读写 smoke
  - 与 runtime 路径的真实连接验证

### B12. 部署 / 运维入口增强
- 目标：补成熟机器人项目常见的 operator 入口。
- 子任务方向：
  - 启动/停止/重启脚本
  - 升级路径
  - 备份/恢复最小流程
  - 配置检查命令

### B13. runtime 健康检查细化
- 目标：让 `/healthz` 不只是进程活着。
- 子任务方向：
  - storage health
  - scheduler health
  - plugin-host health
  - adapter health
  - aggregated readiness

### B14. 插件数据 / 状态持久化规范
- 目标：让插件作者知道状态存哪里、如何恢复、如何暴露给 Console。
- 子任务方向：
  - plugin state store guideline
  - snapshot / status model
  - migration / compatibility expectations

### B15. 开发者体验增强
- 目标：向 NoneBot2 这类成熟框架靠近，但只补最有价值的部分。
- 子任务方向：
  - 更明确的脚手架命令
  - 本地 smoke / e2e 命令统一
  - 模板与测试约束统一
  - 最小发布检查命令

### B16. 插件索引 / 市场前置条件
- 目标：先做 marketplace 前置，不直接做完整市场。
- 子任务方向：
  - 可机器读取的插件索引格式
  - 插件列表读面
  - 来源可信度 / 兼容性字段
  - 安装前检查

### B17. 更完整的多 bot / 多账号编排
- 目标：向 TRSS-Yunzai 的成熟度靠近，但不一次做满。
- 子任务方向：
  - bot instance registry
  - per-instance status
  - per-instance config
  - per-instance restart / isolation boundary

### B18. 更完整的 control plane 写操作
- 目标：从最小 operator action 逐步走向真正 control plane。
- 子任务方向：
  - bulk action
  - action audit
  - rollback / disable
  - action-level permission

---

## Not now
- 统一 transport/client failure taxonomy 大扩张
- retry / reconnect / backoff 全系统化改造
- 完整插件市场
- 多租户
- 分布式执行
- 完整控制台产品化
- 提前为远期生态做大抽象
