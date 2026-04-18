# TODO

## 使用规则
- 只保留仍未完成、且已经能从当前代码中明确看出的事项。
- 按“最影响当前平台真实边界”的顺序排序。
- 每项都尽量写成可以继续切 slice 的主题，而不是历史复盘。
- 已完成事项直接从本文件删除，不做归档。

---

## Active

---

## Backlog

### A9. workflow engine 运行态成熟度
- workflow demo 已存在，但还没有更成熟的恢复、查询、运行时治理语义。
- 需要把 workflow 从演示级能力推进到更稳定的运行时边界。

### A10. adapter / transport 扩展与实例路由
- 当前已支持多 bot instance registry，但事件 source、per-instance routing、更多协议适配器仍未真正展开。
- 需要继续补更多 adapter 覆盖与实例级别的路由/管理边界。

### A11. Console Web 演进
- 当前 Console Web 仍是只读优先、单页式控制台。
- 仍缺真实登录、写操作承载、realtime/refresh 模型、路由化详情页、更多 UX 收口。

### A12. 插件打包 / 脚手架成熟度
- 当前插件开发入口已经固定，但还没有真正的脚手架/生成器/发布体验。
- 需要继续补 manifest 生成、打包、脚手架和更完整的开发工作流。

---

## Not now
- 统一 transport/client failure taxonomy 大扩张
- retry / reconnect / backoff 全系统化改造
- 完整插件市场
- 多租户
- 分布式执行
- 完整控制台产品化
- 提前为远期生态做大抽象
