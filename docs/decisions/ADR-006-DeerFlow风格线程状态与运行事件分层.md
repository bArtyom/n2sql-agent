# ADR-006：DeerFlow 风格线程状态与运行事件分层

## 状态

已采用。

## 背景

Agent 的“普通会话历史”“当前运行恢复点”“前端实时事件”和“大工具结果”承担不同职责。如果把它们都塞进会话消息表或 Redis，会出现工具过程污染多轮上下文、Worker 接管时无法判断恢复边界、Redis 过期后没有事件日志等问题。

## 决策

本项目采用四层存储边界：

1. `conversation_messages` 只保存用户问题和最终助手回答，供普通会话展示。
2. `agent_thread_contexts` 保存一个会话最近一次成功完成后的隐藏 Agent 状态，包括压缩后的消息、短记忆摘要和版本信息。新一轮 Agent 会重新生成当前系统提示，再把这份状态作为隐藏上下文使用。
3. `agent_run_contexts`、`agent_run_checkpoints` 和 `agent_run_decisions` 保存当前单次运行的恢复快照。Worker 崩溃或租约过期时，下一次 Claim 优先读取这些数据，完成的只读工具可以按参数哈希复用。
4. `agent_run_events` 是 PostgreSQL 中的持久事件日志；Redis Stream 和进程内 Hub 只承担实时 SSE、短期回放和跨进程桥接。事件先写 PostgreSQL，再尽力写 Redis；Redis 失败不能让已完成的 Agent 步骤失败。

工具结果超过内联阈值时，数据库只保存摘要和文件引用，原始大结果放在临时文件/对象存储。运行结束后可以清理单次运行恢复快照和 Redis 流，但线程状态保留到会话被清空或删除；删除会话时同时删除对应 Agent runs、事件和隐藏线程状态。

## 结果

- Worker 接管恢复的是“当前 run 的精确状态”，不会把上一轮已经完成的工具过程误当成当前轮。
- 新一轮对话可以复用隐藏线程上下文，但不会把工具调用暴露给用户，也不会把旧系统提示永久固化。
- SSE 断线时先读 Hub/Redis；Redis 事件过期或新连接晚到时，再从 PostgreSQL 事件日志恢复，最终状态和答案不依赖 Redis。
- 清理行为具有明确边界：Redis 是短期传输数据，run checkpoint 是单次运行数据，thread context 是会话级状态，普通会话消息是用户可见历史。

## 不采用的方案

- 不把所有工具结果追加进 `conversation_messages`。
- 不在启用 Redis 时停止写 PostgreSQL 事件日志。
- 不在每个 token 到达时写数据库；只在模型决策前、工具批次完成后和最终答案边界保存状态。
