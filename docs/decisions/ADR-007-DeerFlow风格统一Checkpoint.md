# ADR-007：DeerFlow/LangGraph 风格的统一 Agent Checkpoint

## 状态

已采用。

## 背景

Agent 运行时同时有普通会话历史、模型上下文、工具调用、工具结果和运行事件。它们的职责不同，但恢复 Agent 时不能再把“上下文表、决策表、工具结果表”拼成几套互相可能不一致的状态。DeerFlow 依赖 LangGraph checkpointer，把图执行需要继续的状态作为一个统一快照保存；运行元数据和事件日志仍然独立。

## 决策

本项目只保留一类可恢复状态：`agent_checkpoints`。每个快照的 `state` 包含：

1. 当前可供模型继续决策的 `messages`；
2. 经过压缩的 `summary_text`；
3. 已经做出但尚未完成的 `pending_tool_calls`；
4. `version` 和 `last_step`，用于恢复诊断和状态边界标识。

`agent_runs` 继续保存任务身份、租约、状态、请求快照、最终答案和错误；`agent_run_events` 继续保存可审计的运行事件；Redis/Hub 只负责实时 SSE 和短期回放。这三者不是 checkpoint 的替代品。

大工具结果不直接无限膨胀 checkpoint：小结果以内联方式写入 `state`，超过阈值的结果写入临时文件，`state` 只保存受限预览和引用。恢复时由存储层重新加载文件；文件失效时，普通只读工具可以重新执行，副作用工具仍需遵守审批和幂等规则。

## 保存边界

Engine 在以下语义边界保存同一份完整状态：

- 下一次模型调用前；
- 模型产出工具调用后；
- 每个工具结果加入消息后；
- 最终答案形成后。

不在每个 SSE token 到达时写 checkpoint。SSE 事件用于展示过程，checkpoint 用于让 Worker 崩溃后从一致状态恢复。

## Worker 接管

Worker A 领取 `agent_run` 后执行模型和工具。若 A 在工具调用已经写入 `pending_tool_calls`、但工具结果尚未写入时崩溃，租约回收后 Worker B 读取同一个 run 的最新统一 checkpoint，看到待执行工具调用，直接执行这次已确定的调用，再继续模型；不会重新先让模型做一遍相同决策。

如果工具结果已经加入消息并保存，B 会从同一快照读取 assistant 工具调用消息和 tool 消息，继续下一次模型调用。普通工具允许按既定策略重试；有副作用工具不绕过审批自动重试。

## 多轮会话

完成的 root run 的最新 checkpoint 可以作为下一轮的隐藏上下文来源。生成下一轮请求时，系统提示由当前模型、工具和权限重新构造，checkpoint 只提供历史消息、压缩摘要和必要的工具状态。用户可见的 `conversation_messages` 仍只保存 user/assistant 对话，不把 Agent 内部事件写进普通历史。

## 清理

运行结束后不立即删除统一 checkpoint，因为它还可能作为下一轮会话的隐藏状态。删除或清空会话时，按 `conversation_id` 删除 checkpoint；`agent_run_id` 的级联删除负责清理运行树下的快照。临时大结果文件由独立 TTL 清理。Redis 事件按短期 TTL 自然过期。

## 结果

- 恢复入口只有一个，不再出现上下文、决策和工具 checkpoint 互相漂移；
- Worker 可以复用已完成的上下文，也可以继续尚未完成的工具调用；
- 当前系统提示不会被旧版本固化到 checkpoint；
- 运行状态、事件展示、会话历史和 Agent 恢复各自保持清晰边界。
