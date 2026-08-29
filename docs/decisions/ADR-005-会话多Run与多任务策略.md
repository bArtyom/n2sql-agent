# ADR-005：会话多 Run 与 DeerFlow 风格多任务策略

## 状态

已接受

## 背景

`conversation_id` 表示用户长期会话，一个会话会产生多个独立的 Agent Run。当前持久化提交入口只创建 `pending` Run，没有在数据库层约束同一会话的活跃根 Run，也没有公开 DeerFlow 风格的 `reject`、`rollback`、`interrupt` 选择。

如果只在 HTTP Handler 中先查询再插入，会出现两个请求同时通过检查、随后同时执行的竞态。因此互斥必须由 PostgreSQL 事务和部分唯一索引共同保证。

## 决策

1. `conversation_id` 继续代表会话，`run_id` 继续代表一次执行。每次新问题、重新生成或替换任务都创建新的根 Run。
2. 同一 `conversation_id` 下，根 Run 的 `pending`、`running`、`waiting_children`、`waiting_approval` 状态最多只能存在一个。
3. 请求增加 `multitask_strategy`，允许 `reject`、`rollback`、`interrupt`，缺省为 `reject`。
4. `reject` 在事务内返回活跃 Run 冲突，不创建新 Run。
5. `rollback` 在同一事务内将活跃 Run 及其未完成子树标记为 `canceled`，原因是 `multitask_rollback`，再创建新 `pending` Run。旧 Run 的事件和 checkpoint 保留审计，但新 Run 只读取已成功根 Run 的会话 checkpoint。
6. `interrupt` 与 `rollback` 一样原子替换活跃 Run，但使用独立的终态 `interrupted` 和原因 `multitask_interrupt`；事务提交后通过 Hub 取消旧 Run 的内存 context，并发布 `run_interrupted` 控制事件。
7. 新 Run 的请求响应仍为 `202 Accepted`；`reject` 返回 `409 Conflict`，携带活跃 Run 的公开 ID 和状态。旧 SSE 只属于旧 `run_id`，新 SSE 使用新 `run_id`。
8. Worker 的租约 token 和终态更新继续作为旧 Worker 的 fencing 机制；被替换的 Worker 不能覆盖新 Run 或写入已终止的旧 Run。
9. 子 Run 不参与会话根 Run 唯一约束；子 Agent 仍由共享 Worker 池领取。

## 事务边界

提交入口调用 `Admit`：

```text
BEGIN
  SELECT active root Run FOR UPDATE
  reject       -> 返回冲突并 ROLLBACK
  rollback     -> 取消旧 Run 树，插入新 Run
  interrupt    -> 中断旧 Run 树，插入新 Run
COMMIT
```

部分唯一索引是第二道并发保护，即使两个请求同时执行 `Admit`，也不会落下两个活跃根 Run。

## 不采用的方案

- 不把 `conversation_id` 和 `run_id` 合并成一个键；这会使重新生成、重试和旧 SSE 无法独立定位。
- 不删除被替换 Run 的 checkpoint 或事件；它们仍用于审计和旧 Run 排查。
- 不让新 Run 读取被取消 Run 的未完成 checkpoint，避免半截工具结果污染新一轮上下文。

## 影响

- API 客户端可以按场景选择拒绝、回滚或中断。
- 数据库迁移会增加 `interrupted` 状态和同会话活跃根 Run 的部分唯一索引。
- 旧的只实现 `agentrun.Store` 的测试替身仍可走普通 `Create`；生产 PostgreSQL Store 实现原子 `Admit`。
- 前端暂不增加复杂 UI；发送请求时可直接增加 `multitask_strategy` 字段。
