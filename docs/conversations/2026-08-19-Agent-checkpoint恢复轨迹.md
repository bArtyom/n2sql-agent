# Agent checkpoint 恢复轨迹

## 为什么要发恢复事件

断点续跑发生在 Worker 接管时。如果只重建模型上下文而不发事件，前端在 Redis 事件缺口后可能只看到后续答案，不知道中间的工具结果来自 checkpoint，排查时也无法区分“工具重新执行”和“结果复用”。

## 当前行为

Worker 接管后，Engine 对每个恢复的安全工具发送：

```json
{
  "tool_name": "knowledge_search",
  "checkpoint_action": "resumed_context",
  "resumed_from_checkpoint": true
}
```

同时把恢复的工具参数放进本轮重复调用集合。如果模型再次生成完全相同的工具调用，会触发已有的重复调用保护，不会再次执行或再次消耗 checkpoint。

## 结果

恢复链路现在同时具备三部分：

1. checkpoint 结果重新进入模型上下文；
2. 前端/日志可以看到恢复轨迹；
3. 已恢复调用不会在同一轮被再次重复执行。

## 单轮恢复数据与多轮会话历史的边界（2026-08-28）

### 用户问题

单轮 Agent 执行时 Worker 崩溃，中间过程从哪里恢复？如果工具记录不存在怎么办？第一轮结束后，第二轮是否会保留第一轮的工具调用和工具结果？

### 核心结论

单轮 Run 执行期间，恢复信息分为三类：

```text
agent_run_decisions   → 模型已经做出的工具调用决策
agent_run_checkpoints → 已完成且允许安全复用的工具结果
agent_run_contexts    → 最近模型消息、工具结果和旧消息摘要
```

Worker 崩溃后，租约过期，下一次 Worker 读取这些数据：

```text
有 decision，没有 tool result
    → 复用模型决策，重新执行未完成工具

有 tool checkpoint，没有 context
    → 根据工具名和参数哈希重建 assistant/tool 消息

有 context
    → 直接恢复最近模型上下文

两者都没有
    → 只读工具允许重新检索；有副作用工具不能盲目重试，必须确认外部状态或幂等结果
```

普通会话历史和 Agent 恢复上下文不是同一个东西。第一轮 Run 结束后，会清理该 Run 的 context、tool checkpoint 和 decision checkpoint；普通会话表仍然保留：

```text
用户：公司的年假怎么算？
助手：公司的年假按照累计工作年限计算……
```

第二轮只读取这类用户可见历史，并创建属于新 Run 的恢复数据。第一轮内部的 `knowledge_search`、`document_read` 以及完整工具结果不会作为普通多轮历史永久注入。这样可以避免上下文膨胀和工具结果过期；第二轮需要事实资料时重新检索。

```text
用户可见历史：问题 A → 回答 A → 问题 B → 回答 B
单轮内部过程：问题 A → 工具调用 → 工具结果 → 回答 A（Run 结束后清理恢复数据）
```
