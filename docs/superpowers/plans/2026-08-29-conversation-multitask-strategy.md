# Conversation Multitask Strategy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add DeerFlow-style `reject`, `rollback`, and `interrupt` admission policies so one conversation can keep many historical Runs while allowing at most one active root Run.

**Architecture:** Keep `conversation_id` as the long-lived thread identity and `run_id` as the execution identity. Add a small admission contract in `agentrun`, implement the atomic transaction in `PostgresStore`, and let the HTTP submission handler cancel the replaced in-process Hub run after the transaction commits. Existing Worker, checkpoint, and SSE paths remain the execution and delivery mechanisms.

**Tech Stack:** Go, `database/sql`, PostgreSQL partial unique indexes and row locks, `net/http`, existing in-process Hub, Go tests.

**Spec:** `docs/decisions/ADR-005-会话多Run与多任务策略.md`

## Global Constraints

- `multitask_strategy` accepts only `reject`, `rollback`, or `interrupt`; an omitted value is `reject`.
- Only root Runs with a non-zero `conversation_id` participate in the active-conversation guard.
- Active statuses are `pending`, `running`, `waiting_children`, and `waiting_approval`.
- Replaced Run records, events, and checkpoints are retained; a fresh Run may use only the latest successful root checkpoint.
- Do not stage or modify unrelated dirty files in the repository.

---

### Task 1: Add the strategy and admission contracts

**Files:**
- Modify: `internal/agentrun/run.go`
- Modify: `internal/agentservice/context.go`
- Modify: `internal/handler/knowledge_base_agent_chat.go`
- Test: `internal/agentrun/multitask_test.go`
- Test: `internal/handler/agent_run_worker_test.go`

**Interfaces:**
- Produces `agentrun.MultitaskStrategy`, `agentrun.NormalizeMultitaskStrategy`, `agentrun.AdmissionInput`, `agentrun.AdmissionResult`, `agentrun.ActiveRunConflict`, and `agentrun.MultitaskAdmitter`.
- `ChatRequest` exposes `MultitaskStrategy string json:"multitask_strategy,omitempty"`.

- [ ] **Step 1: Write failing normalization and conflict tests**

Add tests that assert omitted/whitespace strategy becomes `reject`, the two other values are accepted, unknown values return `ErrInvalidMultitaskStrategy`, and an `ActiveRunConflict` exposes the active run ID and status through `errors.As`.

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./internal/agentrun ./internal/handler -run 'Test(NormalizeMultitaskStrategy|ActiveRunConflict|PersistentAgentRunSubmission)' -count=1`

Expected: compile failure because the strategy contract and request field do not exist.

- [ ] **Step 3: Implement the small value types and request validation**

Define the three constants and normalize the request value in `decodeKnowledgeBaseAgentChatRequest`. Keep the JSON field in the persisted request snapshot automatically through `ChatRequest`.

- [ ] **Step 4: Run the focused tests and verify success**

Run: `go test ./internal/agentrun ./internal/handler -run 'Test(NormalizeMultitaskStrategy|ActiveRunConflict|PersistentAgentRunSubmission)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/agentrun/run.go internal/agentservice/context.go internal/handler/knowledge_base_agent_chat.go internal/agentrun/multitask_test.go internal/handler/agent_run_worker_test.go
git commit -m "feat: add conversation multitask strategy contract"
```

### Task 2: Add the database constraint and atomic admission

**Files:**
- Create: `internal/database/migrations/sql/000081_add_agent_run_multitask_strategy.up.sql`
- Create: `internal/database/migrations/sql/000081_add_agent_run_multitask_strategy.down.sql`
- Modify: `internal/agentrun/run.go`
- Test: `internal/agentrun/multitask_postgres_test.go` (or the repository's existing database-test convention)

**Interfaces:**
- `PostgresStore.Admit(context.Context, agentrun.AdmissionInput) (agentrun.AdmissionResult, error)` implements `agentrun.MultitaskAdmitter`.
- `CreateInput` remains the insertion payload; `AdmissionInput` carries it plus the normalized strategy.

- [ ] **Step 1: Write the SQL migration and store contract tests first**

Test the SQL contract statically if no PostgreSQL test database is configured: the up migration must add `interrupted` to both status checks and create a partial unique index scoped to root Runs and the four active statuses; the down migration must remove that index.

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./internal/agentrun -run 'Test(Multitask|ActiveConversation)' -count=1`

Expected: FAIL because `Admit` and the migration files are absent.

- [ ] **Step 3: Implement the atomic PostgreSQL admission transaction**

Inside one transaction, lock the active root Run for the input conversation. For `reject`, return `ActiveRunConflict`. For `rollback` or `interrupt`, update the recursive active tree to the appropriate terminal status, clear leases, set `stop_reason`, finish the current attempt, then insert and return the new pending root Run. Map a unique-index conflict to `ActiveRunConflict` rather than leaking a PostgreSQL error.

- [ ] **Step 4: Extend terminal status handling**

Add `StatusInterrupted`, include it in `IsTerminalStatus`, cancellation tree exclusions, child completion checks, and the attempt status constraint migration.

- [ ] **Step 5: Run the focused tests and verify success**

Run: `go test ./internal/agentrun -run 'Test(Multitask|ActiveConversation)' -count=1`

Expected: PASS when the configured PostgreSQL test path is available; otherwise the static contract tests must still pass and report that integration tests were skipped explicitly.

- [ ] **Step 6: Commit**

```powershell
git add internal/agentrun/run.go internal/agentrun/multitask_postgres_test.go internal/database/migrations/sql/000081_add_agent_run_multitask_strategy.up.sql internal/database/migrations/sql/000081_add_agent_run_multitask_strategy.down.sql
git commit -m "feat: atomically admit one active run per conversation"
```

### Task 3: Wire admission into the async HTTP submission

**Files:**
- Modify: `internal/handler/agent_run_worker.go`
- Modify: `internal/handler/knowledge_base_agent_chat.go`
- Test: `internal/handler/agent_run_worker_test.go`

**Interfaces:**
- The handler type-asserts `agentrun.MultitaskAdmitter`; PostgreSQL production storage uses it, while minimal test stores retain the existing `Create` fallback.
- Replaced Run IDs are returned in the optional response metadata and used to call `hub.Cancel` after commit.

- [ ] **Step 1: Add failing handler tests**

Cover default `reject` returning `409` with `conversation_run_active`, `rollback` returning `202` with a new Run ID and canceling the old Hub context, and `interrupt` returning `202` with `interrupted_run_id`.

- [ ] **Step 2: Run the focused handler tests and verify failure**

Run: `go test ./internal/handler -run 'TestPersistentAgentRunSubmission(Multitask|Reject|Rollback|Interrupt)' -count=1`

Expected: FAIL because the handler still calls `Create` unconditionally.

- [ ] **Step 3: Use `Admit` and map structured conflicts**

Normalize the strategy before hashing/persisting. Start the Hub only after admission succeeds, or clean up the just-created Hub entry if persistence fails. For a replaced Run, cancel the old Hub run and its returned child IDs after the database transaction commits. Return JSON fields `run_id`, `status`, `stream_url`, and optional `replaced_run_id`/`replaced_status`.

- [ ] **Step 4: Run the focused handler tests and verify success**

Run: `go test ./internal/handler -run 'TestPersistentAgentRunSubmission(Multitask|Reject|Rollback|Interrupt)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/handler/agent_run_worker.go internal/handler/knowledge_base_agent_chat.go internal/handler/agent_run_worker_test.go
git commit -m "feat: expose multitask policies on agent submission"
```

### Task 4: Protect Worker and SSE behavior for replaced Runs

**Files:**
- Modify: `internal/agentrun/worker.go`
- Modify: `internal/handler/knowledge_base_agent_chat_stream.go`
- Modify: `internal/agentstream/hub.go`
- Test: `internal/agentrun/worker_test.go`
- Test: `internal/handler/agent_run_stream_test.go`

**Interfaces:**
- The Worker keeps the existing lease-token fencing contract.
- Hub accepts a typed `run_interrupted` control event and closes the old stream only after the event is published.

- [ ] **Step 1: Add failing replacement tests**

Assert that an interrupted old Run is terminal, a stale Worker cannot mark it succeeded, and its SSE stream contains `run_interrupted` but not a later `run_finished` event.

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./internal/agentrun ./internal/handler -run 'Test(.*Interrupted|.*Replaced|.*Stale)' -count=1`

Expected: FAIL because the event type and stale completion assertions are not yet covered.

- [ ] **Step 3: Implement the minimal runtime/SSE changes**

Add `agent.EventRunInterrupted`, register it in event validation/category mapping, publish the control event from the replacement path, and ensure Worker terminal writes remain lease/status guarded. Do not add a second event transport.

- [ ] **Step 4: Run focused and full tests**

Run: `gofmt -w internal/agentrun internal/agent internal/agentservice internal/handler`; then `go test ./internal/agentrun ./internal/agent ./internal/handler -count=1`.

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/agentrun/worker.go internal/handler/knowledge_base_agent_chat_stream.go internal/agentstream/hub.go internal/agentrun/worker_test.go internal/handler/agent_run_stream_test.go internal/agent/event.go
git commit -m "feat: fence and stream interrupted agent runs"
```

### Task 5: Verify, document, and update project status

**Files:**
- Modify: `README.md`
- Modify: `PROJECT_STATUS.md`
- Modify: `docs/agent-runtime-interview-guide.md`

- [ ] **Step 1: Document the request shape and lifecycle**

Add examples for the three strategies, the `409` conflict response, and the `conversation_id`/`run_id` relationship. Explain that replaced Run checkpoint data remains auditable but is not used as a fresh thread checkpoint.

- [ ] **Step 2: Run repository verification**

Run: `gofmt -l internal cmd`; `go test ./...`; `go vet ./...`.

Expected: no formatting output, all tests pass, and vet exits successfully.

- [ ] **Step 3: Review the diff for scope**

Run: `git diff --check`; `git diff --stat HEAD~4..HEAD`; `git status --short`.

Expected: only feature commits contain this change; unrelated pre-existing dirty files remain unstaged.

- [ ] **Step 4: Commit documentation**

```powershell
git add README.md PROJECT_STATUS.md docs/agent-runtime-interview-guide.md docs/decisions/ADR-005-会话多Run与多任务策略.md docs/superpowers/plans/2026-08-29-conversation-multitask-strategy.md
git commit -m "docs: record conversation multitask run design"
```
