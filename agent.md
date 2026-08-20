# Agent 开发原则

## 工具执行安全边界

- 普通工具，尤其是只读的 RAG、文档查询和检索工具，可以在明确失败后反馈给模型并允许再次尝试。
- 有副作用的工具，例如写文件、修改数据库、创建订单、发送消息或调用会改变外部状态的 API，必须实现 `RequiresApproval() bool { return true }`，执行前进入用户审批流程。
- 有副作用的工具默认不可自动重试。只有工具已经通过业务幂等键、外部状态查询或其他方式证明重复执行安全时，才额外实现 `Retryable() bool { return true }`。
- 工具结果为空，或执行错误但无法判断外部操作是否已经完成时，按“不确定状态”处理；有副作用工具直接结束本轮并要求确认，不要盲目再次执行。
- checkpoint 只负责记录 Agent 从哪一步继续，不能撤销已经发生的外部副作用；断点续跑必须同时遵守工具的审批和幂等边界。

## 新工具接入清单

1. 判断工具是否只读、是否会改变外部状态。
2. 只读工具保持默认可重试；副作用工具实现 `RequiresApproval()`。
3. 如果副作用工具具备可靠幂等保障，再实现 `Retryable()`，并说明幂等键和外部状态依据。
4. 为审批前不执行、失败不自动重试、显式幂等后允许重试分别补测试。

## Worker 并发通信要点

- `context.Context` 负责控制执行生命周期：父 Agent、子 Agent、模型调用和工具调用共享或派生 context；用户取消、超时或租约续期失败时，通过 `cancel()` 让执行链路停止。
- `channel` 负责 goroutine 之间传递结果和事件：例如 Agent 通过 `agentDone <- err` 报告执行结果，事件通过 channel 交给 SSE 或事件 Hub 转发。
- Worker 通常由执行 goroutine 和心跳 goroutine 协作：执行 goroutine 处理模型/工具，心跳 goroutine 定期续租；心跳失败时取消执行 context，执行结束后再取消并等待心跳 goroutine 退出。
- `context` 解决“是否停止”，`channel` 解决“传递什么”；二者只负责进程内协作，任务最终状态仍必须写入 PostgreSQL，才能支持 Worker 崩溃后的接管和恢复。

## Git 远程推送规则

- 不使用定时任务自动推送。
- 每完成 3 次本地 commit 后，推送一次当前分支到 `origin`。
- 推送前只执行 `git push origin HEAD`，不自动 `add`、`commit`、`reset` 或处理用户未提交的改动。
- 用户明确要求立即推送时，可以提前执行一次推送；未提交的 `README.md`、`agent.md` 和学习记录不自动纳入推送。
