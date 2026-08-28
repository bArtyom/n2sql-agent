# Agent Runtime Parity Design

## Status

Accepted for incremental implementation in the current Agent branch.

## Goal

把当前 Go Agent 从“可运行的 ReAct 循环”完善为具备 DeerFlow 核心工程特征的 Agent Runtime：可靠的事件游标、统一状态恢复、模型流式输出、轻量 Graph、通用子 Agent、durable interrupt、渐进式工具披露、可插拔记忆和完整运行观测。

## Scope

本设计只覆盖 Agent Runtime，不改变已有 RAG 检索算法和文档处理流程。当前 PostgreSQL 仍然是运行状态与持久化事件的事实来源，Redis 仍然是低延迟事件传输层。

## Decisions

### 1. Event contract

事件拆成两个层次：

- `EventID`：全局唯一，用于幂等去重；
- `Seq`：同一个 Agent Run 内严格递增，用于 Last-Event-ID 和数据库补发；
- `Category`：message、tool、reasoning、lifecycle、subagent；
- `TaskID`：区分父 Agent 和子 Agent；
- `Version`、`Type`、`Data`：保持稳定的版本化事件载荷。

事件顺序和 seq 由持久化层分配，不能由某个 Worker 的内存计数器分配。Redis 只负责实时 tail 和短期 replay，PostgreSQL 负责 durable replay。

### 2. One logical checkpoint

每个 run/thread 只有一套逻辑 AgentState。状态包含消息、摘要、当前节点、待执行工具、中断、子任务和最后事件游标。数据库可以物理拆分 blob，但上层只能通过一个 CheckpointStore 访问，并且所有写入都必须校验 `run_id + attempt_count + lease_token + expected_version`。

### 3. Streaming model boundary

增加带工具调用的模型流式接口。普通文本增量直接发布为 `message_delta`；工具调用参数只在累积为合法 JSON 后才进入 ToolNode。模型请求失败时只允许在还没有向客户端发出增量前切换备用 Provider。

### 4. Lightweight graph

不引入 Python/LangGraph。Go 侧提供最小 Node/Transition 接口，默认 ReAct 流程仍然是 Model → Tool → Model → Finish；审批、子 Agent、人工澄清和等待通过节点和持久化状态表达。

### 5. Subagents and interrupts

保留当前数据库 Worker 作为统一执行器。子 Agent 通过配置化 registry 选择模型、工具、Skill、步数和超时；`task` 不允许递归暴露给子 Agent。审批、澄清和子任务等待统一保存为 durable interrupt，Worker 重启后可以继续。

### 6. Memory and tools

Agent 只依赖 `MemoryProvider` 和 `ToolCatalog` 接口。默认实现继续使用 PostgreSQL 和当前 Go 工具；渐进式披露先支持目录查询和工具白名单，MCP 适配留在同一接口边界内。

### 7. Observability

每个 run、attempt、node、tool、model call 和 child task 共享 trace/run/task 标识，记录耗时、Token、成本、错误分类和 stop reason。事件正文与敏感输入分离，日志不写 Prompt、API Key 和完整文档内容。

## Non-goals

- 不把所有工具结果永久塞入 Redis；
- 不把原始思考链直接展示给用户，reasoning 只保留受控摘要；
- 不立即引入 Kafka、Temporal 等外部编排系统；
- 不为了兼容已经废弃的旧 checkpoint 表恢复旧接口。

## Verification

每个切片必须先有失败测试，再实现最小代码，完成后执行对应 Go 包测试、`go vet ./...`，最后执行完整 `go test ./...`。重点故障场景包括：Worker 接管、旧 Worker 写入、Redis gap、Checkpoint 写失败、模型中途断流、子 Agent 超时和等待状态恢复。
