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

### A3. 通用插件配置 / 状态路径
- 当前只有 `plugin-echo` 具备清晰的 persisted plugin-owned config contract。
- 需要把这条路径推广成更多插件可复用的最小规范，而不是继续停留在单插件示例。

### A4. subprocess 实例配置递归校验补全
- 当前实例配置校验仍有明确的深层递归边界。
- 需要继续推进 deeper nested required / enum / default / type 协同行为与 default merge 语义。

### A5. Postgres 可信度继续补强
- 目前 Postgres 只进入 smoke/store 路径，不是主存储。
- 需要补更多真实 runtime 路径覆盖、失败语义和与 SQLite 的能力对齐。

### A6. 真实 provider / 外部服务集成
- 当前 AI provider 仍是 demo/mock 风格路径。
- 需要补真实 provider 接入、配置、故障与可观测链路，而不只是内部演示能力。

### A7. 更强的鉴权与操作者身份模型
- 当前 RBAC 已有真实边界，但仍偏 runtime-local。
- 需要继续补 console/authn、资源模型、策略持久化/热更新等更完整能力。

### A8. secrets / replay / rollout 继续扩展
- 当前这些子系统都有局部实现，但还不是完整统一体系。
- 需要继续补 secret 生命周期、replay 更丰富模式、rollout 历史/回退/阶段化语义。

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
