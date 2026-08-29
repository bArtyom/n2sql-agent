# DeerFlow 风格的会话、多 Run 与多任务策略

## 学习主题

把 DeerFlow 的 `thread_id/run_id` 思路落到 n2sql-agent：`conversation_id` 负责长期会话，`run_id` 负责某一轮 Agent 执行；同一个会话同时收到新问题时，支持 `reject`、`rollback`、`interrupt` 三种策略。

## 1. 为什么要分两个 ID

项目没有另建一列 `thread_id`，而是用已有的 `conversation_id` 表示 DeerFlow 语义里的 Thread：

```text
conversation_id=42  《员工手册问答》
  ├── run-101：第一次问“年假怎么计算？”
  ├── run-102：第二次问“试用期员工适用吗？”
  ├── run-103：用户点击重新生成
  └── run-104：用户编辑问题后重新执行
```

`conversation_id` 串起普通聊天历史；`run_id` 绑定本轮 Agent 的状态、SSE、租约、attempt、checkpoint 和最终答案。同一个 Run 被 Worker 接管时仍然使用原来的 `run_id`，只更换 `execution_id` 和 `lease_token`。

## 2. 同一会话的冲突

假设 `run-101` 正在运行，用户又在 `conversation_id=42` 中提交第二个问题。默认不能让两个根 Agent 同时修改同一个会话上下文，否则可能出现答案顺序不确定、后一个问题读到半成品 checkpoint、两个最终回答同时写入历史等问题。

因此数据库只允许同一会话有一个活跃根 Run。活跃状态包括：

```text
pending / running / waiting_children / waiting_approval
```

子 Run 不参加这个会话级限制，因为它们是父 Run 的内部任务，不是用户在会话中提交的第二轮问题。

## 3. 三种策略

### `reject`

发现活动 Run 后不改变数据库，直接返回 HTTP 409：

```json
{
  "error": {
    "code": "conversation_run_active",
    "active_run_id": "run-101",
    "active_status": "running",
    "requested_strategy": "reject",
    "conversation_id": 42
  }
}
```

适合防止用户误点发送或客户端重复提交。

### `rollback`

这是“放弃当前回答，改做新问题”：

```text
事务开始
  锁住 conversation_id=42
  run-101 及未完成 child → canceled
  stop_reason = multitask_rollback
  创建 run-102(status=pending)
事务提交
  取消本进程 Hub 中的 run-101
  旧 Worker 因 lease token fencing 失去写权限
```

旧 Run 不删除，事件、attempt 和 checkpoint 仍然保留审计；`run-102` 只读取最近一次成功的线程 checkpoint，不把 `run-101` 的半成品状态当成新问题上下文。

### `interrupt`

这是“中断当前执行，让新问题接管会话”：

```text
事务开始
  锁住 conversation_id=42
  run-101 及未完成 child → interrupted
  stop_reason = multitask_interrupt
  创建 run-102(status=pending)
事务提交
  写入并发布 run_interrupted
  取消本进程 Hub 中的 run-101
```

旧 SSE 会收到 `run_interrupted` 并结束；新 SSE 订阅 `run-102`。如果旧 Worker 在另一个进程，它收不到本地 Hub 的取消，但下一次续租会因 token 已被清空而失败，执行上下文随之取消，数据库写入也会被 fencing 拒绝。

## 4. 后端实现边界

### 请求层

```json
{
  "conversation_id": 42,
  "message": "换个问题：试用期员工适用吗？",
  "multitask_strategy": "interrupt"
}
```

Handler 做参数规范化和校验，然后把完整请求快照写入新 Run。策略也进入请求快照，所以 Worker 不需要依赖原始 HTTP 连接。

### 数据库准入层

`PostgresStore.Admit` 在一个事务中完成：

1. 用 `pg_advisory_xact_lock(conversation_id)` 串行化同一会话的准入；
2. `SELECT ... FOR UPDATE` 找到活跃根 Run；
3. 根据策略返回冲突，或替换旧 Run 树；
4. 插入新的 `pending` 根 Run；
5. 提交事务。

同时增加部分唯一索引作为最后一道约束：

```sql
CREATE UNIQUE INDEX agent_runs_active_root_conversation_idx
ON agent_runs (conversation_id)
WHERE conversation_id IS NOT NULL
  AND run_kind = 'root'
  AND status IN ('pending', 'running', 'waiting_children', 'waiting_approval');
```

业务锁解决正常流程，唯一索引防止旧接口或并发竞态绕过业务检查。

### 事件和 SSE 层

替换旧 Run 时写一个稳定的控制事件，例如：

```json
{
  "id": "run-101-replaced-by-run-102",
  "run_id": "run-101",
  "type": "run_interrupted",
  "category": "lifecycle",
  "data": {
    "reason": "multitask_interrupt",
    "replaced_by_run_id": "run-102",
    "status": "interrupted"
  }
}
```

事件进入 durable event journal，并尽可能发布到当前进程 Hub；配置 Redis 时由 Redis 负责跨进程短期传输。`run_interrupted` 和 `run_canceled` 都属于终态事件，SSE/Redis 收到后关闭旧流。

## 5. 与 Worker 接管的关系

多任务替换与崩溃恢复不是同一件事：

```text
用户主动发新问题
  → rollback/interrupt 旧 Run
  → 新建新的 run_id

Worker 崩溃
  → 原 run_id 保持不变
  → 租约过期后回到 pending
  → 新 Worker 用新的 execution_id/lease_token 接管
```

无论哪种情况，旧 Worker 都不能覆盖新状态：普通恢复靠 lease 过期和 checkpoint version，主动替换靠数据库先把旧 Run 置为终态并清空 lease token。

## 6. 当前实现结果

- `conversation_id` 与 `run_id` 的职责已分开；
- 默认 `reject`，可选 `rollback`、`interrupt`；
- 同一会话的活跃根 Run 有数据库原子约束；
- 替换旧 Run 树时保留审计数据，不物理删除；
- 新 Run 不继承被替换 Run 的未完成 checkpoint；
- 旧 Worker 通过 Hub cancel 和 lease token fencing 停止；
- `run_interrupted` 已纳入事件契约、Redis、SSE 终态和安全运行轨迹。

## 面试版回答

> 我把 DeerFlow 的 Thread 映射为 `conversation_id`，把一次 Agent 执行映射为 `run_id`。一个 conversation 可以有很多历史 Run，但同一时间只允许一个活跃根 Run。新请求默认 reject，也可以选择 rollback 取消旧 Run 树，或 interrupt 把旧 Run 标成中断后创建新 Run。准入在 PostgreSQL 事务中完成，并用部分唯一索引兜底；旧 Run 保留事件、attempt 和 checkpoint 供审计，新 Run 只从最近成功状态恢复。旧 Worker 通过 lease token fencing 失去写权限，SSE 收到对应终态事件后关闭旧流。
