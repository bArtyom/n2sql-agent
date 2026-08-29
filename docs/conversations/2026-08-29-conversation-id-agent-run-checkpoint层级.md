# conversation_id、agent_run_id 与统一 Checkpoint 的层级关系

> 日期：2026-08-29
>
> 主题：Agent 会话、单轮执行、Worker 接管和 Checkpoint 恢复

## 学习问题

刚才讨论了“统一 checkpoint 最终是不是一个会话级状态”。需要区分：会话、一次 Agent 执行，以及执行过程中的恢复快照不是同一个层级。

## 核心结论

Checkpoint 不是简单地“整个会话只有一个状态”，也不是完全脱离会话的临时数据：

- `conversation_id` 表示会话窗口，负责组织多轮用户消息和助手最终回答。
- `agent_run_id` 表示某一次具体的 Agent 执行，通常对应用户在会话里提交的一轮问题。它是 Worker 领取、续租、重试和恢复的边界。
- Checkpoint 表示某一次 `agent_run_id` 执行到某个步骤时保存的可恢复快照，因此恢复时首先按当前 Run 查找。
- “统一 checkpoint”指的是把可恢复状态放进一个统一结构中，例如消息、工具调用/结果引用、摘要、待执行调用、中断节点和当前步骤；不表示一个会话只能有一条 checkpoint。

因此，更准确的说法是：Checkpoint 在逻辑上属于某个会话的状态链，但每个具体快照必须绑定到某一个 Agent Run。`conversation_id` 用于查找和组织多轮状态，`agent_run_id` 用于保证 Worker 能准确恢复某一轮执行。

## 层级关系

```text
conversation_id = 42                会话窗口
├── agent_run_id = 101              第 1 轮执行
│   ├── checkpoint v1：已收到问题
│   ├── checkpoint v2：知识库检索完成
│   └── checkpoint v3：模型已拿到工具结果，准备继续
└── agent_run_id = 102              第 2 轮执行
    ├── checkpoint v1：已加载历史上下文
    └── checkpoint v2：正在调用知识库
```

同一个 `conversation_id` 可以拥有多个 `agent_run_id`；每个 Run 可以有多个版本化 checkpoint。第 1 轮的 checkpoint 不会因为第 2 轮开始就覆盖第 2 轮的执行状态。

## 具体例子：第一轮发生崩溃

用户在会话 42 中提问：“请总结员工手册中的年假规则。”系统创建：

```text
agent_runs
┌────┬────────┬─────────────────┬─────────┐
│ id │ run_id │ conversation_id │ status  │
├────┼────────┼─────────────────┼─────────┤
│101 │ run-101│42               │running  │
└────┴────────┴─────────────────┴─────────┘
```

Worker A 先完成知识库检索，再保存一个统一 checkpoint：

```json
{
  "agent_run_id": 101,
  "conversation_id": 42,
  "version": 2,
  "step_number": 1,
  "current_node": "after_tool",
  "messages": [
    {"role": "user", "content": "请总结员工手册中的年假规则。"},
    {"role": "assistant", "tool_call_id": "call-1", "name": "knowledge_search"},
    {"role": "tool", "tool_call_id": "call-1", "content_ref": "tmp/run-101/tool-1.json"}
  ],
  "pending_tool_calls": [],
  "summary_text": "已检索员工手册的年假相关段落。"
}
```

如果 Worker A 此时崩溃，租约恢复逻辑会让这个 Run 重新进入可领取状态。Worker B 领取的仍然是 `agent_run_id = 101`，而不是创建一个新的会话或凭空从头开始。它会：

1. 读取 `agent_runs`，确认 Run 仍可恢复；
2. 按 `agent_runs.id = 101` 读取最新 checkpoint；
3. 从 checkpoint 恢复已经完成的只读工具结果或结果引用；
4. 从 `current_node = after_tool` 继续下一次模型决策；
5. 完成后把最终答案写入 Run，并把用户消息和最终助手回答写入普通会话历史。

如果工具结果文件已经不存在，Worker B 不能假装结果还在。对于只读工具，可以根据 checkpoint 中保存的参数重新调用；对于有副作用的工具，要先依据幂等键和外部状态判断是否已经执行，不能因为 checkpoint 缺失就盲目重复。

## 第二轮开始时发生什么

第一轮完成后，普通会话历史通常保存：

```text
conversation_messages(conversation_id = 42)
1. user      请总结员工手册中的年假规则。
2. assistant 员工年假根据入职年限计算……
```

内部的工具调用、工具结果和中间思考不会全部当作普通聊天消息永久追加到下一轮。第二轮问题“那病假呢？”会创建新的 Run：

```text
agent_runs
┌────┬────────┬─────────────────┬─────────┐
│ id │ run_id │ conversation_id │ status  │
├────┼────────┼─────────────────┼─────────┤
│101 │ run-101│42               │succeeded│
│102 │ run-102│42               │pending  │
└────┴────────┴─────────────────┴─────────┘
```

Run 102 构建自己的模型上下文：用户当前问题、会话历史中的用户/助手消息、必要的历史摘要、当前用户记忆和本轮工具定义。只有当 Run 102 需要恢复自己的中断执行时，才读取它自己的 checkpoint；不会把 Run 101 的整套工具事件直接当成新的普通对话消息注入。

如果 Run 101 只调用了一次工具且结果很小，它的内部消息可以在 Run 101 的模型调用链中完整存在；这不等于它会被永久复制进 `conversation_messages`。是否在后续上下文中保留某些信息，取决于最终回答、历史摘要和当前 Run 的上下文构建策略，而不是“只要调用过工具就自动进入下一轮”。

## 两种读取场景

### 1. Worker 接管当前失败 Run

```go
checkpoint, err := checkpointStore.GetLatestCheckpoint(ctx, run.ID)
```

这里使用当前 `agent_run_id`，因为目标是精确恢复第 101 轮执行到哪一步。不能只用 `conversation_id = 42`，否则同一会话存在多个 Run 时可能读错状态。

### 2. 新 Run 加载会话级历史状态

```go
checkpoint, err := checkpointStore.GetLatestThreadCheckpoint(
    ctx,
    run.ConversationID,
)
```

这类读取用于新一轮根 Run 没有自己的恢复 checkpoint、但需要加载会话关联的历史状态时。它是“按会话查找参考状态”，不是把多个 Run 合并成一条正在执行的任务。当前执行恢复的优先级仍然是：先读当前 Run 的 checkpoint，再按明确的会话上下文策略加载历史消息或摘要。

## 和数据库表的联系

当前设计可以理解为：

```text
conversations
    42  ───────────────┐
                       │ 1 对多
agent_runs             │
    101（第 1 轮）     │
    102（第 2 轮）     │
      │                │
      │ 1 对多         │
agent_checkpoints     │
    run 101 / v1      │
    run 101 / v2      │
    run 102 / v1      │
```

`agent_checkpoints` 至少需要绑定 `agent_run_id` 和 `conversation_id`，并保存版本、步骤、状态 JSON 或大结果引用。`agent_runs` 保存任务生命周期，例如 `pending`、`running`、`waiting_approval`、`succeeded`、`failed`、`canceled`；普通会话消息表保存面向用户的对话历史。三者职责不同，不能用一张表或一个 ID 混为一谈。

本项目中的 Go 接口也反映了这个边界：`GetLatestCheckpoint(ctx, agentRunID)` 用于当前 Run 恢复，`GetLatestThreadCheckpoint(ctx, conversationID)` 用于会话范围的状态查找；`Checkpoint` 结构同时携带 `AgentRunID` 与 `ConversationID`，既能精确恢复，也能校验它属于哪个会话。

## 与 DeerFlow 的对照

DeerFlow 通常用：

- `thread_id` 表示会话，语义接近本项目的 `conversation_id`；
- `run_id` 表示一次具体执行，语义接近本项目对外的 `run_id`；
- checkpoint 保存某次执行可恢复的状态，并通过 thread 组织多轮执行。

本项目还存在数据库内部的 `agent_runs.id`（数值主键），它主要用于数据库关联和 Worker 查询；它和对外展示的字符串 `run_id` 不是同一个字段，但都指向同一次 Run。这样既能让外部 API 使用稳定的公开 ID，也能让 PostgreSQL 外键和锁查询使用内部主键。

## 面试版回答
