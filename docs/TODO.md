# TODO

## 使用规则
- 只保留还要做的事，不保留历史总结。
- 每个任务都要能直接开工，避免“继续研究”“统一梳理”这类空任务。
- 同时只允许 1 个 active。
- backlog 中每项都要写到可以直接切 slice。
- 已完成任务直接从本文件删除，不做归档。

---

## Active

### B18. 更完整的 control plane 写操作
- 目标：从最小 operator action 逐步走向真正 control plane。
- 直接子步骤：
  - 先列出当前最值得暴露的写操作，避免一开始就做大而全控制面。
  - 为 bulk action 定义最小适用范围和失败语义，明确部分成功时如何返回结果。
  - 为每类写操作补 action audit，保证谁在什么时候对什么对象做了什么操作可追踪。
  - 为 rollback / disable 定义最小回退路径，避免写操作一旦执行就没有安全撤回手段。
  - 把 action-level permission 与 B6 对齐，保证控制面写操作不绕开统一权限模型。
  - 为写操作结果定义稳定返回结构，至少能区分已执行、被拒绝、部分成功、需要人工处理。
  - 保持当前范围聚焦在“可安全执行的最小 operator action”，不提前包装成完整产品化控制台。

---

## Backlog

---

## Not now
- 统一 transport/client failure taxonomy 大扩张
- retry / reconnect / backoff 全系统化改造
- 完整插件市场
- 多租户
- 分布式执行
- 完整控制台产品化
- 提前为远期生态做大抽象
