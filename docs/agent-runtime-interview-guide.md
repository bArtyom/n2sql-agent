# n2sql-agent Agent Runtime 面试与项目讲解

> 本文是当前项目 Agent 主线的学习和面试说明。项目参考 DeerFlow 的运行时思想，结合 Go、PostgreSQL、Redis Stream 和现有 RAG 能力实现；不是把所有逻辑堆在 HTTP Handler 中，也不是把原始思考链或完整工具结果暴露给浏览器。

## 1. 一分钟介绍

这是一个面向文档知识库的 Agent + RAG 问答系统。用户先创建或选择一个 `conversation_id` 作为多轮会话，再在这个会话下提交问题；每个问题都会创建一个独立的 `agent_run`，用 `run_id` 标识这一轮执行。HTTP 层只负责校验请求、创建 `agent_run` 并返回 `run_id`，真正的 ReAct 执行交给后台 Worker。Worker 使用数据库队列领取任务，通过模型的 Function Calling 决定是否调用知识库检索、文档读取或其他安全工具；工具结果作为不可信观察反馈给模型，模型再决定继续调用工具还是生成答案。

运行过程具有流式事件、统一 checkpoint、租约续期、Worker 接管、工具失败自修正、审批中断、子 Agent、运行预算和错误分类。PostgreSQL 保存需要恢复和审计的事实，Redis/Hub 只负责低延迟事件传输和短期回放。最终普通会话只保存用户问题和最终助手回答，内部工具过程保存在 Agent Run 的运行轨迹和统一 checkpoint 中。

面试时可以概括为：

> “我的 Agent 是一个数据库驱动的异步 ReAct Runtime。用户在一个 `conversation_id` 下提交问题，每条问题创建一个 `run_id` 对应的 pending Run；Worker 用 `FOR UPDATE SKIP LOCKED` 领取并获得租约。Engine 通过 Function Calling 在 ModelNode 和 ToolNode 之间循环。每个模型决策、工具结果和中断边界写入一个版本化 checkpoint，事件同时进入持久化事件日志和 Redis/Hub，由 SSE 用游标续传。Worker 崩溃后，新的 Worker 通过租约回收和 checkpoint fencing 接管，从 pending tool call 或当前节点继续。普通工具失败会把错误反馈给模型重试，副作用工具必须审批并遵守幂等边界。”

## 2. 总体架构

```text
浏览器
  │ 先创建/选择 conversation_id
  │ POST /agent-chat/stream {conversation_id, message}
  ▼
HTTP Handler
  ├─ 校验知识库范围、模型、附件、幂等键
  ├─ INSERT agent_runs(status=pending, conversation_id, request snapshot)
  └─ 202 {run_id, stream_url}

浏览器 ── GET /agent-runs/{run_id}/stream ──► SSE Handler
           （只订阅这一轮 Run；会话关系由 run_id → agent_runs.conversation_id 解析）
                                               │
                                               ├─ Redis Stream / Hub：实时事件、短期回放
                                               └─ PostgreSQL event journal：可靠补发

共享 Worker Pool
  ├─ Worker A: ClaimNext → lease → Execute Agent Run
  ├─ Worker B: ClaimNext → 可执行另一个用户的 Run
  └─ Worker C: 过期租约回收后接管 Run
              │
              ▼
       Agent Engine / ReAct Loop
          ├─ Model：Function Calling + 流式文本
          ├─ Tool：知识库检索、文档工具、外部工具
          ├─ Child Agent：共享受限 Scheduler 的独立 Run
          ├─ Checkpoint：一个统一可恢复状态
          └─ Events：run/tool/reasoning/message/child/interrupt
              │
              ├─ agent_runs.response：这一轮 Run 的最终结果
              ├─ conversation_messages：同一个 conversation_id 下的用户问题 + 最终回答
              └─ agent_checkpoints：恢复所需隐藏状态
```

关键边界是：

| 边界 | 负责什么 | 不负责什么 |
| --- | --- | --- |
| HTTP Handler | 校验、入队、SSE 转发 | 不长时间执行模型和工具 |
| Worker | 领取、租约、执行、状态收口 | 不把 HTTP 连接当作任务生命周期 |
| Agent Engine | ReAct 决策、工具调用、预算、checkpoint 边界 | 不直接操作浏览器 |
| PostgreSQL | Run、Attempt、Checkpoint、最终答案、事件日志 | 不承担低延迟广播 |
| Redis/Hub | 实时事件、短期 replay、订阅者广播 | 不是最终状态事实来源 |
| SSE Handler | 把事件按游标发给浏览器 | 不决定 Agent 下一步 |

### 2.1 `conversation_id`、`run_id` 和 `execution_id` 的关系

这三个 ID 处在不同层级，不能互相替代：

| ID | 表示什么 | 生命周期 | 例子 |
| --- | --- | --- | --- |
| `conversation_id` | 一整段多轮会话，也就是用户在界面里看到的一张会话卡片 | 从创建会话开始，直到会话被删除 | `42` |
| `run_id` | 会话中的一轮 Agent 执行，通常对应用户提交的一条问题 | 这一轮从 `pending` 到终态；Worker 重试时仍然不变 | `run-101` |
| `execution_id` | 某个 `run_id` 被某次 Worker 领取执行的尝试 | 每次重新领取都会生成新的值 | `run-101-attempt-2-...` |
| `agent_runs.id` | PostgreSQL 内部主键 | 数据库内部使用，不作为前端公开运行标识 | `101` |

例如：

```text
conversations
  id=42  title="员工手册问答"

agent_runs
  id=101  run_id=run-101  conversation_id=42  message="年假怎么计算？"
  id=102  run_id=run-102  conversation_id=42  message="试用期也适用吗？"
```

两条 Run 属于同一个多轮会话，但各自有独立的 SSE 事件、状态、租约和 checkpoint。`run-101` 如果因 Worker 崩溃被接管，仍然是 `run-101`；只会更换 `execution_id` 和租约，不会创建新的会话，也不会变成 `run-102`。

当前事件信封以 `run_id` 作为实时订阅范围，通常不在每一条事件中重复携带 `conversation_id`。需要知道会话归属时，SSE Handler 或状态接口通过 `agent_runs.run_id → agent_runs.conversation_id` 查询；像 `conversation_saved` 这类会话收口事件则会显式带上 `conversation_id`。这样事件游标只需要处理当前 Run，不会把同一会话的多轮事件混在一个流里。

## 3. 与 DeerFlow 思路对应的十个能力

### 3.1 稳定事件契约和游标

事件包含稳定的 `version`、`id`、`run_id`、`type`、`category`、`seq`、`execution_id` 和 `trace_id`。其中：

- `id` 用于事件幂等去重；
- `seq` 是持久化事件日志中的递增游标；
- `execution_id` 区分同一个 Run 被不同 Worker 执行的第几次尝试；
- `category` 让前端按 `lifecycle/tool/output/control` 分类，不必解析事件名称猜含义；
- `version` 为以后增加字段或改变语义预留兼容边界。

事件不是由某个 Worker 的内存计数器决定顺序，而是追加到 PostgreSQL 后使用事件表的稳定 ID 作为 durable sequence。这样 Worker A 崩溃、Worker B 接管后，新事件仍然可以和旧事件放进同一个有序日志。

### 3.2 SSE 断线和 Redis gap

浏览器收到事件后记住 `Last-Event-ID`，例如最后收到 `run-1-exec-a-3`。重连时把它放在 HTTP Header 中。

SSE Handler 的处理顺序是：

1. 先把事件 ID 映射为 PostgreSQL durable `seq`；
2. 先尝试从持久化事件日志 `ListAfter(seq)` 补发缺失事件；
3. 再订阅 Hub/Redis 的实时尾部；
4. 使用事件 ID 去重，避免“补发最后一条”和“实时订阅第一条”重复；
5. 如果 Redis 短期事件已过期，返回类型化 `stream_replay_gap`。

如果 Run 已进入终态，前端读取 Run 状态和 `response` 恢复最终答案；如果 Run 仍在运行，则丢弃过期游标、订阅当前尾部，继续接收后续事件。中间事件不是最终事实，最终状态和答案不会因为 Redis 过期而丢失。

### 3.3 一个统一 checkpoint

逻辑上每个 Run/Thread 只有一个 `AgentState`，当前实现通过 `agent_checkpoints` 表保存它。这里的 Thread 就是由 `conversation_id` 标识的多轮会话；当前正在执行的轮次则由 `agent_run_id`/`run_id` 标识。它不是“上下文 checkpoint 一张表 + 工具 checkpoint 另一张表”，而是一份完整快照，里面可以同时有：

```json
{
  "version": 7,
  "last_step": 3,
  "current_node": "tool",
  "messages": [
    {"role":"user", "content":"年假怎么计算？"},
    {"role":"assistant", "tool_calls":[{"id":"call-1","name":"knowledge_search"}]}
  ],
  "summary_text": "已检索员工手册，尚未生成最终答案。",
  "pending_tool_calls": [
    {"id":"call-1","name":"knowledge_search","arguments":"{\"query\":\"年假\"}"}
  ]
}
```

`conversation_id` 通常保存在 checkpoint 表的关系字段中，而不是重复塞进 `state` JSON：

```text
agent_checkpoints
  agent_run_id=101
  conversation_id=42
  checkpoint_id="run-101-unified"
  state={current_node:"tool", pending_tool_calls:[...]}
```

因此，Worker 接管当前 Run 时按 `agent_run_id` 读取；新一轮 Run 没有自己的恢复 checkpoint 时，才可以按 `conversation_id` 找到该会话最近的线程 checkpoint。两种读取场景都不会把不同会话的状态混在一起。

保存边界包括：

- 下一次模型调用前：保存当前消息和压缩后的摘要；
- 模型返回工具调用后：保存 `current_node=tool` 和 `pending_tool_calls`；
- 工具结果写回后：保存工具观察，并清除已经完成的 pending call；
- 审批/等待子任务时：保存 `interrupt` 或等待节点；
- 最终答案形成时：保存 `current_node=finish` 和答案消息。

checkpoint 写入必须通过 `lease_token + expected_version` fencing。旧 Worker 即使网络延迟恢复，也不能覆盖新 Worker 的状态。如果 checkpoint 写失败，Engine 会在继续下一次模型或工具副作用前停止本轮，并把失败交给 Worker 收口；不能假装已经有恢复点。

大工具结果不必全部塞进 JSONB：状态中保存受限正文或临时文件引用，临时文件由 blob store 清理。真正重启恢复所需的是消息边界、工具调用 ID、参数哈希和外部引用，而不是无限大的完整结果。

### 3.4 模型流式和 Function Calling

工具定义放在模型请求的独立 `tools` 字段，不作为历史消息追加。每轮请求大致是：

```json
{
  "messages": [
    {"role":"system","content":"你是知识库助手……"},
    {"role":"user","content":"年假怎么算？"}
  ],
  "tools": [
    {
      "type":"function",
      "function":{
        "name":"knowledge_search",
        "description":"在当前知识库检索资料",
        "parameters":{"type":"object","properties":{"query":{"type":"string"}}}
      }
    }
  ],
  "tool_choice":"auto"
}
```

模型可以返回两类内容：

- 文本 delta：立即变成 `message_delta`，SSE 实时显示；
- tool-call delta：参数 JSON 可能被拆成多段，后端先按 `tool_call_id/index` 累积，只有完整 JSON 校验通过后才执行工具。

工具执行结果不会伪装成可信指令，而是以 `UNTRUSTED_TOOL_RESULT` 形式加入当前轮上下文，再交给模型判断下一步。原始隐藏思考链不写入会话历史；当前 UI 只接收受限的 reasoning 摘要事件。

### 3.5 轻量 Agent Graph

项目没有引入完整 Python/LangGraph 编排框架，而是在 Go 中定义了最小 `Node`、`Transition` 和 `Graph`：

```go
type Node interface {
    Name() string
    Run(context.Context, *AgentState) (Transition, error)
}

type Transition struct {
    NextNode string
    Halt     bool
}
```

默认 ReAct 仍然是：

```text
model → tool → model → tool → finish
```

Engine 使用同一份运行时 Graph 注册 `model`、`tool`、`finish` 和 `interrupt` 四类持久化节点。Graph 在恢复前校验 `CurrentNode`，防止损坏或过期游标被误当成模型步骤；真正的模型请求和工具副作用仍由 Engine 节点逻辑执行，Graph 不会替代这些副作用。

审批、等待子 Agent、未来的澄清问题都可以成为持久化节点，而不是在一个长函数中阻塞。`CheckpointState.CurrentNode` 让新 Worker 能从 `tool` 或 `interrupt` 节点继续，而不是从头执行已经完成的模型决策。等待子 Agent 时，统一 checkpoint 额外保存 `interrupt.kind=children`、原工具调用 ID 和 `child_run_ids`；子 Run 全部进入终态后，父 Run 才回到 `pending`，下一个 Worker 使用同一个幂等 child run ID 读取结果，不会重复创建子任务。

### 3.6 Run 级预算和停止原因

单次模型请求的 `max_completion_tokens` 只限制这一调用的输出；Agent 还需要 Run 级总预算：

```go
type RunBudget struct {
    MaxModelCalls  int
    MaxToolCalls   int
    MaxTotalTokens int
}
```

默认值是模型调用 16 次、工具调用 32 次、总 Token 100000。达到预算后，Run 以 `step_limit` 停止并写入 `stop_reason`。此外还区分 `model_error`、`tool_error`、`timeout`、`canceled`、`validation_error` 和 `internal_error`，运维和前端可以知道“为什么停”，而不只得到一个 `agent chat failed`。

### 3.7 配置化子 Agent

父 Agent 通过 `delegate_research` 创建独立 child Run。子 Agent 不是简单的匿名 goroutine，而有自己的：

- `run_id`、`parent_run_id` 和 `run_kind=child`；
- checkpoint、attempt、租约和超时；
- 子 Agent 名称、System Prompt、模型、工具白名单和最大步数；
- 父 Run 事件中的 `child_run_id`、状态和受限摘要。

子 Agent 默认继承父 Agent 允许的外部工具，但明确排除 `delegate_research`、`task`、`create_subagent`，避免递归创建失控。共享 `BoundedChildScheduler` 控制所有父 Run 的子任务总并发，而不是每个父 Agent 私自创建一套槽位。

选择原则是：简单任务父 Agent 自己做；互相独立的研究任务并发 child；有前后依赖的任务由父 Agent 串行推进；需要隔离上下文的复杂任务交给一个 child 内部完成。

### 3.8 Durable Interrupt / Human-in-the-loop

副作用工具执行前，Engine 先写入统一 checkpoint：

```json
{
  "current_node":"interrupt",
  "interrupt": {
    "kind":"approval",
    "id":"run-1:approval:call-9",
    "tool_call_id":"call-9",
    "tool_name":"write_file",
    "arguments":"{...}"
  }
}
```

随后 Run 变成 `waiting_approval`，释放 Worker 租约，不占用 goroutine 等用户点击。用户批准接口在事务中锁定 Run 和统一 checkpoint：清除 interrupt、记录 `approved_tool_call_ids`、版本加一、Run 回到 pending。下一个 Worker 领取后看到已批准的调用，只执行一次，不重复弹窗；拒绝则把“用户拒绝”作为工具观察反馈给模型。

因此审批状态不依赖某个进程内 Hub，也不怕原 Worker 崩溃或服务重启。

### 3.9 Tool Catalog 和 Memory Provider

工具通过 `ToolCatalog` 做元数据发现和实际解析，Agent 可以先知道工具名称、描述、参数，再按当前权限注册真正可用的工具。真正的 Tool Registry 仍负责：

- 是否允许暴露；
- 是否需要审批；
- 是否可以重试；
- 是否支持并行；
- 参数校验和结果脱敏。

记忆通过 `MemoryProvider` 接口解耦：

```go
type Provider interface {
    Add(context.Context, Scope, string) error
    GetContext(context.Context, Scope) (Context, error)
    Search(context.Context, Scope, string, int) ([]string, error)
    Update(context.Context, Scope, string, string) error
    Delete(context.Context, Scope, string) error
}
```

当前默认实现使用 PostgreSQL。每轮模型请求动态构造 system prompt，把用户级长期记忆作为一段受控上下文注入；工具定义仍然放独立 `tools` 字段。这样以后可以替换为 DeerMem、Mem0、向量记忆或文件后端，上层 Engine 不需要改变。

### 3.10 观测、故障注入和安全日志

每个请求通过 `trace_id` 关联，Worker 执行再附加 `run_id`、`task_id`、`execution_id` 和 `attempt`。当前记录的低基数信息包括：

- HTTP 请求数量、4xx/5xx 和耗时；
- Agent Run 成功、失败、取消、超时、步数、工具调用数和总 Token；
- Worker 领取失败、重试、死信和任务耗时；
- 模型调用成功率、错误分类、耗时、Prompt/Completion/Total Token、熔断和 fallback；
- 队列深度；
- model/tool/checkpoint 等语义阶段的 trace 日志。

日志不会写 API Key、完整 Prompt、完整文档和完整工具参数。Engine 提供测试专用 `FaultInjector`，可以在 `before_model`、`after_tool_checkpoint` 等边界模拟崩溃，验证 Worker 接管和断点续跑，而不需要真的杀掉整个测试进程。

## 4. 一次完整对话示例

问题：用户在会话 `conversation_id=42` 中问“请总结《员工手册》里的年假规定”。

如果是新会话，前端先创建一条 `conversations` 记录并得到 `conversation_id=42`；如果是已有会话，则直接复用这个 ID。它代表整段多轮对话，不会随着每个问题变化。

### 第一步：提交

HTTP Handler 接收 `conversation_id=42`，校验用户的知识库权限、消息、模型和附件，生成本轮 `run_id=run-101`，写入：

```http
POST /api/knowledge-bases/7/agent-chat/stream
Content-Type: application/json

{"conversation_id":42,"message":"请总结《员工手册》里的年假规定"}
```

```text
conversations
  conversation_id=42
  knowledge_base_id=7
  title="员工手册问答"

agent_runs
  id=101
  run_id=run-101
  conversation_id=42
  status=pending
  attempt_count=0
  request={消息、知识库范围、模型、预算}
```

返回 `202` 和 `run_id=run-101`，浏览器随后连接 `/agent-runs/run-101/stream`。SSE 连接跟踪的是本轮 `run_id`；它属于哪个多轮会话，由 `agent_runs.conversation_id=42` 关联。

### 第二步：领取和租约

Worker 执行 `ClaimNext`。数据库事务用 `FOR UPDATE SKIP LOCKED` 锁住一条 pending Run，写入：

```text
status=running
attempt_count=1
lease_token=lease-a
lease_until=未来 5 分钟
execution_id=run-101-attempt-1-...
conversation_id=42
```

另一个用户的 Run 可以被 Worker B 同时领取。当前 Worker 还启动心跳，定期用同一个 `lease_token` 续租；续租失败就取消执行 context。注意，`conversation_id=42` 只是说明这轮 Run 属于哪段会话，不是 Worker 领取任务的唯一键；Worker 领取和恢复使用 `run_id`/数据库主键。

### 第三步：第一次模型决策

Engine 组装 system prompt、用户问题、长期记忆和工具定义，调用聊天模型。模型返回：

```json
{
  "tool_calls":[
    {"id":"call-search-1","name":"knowledge_search","arguments":"{\"query\":\"年假规定\"}"}
  ]
}
```

此时发布 `tool_called`，并把 `pending_tool_calls` 写入统一 checkpoint。若此刻 Worker 崩溃，Worker B 不会再次让模型猜一次，而是读取这个 pending call。

### 第四步：执行工具

`knowledge_search` 在当前知识库范围内做向量、关键词和必要的图谱/图片召回，返回若干受限资料。工具结果经过脱敏、截断、引用提取后：

- 发布 `tool_finished`，只带工具名、摘要、引用和统计；
- 以不可信 tool message 加入内存中的当前轮消息；
- checkpoint 更新为 `pending_tool_calls=[]`，`current_node=model`。

如果普通工具失败，Engine 将错误作为观察反馈给模型，例如“检索接口超时，请改写查询或直接说明无法检索”，模型可以换参数再试。如果是写入文件、修改数据库等副作用工具，则先进入 approval，不自动重复执行。

### 第五步：第二次模型决策和流式回答

模型根据检索结果生成文本，响应中的文本 delta 逐段发布：

```text
message_delta: “根据员工手册，”
message_delta: “年假天数按入职年限计算……”
```

同时 Engine 记录最终 `assistant` 消息和 `current_node=finish` checkpoint。模型输出为空、达到预算或持续重复调用工具时，Run 会使用结构化 stop reason 结束。

### 第六步：持久化和收口

最终响应写入：

```text
agent_runs.response
  {answer, sources, stats, trace, execution_id}

conversation_messages
  conversation_id=42  role=user       content=“请总结《员工手册》里的年假规定”
  conversation_id=42  role=assistant   content=“根据员工手册，……”
```

普通会话表不保存每一个 tool call。`agent_run_events` 保存可审计的事件日志，`agent_checkpoints` 保存最后的隐藏运行状态；这两者都能通过 `agent_run_id` 找到本轮，并通过 `conversation_id=42` 找到所属会话。Run 标记 `succeeded`，释放租约，Hub/Redis 流结束。

### 第七步：同一个会话的下一轮

用户继续在原来的会话中提问：

```http
POST /api/knowledge-bases/7/agent-chat/stream
Content-Type: application/json

{"conversation_id":42,"message":"那试用期员工也适用吗？"}
```

这次不会复用 `run-101`，而是创建新的 Run：

```text
agent_runs
  id=102
  run_id=run-102
  conversation_id=42
  status=pending
```

Worker 执行 `run-102` 时，按照 `conversation_id=42` 读取该会话已经完成的用户消息和助手回答，并把当前问题追加到本轮上下文。如果需要恢复的是 `run-102` 自己的中断或崩溃，则优先读取 `agent_run_id=102` 的 checkpoint；只有新一轮没有自己的恢复状态时，才使用该会话最近的线程级 checkpoint 作为隐藏运行上下文。完成后，新的 user/assistant 交换仍然写入 `conversation_id=42`，因此一段会话可以包含多个 Run。

```text
conversation_messages（conversation_id=42）
  1  user      请总结《员工手册》里的年假规定
  2  assistant 根据员工手册，……
  3  user      那试用期员工也适用吗？
  4  assistant 试用期员工……
```

上一轮的内部 `tool_called`、`tool_finished` 事件不会变成这张普通聊天表中的消息；它们仍通过上一轮的 `run_id=run-101` 关联到运行事件和 checkpoint。这样既能保留多轮对话语义，又不会把不同轮次的实时 SSE 事件混成一条流。

## 5. Worker 崩溃、重试和恢复示例

假设 Worker A 已完成知识库检索，checkpoint 是：

```json
{
  "run_id": "run-101",
  "conversation_id": 42,
  "version": 4,
  "current_node": "model",
  "messages": [
    {"role":"user","content":"总结年假"},
    {"role":"assistant","tool_calls":[{"id":"call-1","name":"knowledge_search"}]},
    {"role":"tool","tool_call_id":"call-1","content":"UNTRUSTED_TOOL_RESULT\n……"}
  ],
  "pending_tool_calls": []
}
```

上面 JSON 为了便于理解把两个关联 ID 一起展示；实际实现中，`run_id` 和 `conversation_id` 主要由 `agent_runs`/`agent_checkpoints` 的列保存，`state` JSON 重点保存可恢复的 Agent 状态。

Worker A 在下一次模型请求前崩溃：

1. 心跳停止，`lease_until` 到期；
2. 回收逻辑 `RequeueExpired` 把 `running` 改为 `pending/requeued`，增加下一次尝试记录；
3. Worker B 用新的 `lease_token=lease-b` 领取；
4. B 读取 `agent_runs` 中的 `conversation_id=42`、同一个 `agent_checkpoints` 的最新有效版本和请求快照；
5. 因为 `pending_tool_calls` 为空，B 不重复知识库检索，直接将已有工具结果送进模型继续决策；
6. B 写 checkpoint 时携带 `lease-b` 和期望版本 4。旧 A 如果延迟回来写入，会被 `ErrCheckpointConflict` 拒绝。

如果 A 是在工具调用已经发出、但工具结果尚未 checkpoint 前崩溃：

- 普通只读工具可能被重新调用一次；因此工具实现需要尽可能幂等，或者通过参数哈希/结果缓存避免重复；
- 副作用工具不能直接盲目重试，必须审批、查询外部状态或使用业务幂等键；
- checkpoint 不能撤销外部副作用，所以安全边界优先于“绝不重复”。

模型 API 的一次请求则有独立的最多三次调用策略：超时、429、网络异常和 5xx 使用指数退避加随机抖动；认证失败、参数错误和权限不足不重试。Provider 连续失败后才按配置尝试备用 Provider。Worker 级执行失败另有 attempt 历史和租约恢复，两者不是同一个重试层。

## 6. 哪些数据存在哪里

### PostgreSQL：事实来源

| 表/字段 | 保存内容 | 用途 |
| --- | --- | --- |
| `agent_runs` | `run_id`、`conversation_id`、request snapshot、状态、attempt、租约、错误、stop reason、最终 response | Worker 领取、按会话关联各轮 Run、状态查询、最终答案恢复 |
| `agent_run_attempts` | 每次 Worker 尝试的开始/结束、错误和状态 | 解释重试和崩溃恢复 |
| `agent_checkpoints` | 一个统一 AgentState 的版本快照，以及所属 `agent_run_id`/`conversation_id` | Worker 接管当前 Run、按会话加载线程状态、pending tool、审批和上下文恢复 |
| `agent_run_events` | 版本化事件和 durable seq | Redis gap 后补发、审计和调试 |
| `conversation_messages` | `conversation_id` 下的用户消息和最终助手回答 | 多轮对话历史；同一个会话通过这个 ID 串起多个问题和回答 |
| memory 表 | 用户级长期记忆/偏好 | 新对话注入或按需检索 |

### Redis/Hub：短期事件层

保存或转发 `run_started`、`tool_called`、`tool_finished`、`reasoning_delta`、`message_delta`、`run_finished` 等短期运行事件。它们给实时 SSE 使用，设置 TTL/长度上限，过期是允许的；过期后由 PostgreSQL 的事件日志或最终 `response` 兜底。Hub 是进程内广播器，Redis 是跨进程事件桥，不是通用 goroutine 总线。

### 临时文件/Blob Store：大对象

大工具结果、图片和大解析产物不全部堆在 Worker 内存或 Redis。checkpoint 里只保留受限内容和外部引用；任务完成或失败清理时删除临时对象。需要长期保留的原始文件应走对象存储抽象，而不是依赖某个 Worker 的本地临时目录。

## 7. 工具安全和错误自修正

工具策略是后端事实，不由模型或前端决定：

```text
只读工具
  参数校验 → 调用 → 失败观察 → 模型改参数/换工具/回答

副作用工具
  参数校验 → durable approval → 用户批准
  → 幂等键/外部状态检查 → 执行 → checkpoint
```

“错误交给模型重新判断”不是把所有异常无限重试。模型能修复的是查询词错误、参数缺失、工具不存在或检索结果为空；认证失败、权限不足、不可逆外部副作用和未知执行状态必须在后端停止。这样既保留 Agent 的自修正能力，也不会把安全策略交给模型。

## 8. 子 Agent 的并发模型

Go 的 Worker 池和 child scheduler 是两层并发：

```text
进程内共享 Agent Worker Pool（例如 5 个槽位）
  ├─ Worker 1 执行用户 A 的父 Run
  ├─ Worker 2 执行用户 B 的父 Run
  └─ Worker 3 执行用户 A 的 child Run

进程内共享 BoundedChildScheduler（例如 3 个槽位）
  ├─ 父 Run A 的 child-1
  ├─ 父 Run A 的 child-2
  └─ 父 Run B 的 child-1
```

每个正在执行的 Run 有自己的 context、租约、checkpoint 和事件身份。goroutine 不是线程池中的永久线程：任务完成后 goroutine 退出，新的任务由 Worker 循环或 scheduler 槽位继续消费。`context` 负责取消，`channel` 负责 goroutine 间传结果，Hub 只负责向多个 SSE 订阅者广播运行事件。

## 9. 严格知识库模式和 RAG

Agent 不是“永远只会搜索一次”的固定链路。默认 `knowledge_base_preferred` 允许先查知识库，必要时由 Agent 继续检索或使用后端允许的其他工具；`knowledge_base_only` 要求最终回答必须有知识库证据，没有命中就输出严格拒答。

知识库工具内部是 RAG 子系统：

```text
问题
  ├─ 向量召回
  ├─ 关键词/标题路径召回
  ├─ 摘要、图片和可选 GraphRAG 召回
  ├─ Hybrid/RRF 融合
  ├─ 可选 Rerank + MMR
  └─ 引用和受限上下文
```

Agent 的作用是决定何时调用、是否改写问题、是否需要第二次检索和如何组织回答；RAG 的作用是提供候选证据、分数和引用。严格拒答在后端根据“是否存在有效知识库证据”判断，不能只相信模型说自己查到了。

## 10. 代码导航

- ReAct Engine、模型/工具循环、checkpoint 边界：`internal/agentruntime/engine.go`
- 统一 AgentState 和上下文压缩：`internal/agentruntime/context_state.go`
- 轻量 Graph：`internal/agentruntime/graph.go`
- 子 Agent Registry 和委派：`internal/agentruntime/subagent_registry.go`、`delegate_research_tool.go`
- Agent Run 状态、租约、attempt、checkpoint：`internal/agentrun/run.go`
- Worker 领取、心跳、错误收口：`internal/agentrun/worker.go`
- 事件持久化和 durable cursor：`internal/agentrun/events.go`
- SSE/Last-Event-ID/gap 恢复：`internal/handler/knowledge_base_agent_chat_stream.go`
- 异步提交和执行适配：`internal/handler/agent_run_worker.go`
- Tool Registry/Catalog：`internal/agent/registry.go`、`internal/agent/tool_catalog.go`
- Memory Provider：`internal/memory/provider.go`
- 错误分类、退避和熔断：`internal/ops/failure.go`、`internal/ops/circuit_breaker.go`
- 运行指标和 trace：`internal/metrics/metrics.go`、`internal/ops/trace.go`
- 统一 checkpoint 迁移：`internal/database/migrations/sql/000079_unify_agent_checkpoints.up.sql`

## 11. 面试追问简答

### 为什么不能只把状态放 Redis？

Redis 适合实时事件和短期回放，但 TTL、内存容量和故障策略不适合作为唯一事实来源。Run 状态、最终答案、恢复 checkpoint 和 durable event journal 放 PostgreSQL；Redis 过期只会导致 SSE gap，不会丢最终答案。

### 为什么需要 `FOR UPDATE SKIP LOCKED`？

多个 Worker 同时找 pending 任务时，`FOR UPDATE` 锁住候选行，`SKIP LOCKED` 让其他 Worker 跳过已被锁定的行，分别领取不同任务，不互相等待，也不会重复消费同一个 Run。

### checkpoint 和普通聊天历史有什么区别？

`conversation_id` 标识多轮会话，普通聊天历史面向用户，查询这个 ID 得到该会话中的用户问题和最终助手回答；`run_id` 标识其中某一轮执行，checkpoint 面向运行时，包含该 Run 尚未结束的工具调用、观察、压缩摘要、当前节点和中断信息。Run 结束后 checkpoint 仍可以作为该 `conversation_id` 对应 Thread 的隐藏恢复状态，但不会原样展示给用户。

### `conversation_id` 和 `run_id` 有什么区别？

`conversation_id` 是会话级 ID，表示用户在界面中打开的整段多轮对话；`run_id` 是执行级 ID，表示这段会话中的某一轮 Agent 任务。比如会话 `42` 先问“年假怎么计算”，创建 `run-101`；随后再问“试用期适用吗”，同一个会话 `42` 会创建新的 `run-102`。如果 `run-101` 的 Worker 崩溃，Worker B 接管的仍是 `run-101`，不会创建 `run-102`；只有用户真正发起下一条问题时才创建新的 Run。`execution_id` 则进一步标识某个 Run 的第几次 Worker 尝试。

面试时可以这样回答：

> “我把会话和执行分开建模。`conversation_id` 串起一段多轮聊天，负责关联会话标题、普通消息和线程级上下文；`run_id` 表示其中一轮异步 Agent 执行，负责状态、SSE、租约和 checkpoint。一个 conversation 可以有多个 run，同一个 run 发生 Worker 重试时 run_id 不变，只更换 execution_id 和 lease token。”

### Worker 崩溃后怎样避免旧 Worker 覆盖新状态？

领取时生成新 `lease_token`，写 checkpoint、续租、成功和失败状态时都必须匹配 token；checkpoint 还比较 `expected_version`。旧 Worker 的写入会得到 lease/version conflict。

### 为什么副作用工具必须审批？

模型可能重试、Worker 可能接管、网络可能在执行后才断开。只要外部状态不确定，自动重试就可能重复扣款、写文件或发消息。审批和幂等键让执行边界变得明确；checkpoint 只能恢复流程，不能撤销已经发生的副作用。

### Agent 的 Token 限制有哪两层？

Provider 请求里的 `max_completion_tokens` 限制一次模型输出；RunBudget 限制整轮 Agent 的模型调用次数、工具调用次数和累计 Token。两者一起防止单次输出过大和 ReAct 循环无限增长。

### SSE 断线时为什么不一定补全所有中间事件？

中间事件主要用于实时展示，不是业务事实。系统优先从 durable event journal 补发；如果事件日志或 Redis 窗口无法覆盖游标，且 Run 已终止，就恢复最终答案；若仍运行，则从当前尾部继续订阅。关键是用户不会因为错过某个 UI 事件而得到错误的最终状态。

## 12. 当前设计的取舍

当前实现是一个紧凑的 Go Agent Runtime：保留 DeerFlow/LangGraph 的核心思想——线程状态、checkpoint、节点、interrupt、子任务和可插拔记忆——但没有引入完整外部编排平台。它适合学习和单体/少量实例部署；如果进入更大规模生产环境，可以在不改变这些接口的前提下替换为对象存储、Prometheus/OTel、独立队列和更强的 Graph 执行器。

## 13. 与本地 DeerFlow 的真实差异

下面的差异是“当前代码已经做到了什么”的边界，不把接口存在误说成平台能力已经完全等价：

| 方向 | 当前项目 | DeerFlow | 结论 |
| --- | --- | --- | --- |
| Agent 循环 | Go Engine 实现 Model → Tool → Model 的 ReAct 循环 | LangGraph 编译后的 StateGraph/子图 | 核心行为已对齐，编排器仍是轻量版 |
| Graph 恢复 | Graph 注册 `model/tool/finish/interrupt`，校验 checkpoint 游标 | 图节点、边、Reducer 和 State Schema 共同驱动执行 | 已有恢复边界，尚未提供完整任意 DAG 编排 |
| Checkpoint | PostgreSQL 保存一个统一的完整状态快照，大结果外置 Blob | LangGraph Checkpointer，并支持 delta/snapshot channel mode 与 checkpoint cache | 可靠恢复已具备，吞吐和缓存层较简单 |
| 事件流 | PostgreSQL durable event journal + Redis/Hub + SSE Last-Event-ID/gap | StreamBridge、DB 事件、delivery receipt、跨 Worker reconciliation | 断线恢复已具备，交付回执和运维控制面较少 |
| 工具发现 | `ToolCatalog` 提供元数据发现/解析；当前 Engine 默认仍绑定当前 Registry 定义 | `tool_search` 只把命中的延迟工具 schema 提升给模型 | 接口已预留，真正按需隐藏/提升 schema 仍是后续增强点 |
| Skills | 工具 allowlist、审批、重试、并行安全和子 Agent继承边界 | Skill 激活、`allowed-tools` 运行时过滤、技能目录和沙箱投影 | 安全边界已具备，Skill 生态和渐进加载较轻 |
| 记忆 | `MemoryProvider` + PostgreSQL，按用户/知识库注入受限上下文 | `MemoryMiddleware` 在一轮完成后异步队列化，只保留用户输入与最终回答，并可切换 DeerMem/Mem0 等后端 | 存储抽象已对齐，自动异步记忆管线仍较简单 |
| 子 Agent | 命名配置、父工具边界继承、共享调度槽位、持久化 child Run、等待屏障 | LangGraph 子图、任务工具、子 Agent 运行树和更丰富的任务编排 | 异步执行和恢复已具备，子图级编排较少 |
| 失败恢复 | 模型错误分类/退避/fallback，租约/attempt/lease token，普通工具反馈模型重试，副作用工具审批 | 还叠加 orphan reconciliation、delivery 语义和更完整的运行管理 | 核心容错已具备，平台运维语义较少 |
| 目标与长任务 | Run 预算、最大步数、超时、取消和 stop reason | Goal state、no-progress 检测、目标续跑、长任务交付验证 | 有安全上限，但没有 DeerFlow 的目标驱动续跑层 |
| 观测 | 结构化 trace identity、模型/工具边界、进程内 metrics、故障注入 | LangSmith/Langfuse/Monocle/OTel 等外部 Trace 与完整输入输出审计 | 基础可诊断，外部可视化与跨部署聚合尚未接入 |
| 生产任务 | PostgreSQL Worker 负责 Agent Run 和子 Run | 还支持 MCP durable background task、远程 status/cancel、artifact 交付 | 当前项目聚焦 Agent + RAG，远程任务平台不在本阶段 |

面试时最稳妥的表述是：项目已经实现 DeerFlow 的核心运行时思想——统一 State/checkpoint、可恢复 Worker、事件游标、工具安全边界、子 Agent 和可插拔记忆；没有声称复刻 DeerFlow 的完整 LangGraph 编排器、Skill 生态、外部 Trace 平台和 MCP 任务控制面。这种回答既能说明设计能力，也能清楚交代工程取舍。
