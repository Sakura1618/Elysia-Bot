# TODO

## 使用规则
- 只保留仍未完成、且已经能从当前代码中明确看出的事项。
- 按“最影响当前平台真实边界”的顺序排序。
- 每项都尽量写成可以继续切 slice 的收口主题，而不是历史复盘或宽泛愿景。
- 已完成事项直接从本文件删除，不做归档。
- 本页只记录当前主链收口事项。远期方向仅在 Not now 保留边界提示，不作为当前承诺。

## Backlog

### 1. 在读面优先前提下补有限控制面写路径
- 在认证与审计收口后，再扩 plugin config、schedule、dead-letter、rollout 等最小写入口。
- 保持 `console-web` 不是完整产品化控制面，避免 UI 范围再次跑到 runtime 收口前面。
- 先补详情与状态可见性，再考虑批量操作和更强交互。

---

## Not now
- remote plugin runtime / remote host 正式产品化
- 多节点或分布式执行
- 完整插件市场
- 多租户
- 完整控制台产品化
- 低代码 / 可视化 workflow 编排
- 广泛平台扩张（超出当前 OneBot + Webhook 主线）
- 兼容现有外部插件生态
- 跨全系统的大一统 failure taxonomy 扩张
- 超出当前主链所需范围的全局 retry / reconnect / backoff 统一化
- 提前为远期生态做大抽象
