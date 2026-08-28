# Agent Runtime Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将当前 Go Agent 完善为具有可靠事件恢复、统一状态、模型流式、轻量 Graph、通用子 Agent、durable interrupt、工具渐进披露、可插拔记忆和运行观测能力的 Agent Runtime。

**Architecture:** 保留现有 PostgreSQL Agent Run Worker、Redis live stream 和 Go ToolRegistry。新增稳定的 durable event cursor 与统一 AgentState；把 Engine 的固定 ReAct 逻辑逐步包在轻量 Node/Transition 执行层中，不引入 LangGraph/Python 编排框架。每个任务结束后独立测试并提交。

**Tech Stack:** Go、PostgreSQL、Redis Streams、现有 modelclient/modelruntime、Go testing、SQL migrations。

**Spec:** `docs/superpowers/specs/2026-08-28-agent-runtime-parity-design.md`

**Implementation status (2026-08-28):** Core implementation for Tasks 1–10 is complete. The checklist below records the original TDD plan; focused tests have been added and the final full-repository verification is performed after this documentation update.

## Global Constraints

- 逻辑上只保留一套 Agent Checkpoint；旧 checkpoint 接口不做兼容。
- PostgreSQL 是 Agent 状态和 durable event 的事实来源，Redis 只承担低延迟事件 tail/replay。
- Worker 必须用 `run_id + attempt_count + lease_token + expected_version` 防止旧 Worker 写入。
- 工具结果按不可信数据处理；副作用工具不因重试自动重复执行。
- 生产代码前必须先写失败测试并观察失败。
- 每个完成的切片单独执行测试、`go vet`（受影响包）并提交。

---

### Task 1: Durable event identity and cursor

**Files:**
- Create: `internal/database/migrations/sql/000080_add_agent_event_cursor.up.sql`
- Create: `internal/database/migrations/sql/000080_add_agent_event_cursor.down.sql`
- Modify: `internal/agent/event.go`
- Modify: `internal/agentrun/events.go`
- Modify: `internal/agentrun/redis_events.go`
- Modify: `internal/agentrun/event_bridge.go`
- Modify: `internal/handler/agent_run_worker.go`
- Test: `internal/agentrun/events_test.go`, `internal/agentrun/redis_events_test.go`, `internal/handler/agent_run_event_replay_test.go`

**Interfaces:**
- `Event` gains durable `Seq`, `Category`, and `TaskID`.
- `EventStore.ListAfter(ctx, runID, knowledgeBaseID, afterSeq, limit)` returns ordered events.
- Durable storage allocates sequence numbers; event IDs are unique idempotency keys.

- [ ] **Step 1: Write failing tests** for two Worker attempts using the same run ID, asserting distinct ordered seq values and no dropped resumed events; add a `ListAfter` pagination test.
- [ ] **Step 2: Run `go test ./internal/agentrun ./internal/handler -run 'Event|Replay'` and confirm failure caused by missing cursor/duplicate event behavior.**
- [ ] **Step 3: Add the migration and implement database-assigned seq plus `ListAfter`; replace local emitter counters with UUID/attempt-aware idempotency IDs and map Redis stream IDs to durable seq.

```go
type Event struct {
    ID       string `json:"id"`
    Seq      int64  `json:"seq,omitempty"`
    Category string `json:"category,omitempty"`
    TaskID   string `json:"task_id,omitempty"`
    Type     EventType `json:"type"`
    Data     any    `json:"data,omitempty"`
}
```

- [ ] **Step 4: Run focused tests, then `go vet ./internal/agentrun ./internal/handler`; commit `fix: make agent event replay durable across worker takeover`.**

### Task 2: Event contract and gap recovery

**Files:**
- Modify: `internal/agentstream/hub.go`
- Modify: `internal/agentrun/event_bridge.go`
- Modify: SSE handlers under `internal/handler/`
- Test: `internal/agentstream/hub_test.go`, `internal/handler/agent_run_event_replay_test.go`

**Interfaces:**
- All emitted event types must pass the same validator.
- A Redis gap returns a typed gap containing requested and retained cursor bounds.
- SSE falls back to durable `ListAfter` before resuming live tail.

- [ ] **Step 1: Add failing tests for `skill_loaded` and `loop_detected`, Redis gap followed by database replay, and a cursor that is already terminal.**
- [ ] **Step 2: Run the tests and verify the missing event type/cursor path fails.**
- [ ] **Step 3: Share event validation between `agent` and `Hub`; implement durable replay by seq and prevent duplicate replay when Redis reconnects.**
- [ ] **Step 4: Run focused stream tests and commit `fix: unify agent event contract and gap recovery`.**

### Task 3: Unified checkpoint fencing and failure semantics

**Files:**
- Modify: `internal/agentrun/run.go`
- Modify: `internal/agentruntime/context_state.go`
- Modify: `internal/agentruntime/engine.go`
- Modify: `internal/handler/agent_run_worker.go`
- Modify: `internal/database/migrations/sql/000079_create_agent_checkpoints.up.sql` or a new migration when required
- Test: `internal/agentruntime/engine_test.go`, `internal/agentrun/worker_test.go`

**Interfaces:**
- `Checkpoint` carries `LeaseToken` and `ExpectedVersion`.
- `CheckpointStore.SaveCheckpoint` returns a conflict error when the run no longer belongs to the caller.
- Engine checkpoint callbacks return errors to the Worker through a controlled degraded-state path.

- [ ] **Step 1: Add failing tests for stale lease checkpoint writes, version conflicts, and transient checkpoint failure with bounded retry/diagnostic state.**
- [ ] **Step 2: Run the tests and confirm current `SaveCheckpoint` accepts stale writes and Engine ignores sink errors.**
- [ ] **Step 3: Add SQL ownership/version predicates and make Engine retry checkpoint writes; preserve execution only when the checkpoint is explicitly marked degraded.**
- [ ] **Step 4: Verify Worker takeover resumes the latest valid unified state and commit `fix: fence unified agent checkpoints`.**

### Task 4: Tool-capable model streaming

**Files:**
- Modify: `internal/modelclient/client.go`
- Modify: `internal/modelruntime/chat.go`
- Modify: `internal/agentruntime/engine.go`
- Modify: `internal/handler/agent_run_worker.go`
- Test: `internal/modelclient/http_client_test.go`, `internal/modelruntime/chat_test.go`, `internal/agentruntime/engine_test.go`

**Interfaces:**
- Add `ToolChatStreamer.ChatMessagesWithToolsStream` with text and tool-call chunk callbacks.
- Engine buffers tool-call JSON until valid, but emits answer text deltas immediately.

- [ ] **Step 1: Add a failing parser/engine test where three model text chunks become three `message_delta` events and split tool JSON becomes one executable call.**
- [ ] **Step 2: Run the test and confirm the current full-response runner emits only one final delta.**
- [ ] **Step 3: Implement SSE/JSON incremental assembly, provider fallback only before first emitted delta, and Engine streaming while preserving checkpoint boundaries.**
- [ ] **Step 4: Run model and engine tests and commit `feat: stream tool-capable agent model responses`.**

### Task 5: Lightweight Agent Graph

**Files:**
- Create: `internal/agentruntime/graph.go`
- Create: `internal/agentruntime/graph_test.go`
- Modify: `internal/agentruntime/engine.go`
- Modify: `internal/agentruntime/context_state.go`

**Interfaces:**

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

- [ ] **Step 1: Write failing tests for Model → Tool → Model → Finish, unknown node failure, and persisted `CurrentNode` resume.**
- [ ] **Step 2: Run the tests and confirm the graph types do not exist.**
- [ ] **Step 3: Implement the minimal graph registry/executor and adapt the existing ReAct path without changing its default behavior.**
- [ ] **Step 4: Verify graph state is included in the unified checkpoint and commit `refactor: introduce lightweight agent graph runtime`.**

### Task 6: Run-level budgets and structured stop reasons

**Files:**
- Modify: `internal/agentruntime/engine.go`
- Modify: `internal/agentrun/run.go`
- Modify: `internal/agentservice/context.go`
- Modify: `internal/handler/agent_run_worker.go`
- Test: `internal/agentruntime/engine_test.go`, `internal/agentrun/worker_test.go`

- [ ] **Step 1: Add failing tests for total model calls, total tool calls, aggregate token budget, loop cap, and timeout classification.**
- [ ] **Step 2: Run tests and verify only per-call completion tokens/max steps currently exist.**
- [ ] **Step 3: Add a request/run budget, consume counters at model/tool boundaries, and persist `stop_reason` without exposing raw chain-of-thought.**
- [ ] **Step 4: Run focused tests and commit `feat: enforce agent run budgets and stop reasons`.**

### Task 7: Configurable subagent registry and durable progress

**Files:**
- Create: `internal/agentruntime/subagent_registry.go`
- Create: `internal/agentruntime/subagent_registry_test.go`
- Modify: `internal/agentruntime/delegate_research_tool.go`
- Modify: `internal/agentservice/service.go`
- Modify: `internal/agentrun/run.go`
- Test: `internal/agentruntime/delegate_research_tool_test.go`, `internal/agentservice/service_test.go`

- [ ] **Step 1: Add failing tests for named subagents, parent tool allowlist inheritance minus `task`, and child-specific model/step/timeout settings.**
- [ ] **Step 2: Run tests and confirm only the knowledge-only research child exists.**
- [ ] **Step 3: Implement registry-backed subagent specs, task creation, bounded scheduler admission, and durable child progress summaries.**
- [ ] **Step 4: Verify child terminal events and parent join behavior, then commit `feat: add configurable subagent registry`.**

### Task 8: Durable interrupts for approval, clarification, and children

**Files:**
- Create: `internal/agentruntime/interrupt.go`
- Create: `internal/agentruntime/interrupt_test.go`
- Modify: `internal/agentruntime/engine.go`
- Modify: `internal/agentrun/run.go`
- Modify: `internal/agentrun/worker.go`
- Modify: `internal/handler/agent_run_worker.go`
- Test: `internal/agentrun/worker_test.go`, `internal/handler/agent_run_stop_test.go`

- [ ] **Step 1: Add failing tests for durable `waiting_approval`, `waiting_clarification`, and `waiting_children` states resumed by a new Worker.**
- [ ] **Step 2: Run tests and confirm approval currently depends on a process-local gate/Hub wait.**
- [ ] **Step 3: Persist interrupt payload and expiry in the unified checkpoint, release the lease while waiting, and requeue only after a valid resume input.**
- [ ] **Step 4: Test expiry/cancel paths and commit `feat: persist agent interrupts across worker restarts`.**

### Task 9: Tool catalog and Memory Provider boundaries

**Files:**
- Create: `internal/agent/tool_catalog.go`
- Create: `internal/memory/provider.go`
- Modify: `internal/agent/registry.go`
- Modify: `internal/agentservice/service.go`
- Modify: `internal/memory/memory.go`
- Test: `internal/agent/tool_catalog_test.go`, `internal/agentservice/memory_test.go`

- [ ] **Step 1: Add failing tests for catalog-first tool discovery, allowlist enforcement, memory `Add/GetContext/Search/Delete`, and no memory instruction escalation.**
- [ ] **Step 2: Run tests and confirm the current service exposes all registered tools and directly lists memories.**
- [ ] **Step 3: Implement `ToolCatalog` and `MemoryProvider` interfaces; adapt existing Go registry/PostgreSQL store without changing permission defaults.**
- [ ] **Step 4: Add `tool_search`-style metadata disclosure and provider-backed memory injection, then commit `refactor: add agent tool and memory provider boundaries`.**

### Task 10: Observability, fault injection, and interview documentation

**Files:**
- Create: `internal/agentruntime/fault_injection_test.go`
- Modify: `internal/ops/trace.go`
- Modify: `internal/agentrun/run.go`
- Modify: `internal/agentrun/events.go`
- Create: `docs/agent-runtime-interview-guide.md`
- Modify: `agent.md`
- Modify: `PROJECT_STATUS.md`

- [ ] **Step 1: Add failing integration-style tests for Worker crash, Redis gap, checkpoint failure, model mid-stream failure, child timeout, and stale lease writes.**
- [ ] **Step 2: Implement structured run/attempt/node/tool/model metrics with bounded logs and no secrets or full prompts.**
- [ ] **Step 3: Run the full Agent package tests and write the interview guide covering architecture, data flow, failure recovery, memory, tools, children, SSE and trade-offs.**
- [ ] **Step 4: Run `go test ./...`, `go vet ./...`, inspect `git diff --check`, commit `docs: add agent runtime interview guide`, and only then audit all ten requirements.**
