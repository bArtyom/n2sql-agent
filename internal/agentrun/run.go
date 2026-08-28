package agentrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
)

type Status string

const (
	StatusPending         Status = "pending"
	StatusRunning         Status = "running"
	StatusWaitingChildren Status = "waiting_children"
	StatusWaitingApproval Status = "waiting_approval"
	StatusRequeued        Status = "requeued"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusTimeout         Status = "timeout"
	StatusCanceled        Status = "canceled"
)

type Kind string

const (
	KindRoot  Kind = "root"
	KindChild Kind = "child"
)

var (
	ErrNoRun              = errors.New("no pending agent run")
	ErrRunNotFound        = errors.New("agent run not found")
	ErrInvalidRun         = errors.New("invalid agent run")
	ErrLeaseLost          = errors.New("agent run lease lost")
	ErrCheckpointConflict = errors.New("agent checkpoint ownership or version conflict")
	ErrApprovalNotFound   = errors.New("agent approval not found")
)

const defaultLeaseDuration = 5 * time.Minute
const maxAgentRunAttempts = 3

func IsTerminalStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusTimeout || status == StatusCanceled
}

func shouldRetryExpiredRun(attemptCount int) bool {
	return attemptCount < maxAgentRunAttempts
}

type Run struct {
	ID              int64           `json:"id"`
	RunID           string          `json:"run_id"`
	KnowledgeBaseID int64           `json:"knowledge_base_id"`
	ConversationID  int64           `json:"conversation_id,omitempty"`
	ParentRunID     int64           `json:"parent_run_id,omitempty"`
	RunKind         Kind            `json:"run_kind"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response,omitempty"`
	Status          Status          `json:"status"`
	AttemptCount    int             `json:"attempt_count"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	StopReason      string          `json:"stop_reason,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	LeaseUntil      *time.Time      `json:"lease_until,omitempty"`
	HeartbeatAt     *time.Time      `json:"heartbeat_at,omitempty"`
	LeaseToken      string          `json:"-"`
	ExecutionID     string          `json:"execution_id,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Checkpoint      *Checkpoint     `json:"-"`
}

// Attempt is the durable lifecycle record for one Worker claim. It lets the
// UI explain retries and lease recovery without exposing the request snapshot
// or transient tool payloads.
type Attempt struct {
	ID           int64      `json:"id"`
	AgentRunID   int64      `json:"agent_run_id"`
	AttemptCount int        `json:"attempt_count"`
	Status       Status     `json:"status"`
	ErrorMessage string     `json:"error_message,omitempty"`
	StopReason   string     `json:"stop_reason,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type CreateInput struct {
	RunID           string
	KnowledgeBaseID int64
	ConversationID  int64
	ParentRunID     int64
	RunKind         Kind
	Request         json.RawMessage
}

type ChildCreateInput struct {
	RunID           string
	ParentRunID     int64
	KnowledgeBaseID int64
	ToolCallID      string
	TraceID         string
	Request         json.RawMessage
}

type ChildRunStore interface {
	CreateChild(context.Context, ChildCreateInput) (Run, error)
	SaveChildResponse(context.Context, int64, json.RawMessage) error
	MarkChildSucceeded(context.Context, int64) error
	MarkChildFailed(context.Context, int64, string) error
	MarkChildCanceled(context.Context, int64) error
}

type AsyncChildRunStore interface {
	CreatePendingChild(context.Context, ChildCreateInput) (Run, error)
	Get(context.Context, string, int64) (Run, error)
}

type Store interface {
	Create(context.Context, CreateInput) (Run, error)
	ClaimNext(context.Context) (Run, error)
	RequeueExpired(context.Context) error
	RenewLease(context.Context, int64, string) error
	MarkSucceeded(context.Context, int64, string) error
	MarkFailed(context.Context, int64, string, string) error
	MarkCanceled(context.Context, int64, string) error
}

const (
	StopReasonModelError      = "model_error"
	StopReasonToolError       = "tool_error"
	StopReasonTimeout         = "timeout"
	StopReasonCanceled        = "canceled"
	StopReasonStepLimit       = "step_limit"
	StopReasonValidationError = "validation_error"
	StopReasonInternalError   = "internal_error"
	StopReasonOrphanRecovered = "orphan_recovered"
)

// StoppedError carries a bounded lifecycle reason across the executor/Worker
// boundary. The detailed error remains separate and is stored in error_message.
type StoppedError struct {
	Err    error
	Reason string
}

func (e *StoppedError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *StoppedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StopReasonStore persists a DeerFlow-style stop reason alongside the
// human-facing error. Store remains compatible with adapters that only support
// MarkFailed or MarkCanceled.
type StopReasonStore interface {
	MarkFailedWithReason(context.Context, int64, string, string, string) error
	MarkTimedOut(context.Context, int64, string, string) error
	MarkCanceledWithReason(context.Context, int64, string, string) error
}

// ParentRunCoordinator is implemented by durable stores that can park a
// parent while independent children run and put it back in the pending queue
// once every child has reached a terminal state.
type ParentRunCoordinator interface {
	MarkWaitingChildren(context.Context, int64, string) error
	ResumeParentIfChildrenTerminal(context.Context, int64) (bool, error)
}

// ApprovalInterruptStore parks an approval-gated run without holding a
// Worker lease and atomically applies the user's decision to its unified
// checkpoint before requeueing it.
type ApprovalInterruptStore interface {
	MarkWaitingApproval(context.Context, int64, string) error
	ResolveApproval(context.Context, string, int64, bool) error
}

// Reader exposes safe run metadata without exposing the persisted request
// snapshot. It is intentionally separate from Store so read-only API handlers
// do not need task mutation capabilities.
type Reader interface {
	Get(context.Context, string, int64) (Run, error)
}

// DatabaseReader resolves a run by its internal database ID. Workers use it
// for publishing a parent-stream event after a child releases the barrier.
type DatabaseReader interface {
	GetByID(context.Context, int64) (Run, error)
}

// CancellationStore cancels a run and all unfinished descendants. It returns
// child public IDs so the HTTP layer can also cancel in-process contexts.
type CancellationStore interface {
	CancelTree(context.Context, string, int64) ([]string, error)
}

// ChildReader exposes safe parent/child Run metadata for execution trees.
type ChildReader interface {
	ListChildren(context.Context, int64, int64) ([]Run, error)
}

// AttemptReader exposes retry history by internal run ID. The HTTP layer
// resolves the public run ID and knowledge-base ownership before calling it.
type AttemptReader interface {
	ListAttempts(context.Context, int64) ([]Attempt, error)
}

type ResultWriter interface {
	SaveResponse(context.Context, int64, json.RawMessage) error
}

// Checkpoint is one complete, immutable Agent state snapshot. The state JSON
// contains messages, compacted memory and any pending tool decision together.
// This mirrors DeerFlow/LangGraph: one checkpointer owns resumable state, while
// run metadata and the event journal remain separate concerns.
type Checkpoint struct {
	ID              int64
	AgentRunID      int64
	ConversationID  int64
	AttemptCount    int
	StepNumber      int
	CheckpointID    string
	State           json.RawMessage
	CreatedAt       time.Time
	LeaseToken      string `json:"-"`
	ExpectedVersion int    `json:"-"`
}

type CheckpointStore interface {
	SaveCheckpoint(context.Context, Checkpoint) error
	GetLatestCheckpoint(context.Context, int64) (*Checkpoint, error)
	GetLatestThreadCheckpoint(context.Context, int64) (*Checkpoint, error)
	DeleteThreadCheckpoints(context.Context, int64) error
}

type PostgresStore struct {
	db               *sql.DB
	checkpointBlobs  *ToolResultFileStore
	checkpointInline int
}

func (s *PostgresStore) PendingCount(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE status = 'pending'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count agent runs: %w", err)
	}
	return count, nil
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, checkpointInline: 8 * 1024}
}

func NewPostgresStoreWithCheckpointFiles(db *sql.DB, blobs *ToolResultFileStore, inlineLimit int) *PostgresStore {
	if inlineLimit <= 0 {
		inlineLimit = 8 * 1024
	}
	return &PostgresStore{db: db, checkpointBlobs: blobs, checkpointInline: inlineLimit}
}

func (s *PostgresStore) Get(ctx context.Context, runID string, knowledgeBaseID int64) (Run, error) {
	if runID == "" || knowledgeBaseID <= 0 {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, knowledge_base_id, COALESCE(conversation_id, 0), COALESCE(parent_run_id, 0), run_kind, request, response,
			status, attempt_count, COALESCE(error_message, ''), COALESCE(stop_reason, ''), created_at, started_at,
			finished_at, lease_until, heartbeat_at, lease_token, updated_at
		FROM agent_runs
		WHERE run_id = $1 AND knowledge_base_id = $2`, runID, knowledgeBaseID).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.ParentRunID, &run.RunKind, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt,
		&run.FinishedAt, &run.LeaseUntil, &run.HeartbeatAt, &run.LeaseToken, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get agent run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) GetByID(ctx context.Context, id int64) (Run, error) {
	if id <= 0 {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, knowledge_base_id, COALESCE(conversation_id, 0), COALESCE(parent_run_id, 0), run_kind, request, response,
			status, attempt_count, COALESCE(error_message, ''), COALESCE(stop_reason, ''), created_at, started_at,
			finished_at, lease_until, heartbeat_at, lease_token, updated_at
		FROM agent_runs WHERE id = $1`, id).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.ParentRunID, &run.RunKind, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt,
		&run.FinishedAt, &run.LeaseUntil, &run.HeartbeatAt, &run.LeaseToken, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get agent run by id: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) CancelTree(ctx context.Context, runID string, knowledgeBaseID int64) ([]string, error) {
	if strings.TrimSpace(runID) == "" || knowledgeBaseID <= 0 {
		return nil, ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin agent cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var rootID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM agent_runs WHERE run_id = $1 AND knowledge_base_id = $2 FOR UPDATE`, runID, knowledgeBaseID).Scan(&rootID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("lock agent run for cancellation: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE run_tree AS (
			SELECT id, run_id FROM agent_runs WHERE id = $1
			UNION ALL
			SELECT child.id, child.run_id FROM agent_runs child JOIN run_tree parent ON child.parent_run_id = parent.id
		)
		SELECT run_id FROM run_tree WHERE id <> $1`, rootID)
	if err != nil {
		return nil, fmt.Errorf("list child runs for cancellation: %w", err)
	}
	var childIDs []string
	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan child run for cancellation: %w", err)
		}
		childIDs = append(childIDs, childID)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close child cancellation rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE run_tree AS (
			SELECT id FROM agent_runs WHERE id = $1
			UNION ALL
			SELECT child.id FROM agent_runs child JOIN run_tree parent ON child.parent_run_id = parent.id
		)
		UPDATE agent_runs
		SET status = 'canceled', finished_at = CURRENT_TIMESTAMP,
			stop_reason = 'canceled',
			lease_until = NULL, heartbeat_at = NULL, lease_token = NULL,
			updated_at = CURRENT_TIMESTAMP
		WHERE id IN (SELECT id FROM run_tree)
		  AND status NOT IN ('succeeded', 'failed', 'timeout', 'canceled')`, rootID); err != nil {
		return nil, fmt.Errorf("cancel agent run tree: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent cancellation: %w", err)
	}
	return childIDs, nil
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Run, error) {
	if input.RunID == "" || input.KnowledgeBaseID <= 0 || len(input.Request) == 0 || !json.Valid(input.Request) {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_runs (run_id, knowledge_base_id, conversation_id, parent_run_id, run_kind, request)
		VALUES ($1, $2, NULLIF($3, 0), NULLIF($4, 0), $5, $6)
		RETURNING id, run_id, knowledge_base_id, COALESCE(conversation_id, 0), COALESCE(parent_run_id, 0), run_kind, request, response,
			status, attempt_count, COALESCE(error_message, ''), COALESCE(stop_reason, ''), created_at, started_at, finished_at,
			lease_until, heartbeat_at, lease_token, updated_at`,
		input.RunID, input.KnowledgeBaseID, input.ConversationID, input.ParentRunID, runKind(input.RunKind), input.Request).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.ParentRunID, &run.RunKind, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.LeaseUntil, &run.HeartbeatAt, &run.LeaseToken, &run.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create agent run: %w", err)
	}
	return run, nil
}

func runKind(kind Kind) Kind {
	if kind == "" {
		return KindRoot
	}
	return kind
}

func (s *PostgresStore) CreateChild(ctx context.Context, input ChildCreateInput) (Run, error) {
	if input.RunID == "" || input.ParentRunID <= 0 || input.KnowledgeBaseID <= 0 || len(input.Request) == 0 || !json.Valid(input.Request) {
		return Run{}, ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin create child agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var run Run
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_runs (run_id, knowledge_base_id, parent_run_id, run_kind, request, status, attempt_count, started_at, finished_at, response, error_message, updated_at)
		VALUES ($1, $2, $3, 'child', $4, 'running', 1, CURRENT_TIMESTAMP, NULL, NULL, NULL, CURRENT_TIMESTAMP)
		ON CONFLICT (run_id) DO UPDATE SET
			parent_run_id = EXCLUDED.parent_run_id,
			knowledge_base_id = EXCLUDED.knowledge_base_id,
			request = EXCLUDED.request,
			status = 'running',
			attempt_count = agent_runs.attempt_count + 1,
			started_at = CURRENT_TIMESTAMP,
			finished_at = NULL,
			response = NULL,
			error_message = NULL,
			stop_reason = NULL,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id, run_id, knowledge_base_id, 0, parent_run_id, run_kind, request, response,
			status, attempt_count, COALESCE(error_message, ''), COALESCE(stop_reason, ''), created_at, started_at, finished_at,
			lease_until, heartbeat_at, lease_token, updated_at`,
		input.RunID, input.KnowledgeBaseID, input.ParentRunID, input.Request).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.ParentRunID, &run.RunKind, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.LeaseUntil, &run.HeartbeatAt, &run.LeaseToken, &run.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create child agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_run_attempts (agent_run_id, attempt_count, status, started_at, updated_at)
		VALUES ($1, $2, 'running', $3, $3)
		ON CONFLICT (agent_run_id, attempt_count)
		DO UPDATE SET status = 'running', error_message = NULL, stop_reason = NULL,
		              started_at = EXCLUDED.started_at, finished_at = NULL, updated_at = EXCLUDED.updated_at`,
		run.ID, run.AttemptCount, run.StartedAt); err != nil {
		return Run{}, fmt.Errorf("record child agent run attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit create child agent run: %w", err)
	}
	return run, nil
}

// CreatePendingChild creates an idempotent child job for the shared Worker.
// Pending/running/succeeded rows are returned unchanged so a resumed parent
// reuses the same child. A failed child is requeued only while it is below the
// normal run-attempt limit; after that the parent receives its final failure.
func (s *PostgresStore) CreatePendingChild(ctx context.Context, input ChildCreateInput) (Run, error) {
	if input.RunID == "" || input.ParentRunID <= 0 || input.KnowledgeBaseID <= 0 || len(input.Request) == 0 || !json.Valid(input.Request) {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_runs (run_id, knowledge_base_id, parent_run_id, run_kind, request, status)
		VALUES ($1, $2, $3, 'child', $4, 'pending')
		ON CONFLICT (run_id) DO UPDATE SET
			status = CASE
				WHEN agent_runs.status = 'failed' AND agent_runs.attempt_count < $5 THEN 'pending'
				ELSE agent_runs.status
			END,
			finished_at = CASE
				WHEN agent_runs.status = 'failed' AND agent_runs.attempt_count < $5 THEN NULL
				ELSE agent_runs.finished_at
			END,
			response = CASE
				WHEN agent_runs.status = 'failed' AND agent_runs.attempt_count < $5 THEN NULL
				ELSE agent_runs.response
			END,
			error_message = CASE
				WHEN agent_runs.status = 'failed' AND agent_runs.attempt_count < $5 THEN NULL
				ELSE agent_runs.error_message
			END,
			stop_reason = CASE
				WHEN agent_runs.status = 'failed' AND agent_runs.attempt_count < $5 THEN NULL
				ELSE agent_runs.stop_reason
			END,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id, run_id, knowledge_base_id, 0, parent_run_id, run_kind, request, response,
			status, attempt_count, COALESCE(error_message, ''), COALESCE(stop_reason, ''), created_at, started_at, finished_at,
			lease_until, heartbeat_at, lease_token, updated_at`,
		input.RunID, input.KnowledgeBaseID, input.ParentRunID, input.Request, maxAgentRunAttempts).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.ParentRunID, &run.RunKind, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.LeaseUntil, &run.HeartbeatAt, &run.LeaseToken, &run.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create pending child agent run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) SaveChildResponse(ctx context.Context, id int64, response json.RawMessage) error {
	if id <= 0 || len(response) == 0 || !json.Valid(response) {
		return ErrInvalidRun
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET response = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND run_kind = 'child' AND status = 'running'`, id, response)
	if err != nil {
		return fmt.Errorf("save child agent response: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (s *PostgresStore) MarkChildSucceeded(ctx context.Context, id int64) error {
	return s.markChildFinished(ctx, id, StatusSucceeded, "")
}

func (s *PostgresStore) MarkChildFailed(ctx context.Context, id int64, message string) error {
	return s.markChildFinished(ctx, id, StatusFailed, message)
}

func (s *PostgresStore) MarkChildCanceled(ctx context.Context, id int64) error {
	return s.markChildFinished(ctx, id, StatusCanceled, "")
}

func (s *PostgresStore) markChildFinished(ctx context.Context, id int64, status Status, message string) error {
	if id <= 0 {
		return ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark child agent run %s: %w", status, err)
	}
	defer func() { _ = tx.Rollback() }()
	var attemptCount int
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_runs
		SET status = $2, error_message = NULLIF($3, ''), finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND run_kind = 'child' AND status = 'running'
		RETURNING attempt_count`, id, status, message).Scan(&attemptCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("mark child agent run %s: %w", status, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET status = $2, error_message = NULLIF($3, ''), finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE agent_run_id = $1 AND attempt_count = $4 AND status IN ('running', 'waiting_children')`, id, status, message, attemptCount); err != nil {
		return fmt.Errorf("update child agent attempt %s: %w", status, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit child agent run %s: %w", status, err)
	}
	return nil
}

func (s *PostgresStore) ClaimNext(ctx context.Context) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin claim agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var run Run
	err = tx.QueryRowContext(ctx, `
		WITH next_run AS (
			SELECT id FROM agent_runs
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE agent_runs AS run
		SET status = 'running', attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP, finished_at = NULL,
			error_message = NULL, stop_reason = NULL,
			lease_until = CURRENT_TIMESTAMP + INTERVAL '5 minutes',
			lease_token = md5(random()::text || clock_timestamp()::text || run.id::text),
			heartbeat_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		FROM next_run
		WHERE run.id = next_run.id
		RETURNING run.id, run.run_id, run.knowledge_base_id, COALESCE(run.conversation_id, 0), COALESCE(run.parent_run_id, 0), run.run_kind,
			run.request, run.response, run.status, run.attempt_count, COALESCE(run.error_message, ''), COALESCE(run.stop_reason, ''),
			run.created_at, run.started_at, run.finished_at, run.lease_until, run.heartbeat_at, run.lease_token, run.updated_at`).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.ParentRunID, &run.RunKind, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.LeaseUntil, &run.HeartbeatAt, &run.LeaseToken, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNoRun
	}
	if err != nil {
		return Run{}, fmt.Errorf("claim agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_run_attempts (agent_run_id, attempt_count, status, started_at, updated_at)
		VALUES ($1, $2, 'running', $3, $3)
		ON CONFLICT (agent_run_id, attempt_count)
		DO UPDATE SET status = 'running', error_message = NULL, stop_reason = NULL,
		              started_at = EXCLUDED.started_at, finished_at = NULL, updated_at = EXCLUDED.updated_at`,
		run.ID, run.AttemptCount, run.StartedAt); err != nil {
		return Run{}, fmt.Errorf("record agent run attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit claim agent run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) ListChildren(ctx context.Context, parentRunID, knowledgeBaseID int64) ([]Run, error) {
	if parentRunID <= 0 || knowledgeBaseID <= 0 {
		return nil, ErrInvalidRun
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, knowledge_base_id, COALESCE(conversation_id, 0),
			COALESCE(parent_run_id, 0), run_kind, response, status, attempt_count,
			COALESCE(error_message, ''), COALESCE(stop_reason, ''), created_at, started_at, finished_at,
			lease_until, heartbeat_at, updated_at
		FROM agent_runs
		WHERE parent_run_id = $1 AND knowledge_base_id = $2
		ORDER BY created_at, id`, parentRunID, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list child agent runs: %w", err)
	}
	defer rows.Close()
	children := make([]Run, 0)
	for rows.Next() {
		var run Run
		if err := rows.Scan(
			&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID,
			&run.ParentRunID, &run.RunKind, &run.Response, &run.Status, &run.AttemptCount,
			&run.ErrorMessage, &run.StopReason, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
			&run.LeaseUntil, &run.HeartbeatAt, &run.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan child agent run: %w", err)
		}
		children = append(children, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate child agent runs: %w", err)
	}
	return children, nil
}

func (s *PostgresStore) ListAttempts(ctx context.Context, agentRunID int64) ([]Attempt, error) {
	if agentRunID <= 0 {
		return nil, ErrInvalidRun
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_run_id, attempt_count, status,
		       COALESCE(error_message, ''), COALESCE(stop_reason, ''),
		       started_at, finished_at, updated_at
		FROM agent_run_attempts
		WHERE agent_run_id = $1
		ORDER BY attempt_count, id`, agentRunID)
	if err != nil {
		return nil, fmt.Errorf("list agent run attempts: %w", err)
	}
	defer rows.Close()
	attempts := make([]Attempt, 0)
	for rows.Next() {
		var attempt Attempt
		if err := rows.Scan(
			&attempt.ID, &attempt.AgentRunID, &attempt.AttemptCount, &attempt.Status,
			&attempt.ErrorMessage, &attempt.StopReason, &attempt.StartedAt,
			&attempt.FinishedAt, &attempt.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent run attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent run attempts: %w", err)
	}
	return attempts, nil
}

func (s *PostgresStore) SaveResponse(ctx context.Context, id int64, response json.RawMessage) error {
	if id <= 0 || len(response) == 0 || !json.Valid(response) {
		return ErrInvalidRun
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs
		SET response = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running'`, id, response)
	if err != nil {
		return fmt.Errorf("save agent run response: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return fmt.Errorf("save agent run response: no running row")
	}
	return nil
}

type persistedCheckpointState struct {
	Version             int                          `json:"version"`
	LastStep            int                          `json:"last_step"`
	CurrentNode         string                       `json:"current_node,omitempty"`
	Messages            []json.RawMessage            `json:"messages"`
	SummaryText         string                       `json:"summary_text"`
	PendingToolCalls    []json.RawMessage            `json:"pending_tool_calls,omitempty"`
	ToolResultRefs      map[string]string            `json:"tool_result_refs,omitempty"`
	Interrupt           *agentruntime.InterruptState `json:"interrupt,omitempty"`
	ApprovedToolCallIDs []string                     `json:"approved_tool_call_ids,omitempty"`
	RejectedToolCallIDs []string                     `json:"rejected_tool_call_ids,omitempty"`
}

func (s *PostgresStore) SaveCheckpoint(ctx context.Context, checkpoint Checkpoint) error {
	if checkpoint.AgentRunID <= 0 || checkpoint.AttemptCount <= 0 || checkpoint.StepNumber < 0 ||
		strings.TrimSpace(checkpoint.CheckpointID) == "" || strings.TrimSpace(checkpoint.LeaseToken) == "" || len(checkpoint.State) == 0 || !json.Valid(checkpoint.State) {
		return ErrInvalidRun
	}
	state, err := s.externalizeCheckpointState(ctx, checkpoint)
	if err != nil {
		return err
	}
	var persisted persistedCheckpointState
	if err := json.Unmarshal(state, &persisted); err != nil || persisted.Version <= 0 {
		return ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var leaseToken string
	var status Status
	if err := tx.QueryRowContext(ctx, `
		SELECT lease_token, status
		FROM agent_runs
		WHERE id = $1
		FOR UPDATE`, checkpoint.AgentRunID).Scan(&leaseToken, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("lock agent run for checkpoint: %w", err)
	}
	if leaseToken != checkpoint.LeaseToken || status != StatusRunning {
		return ErrCheckpointConflict
	}
	var currentVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX((state->>'version')::integer), 0)
		FROM agent_checkpoints
		WHERE agent_run_id = $1`, checkpoint.AgentRunID).Scan(&currentVersion); err != nil {
		return fmt.Errorf("read agent checkpoint version: %w", err)
	}
	if checkpoint.ExpectedVersion != currentVersion || persisted.Version <= currentVersion {
		return ErrCheckpointConflict
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_checkpoints
			(agent_run_id, conversation_id, attempt_count, step_number, checkpoint_id, state)
		VALUES ($1, NULLIF($2, 0), $3, $4, $5, $6)
		ON CONFLICT (agent_run_id, attempt_count, checkpoint_id)
		DO UPDATE SET step_number = EXCLUDED.step_number,
		              state = EXCLUDED.state,
		              updated_at = CURRENT_TIMESTAMP`,
		checkpoint.AgentRunID, checkpoint.ConversationID, checkpoint.AttemptCount,
		checkpoint.StepNumber, checkpoint.CheckpointID, state)
	if err != nil {
		return fmt.Errorf("save agent checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent checkpoint: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetLatestCheckpoint(ctx context.Context, agentRunID int64) (*Checkpoint, error) {
	if agentRunID <= 0 {
		return nil, ErrInvalidRun
	}
	var checkpoint Checkpoint
	err := s.db.QueryRowContext(ctx, `
		SELECT id, agent_run_id, COALESCE(conversation_id, 0), attempt_count, step_number, checkpoint_id, state, created_at
		FROM agent_checkpoints
		WHERE agent_run_id = $1
		ORDER BY attempt_count DESC, step_number DESC, id DESC
		LIMIT 1`, agentRunID).Scan(
		&checkpoint.ID, &checkpoint.AgentRunID, &checkpoint.ConversationID, &checkpoint.AttemptCount,
		&checkpoint.StepNumber, &checkpoint.CheckpointID, &checkpoint.State, &checkpoint.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent checkpoint: %w", err)
	}
	if err := s.restoreCheckpointState(ctx, &checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func (s *PostgresStore) GetLatestThreadCheckpoint(ctx context.Context, conversationID int64) (*Checkpoint, error) {
	if conversationID <= 0 {
		return nil, ErrInvalidRun
	}
	var checkpoint Checkpoint
	err := s.db.QueryRowContext(ctx, `
		SELECT checkpoint.id, checkpoint.agent_run_id, checkpoint.conversation_id,
		       checkpoint.attempt_count, checkpoint.step_number, checkpoint.checkpoint_id,
		       checkpoint.state, checkpoint.created_at
		FROM agent_checkpoints AS checkpoint
		JOIN agent_runs AS run ON run.id = checkpoint.agent_run_id
		WHERE checkpoint.conversation_id = $1
		  AND run.run_kind = 'root'
		  AND run.status = 'succeeded'
		ORDER BY checkpoint.created_at DESC, checkpoint.id DESC
		LIMIT 1`, conversationID).Scan(
		&checkpoint.ID, &checkpoint.AgentRunID, &checkpoint.ConversationID, &checkpoint.AttemptCount,
		&checkpoint.StepNumber, &checkpoint.CheckpointID, &checkpoint.State, &checkpoint.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get thread checkpoint: %w", err)
	}
	if err := s.restoreCheckpointState(ctx, &checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func (s *PostgresStore) DeleteThreadCheckpoints(ctx context.Context, conversationID int64) error {
	if conversationID <= 0 {
		return ErrInvalidRun
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agent_checkpoints WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("delete thread checkpoints: %w", err)
	}
	return nil
}

func (s *PostgresStore) externalizeCheckpointState(ctx context.Context, checkpoint Checkpoint) ([]byte, error) {
	var state persistedCheckpointState
	if err := json.Unmarshal(checkpoint.State, &state); err != nil {
		return nil, fmt.Errorf("decode agent checkpoint state: %w", err)
	}
	if len(state.Messages) == 0 {
		return nil, ErrInvalidRun
	}
	for index, rawMessage := range state.Messages {
		var message map[string]any
		if err := json.Unmarshal(rawMessage, &message); err != nil {
			return nil, fmt.Errorf("decode agent checkpoint message: %w", err)
		}
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		if role != "tool" || len(content) <= s.checkpointInline {
			continue
		}
		if s.checkpointBlobs == nil {
			message["content"] = truncateCheckpointText(content, s.checkpointInline)
			state.Messages[index], _ = json.Marshal(message)
			continue
		}
		ref, err := s.checkpointBlobs.Put(ctx,
			fmt.Sprintf("run-%d/attempt-%d/checkpoint-%s-message-%d", checkpoint.AgentRunID, checkpoint.AttemptCount, checkpoint.CheckpointID, index), content)
		if err != nil {
			return nil, fmt.Errorf("externalize agent checkpoint tool result: %w", err)
		}
		if state.ToolResultRefs == nil {
			state.ToolResultRefs = make(map[string]string)
		}
		state.ToolResultRefs[strconv.Itoa(index)] = ref
		message["content"] = truncateCheckpointText(content, 4096)
		state.Messages[index], _ = json.Marshal(message)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode agent checkpoint state: %w", err)
	}
	return payload, nil
}

func (s *PostgresStore) restoreCheckpointState(ctx context.Context, checkpoint *Checkpoint) error {
	if checkpoint == nil || len(checkpoint.State) == 0 {
		return ErrInvalidRun
	}
	var state persistedCheckpointState
	if err := json.Unmarshal(checkpoint.State, &state); err != nil {
		return fmt.Errorf("decode stored agent checkpoint state: %w", err)
	}
	for key, ref := range state.ToolResultRefs {
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 || index >= len(state.Messages) {
			return fmt.Errorf("invalid agent checkpoint tool result reference")
		}
		if s.checkpointBlobs == nil {
			return fmt.Errorf("agent checkpoint blob store is unavailable")
		}
		content, err := s.checkpointBlobs.Get(ctx, ref)
		if err != nil {
			return fmt.Errorf("restore agent checkpoint tool result: %w", err)
		}
		var message map[string]any
		if err := json.Unmarshal(state.Messages[index], &message); err != nil {
			return fmt.Errorf("decode stored agent checkpoint message: %w", err)
		}
		message["content"] = content
		state.Messages[index], _ = json.Marshal(message)
	}
	state.ToolResultRefs = nil
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode restored agent checkpoint state: %w", err)
	}
	checkpoint.State = payload
	return nil
}

// DeleteThreadData removes all durable Agent runs for a conversation. Child
// runs are deleted through agent_runs' ON DELETE CASCADE, which also removes
// their events and recovery checkpoints. Redis transport entries are
// intentionally not touched here; they are short-lived and expire on their
// own.
func (s *PostgresStore) DeleteThreadData(ctx context.Context, conversationID int64) error {
	if conversationID <= 0 {
		return ErrInvalidRun
	}
	if _, err := s.db.ExecContext(ctx, `
		WITH RECURSIVE run_tree AS (
			SELECT id FROM agent_runs WHERE conversation_id = $1
			UNION ALL
			SELECT child.id
			FROM agent_runs child
			JOIN run_tree parent ON child.parent_run_id = parent.id
		)
		DELETE FROM agent_runs
		WHERE id IN (SELECT id FROM run_tree)`, conversationID); err != nil {
		return fmt.Errorf("delete conversation agent runs: %w", err)
	}
	return nil
}

func truncateCheckpointText(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 3 {
		return value[:maxBytes]
	}
	limit := maxBytes - len("...")
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit]) + "..."
}

func (s *PostgresStore) RequeueExpired(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin requeue expired agent runs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = CASE WHEN attempt_count >= $1 THEN 'failed' ELSE 'pending' END,
			lease_until = NULL, heartbeat_at = NULL, lease_token = NULL,
			stop_reason = CASE WHEN attempt_count >= $1
				THEN 'orphan_recovered'
				ELSE NULL END,
			error_message = CASE WHEN attempt_count >= $1
				THEN 'worker lease expired: maximum attempts reached'
				ELSE 'worker lease expired' END,
			finished_at = CASE WHEN attempt_count >= $1 THEN CURRENT_TIMESTAMP ELSE NULL END,
			updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running' AND lease_until IS NOT NULL AND lease_until <= CURRENT_TIMESTAMP`, maxAgentRunAttempts); err != nil {
		return fmt.Errorf("requeue expired agent runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts AS attempt
		SET status = CASE WHEN run.status = 'failed' THEN 'failed' ELSE 'requeued' END,
			error_message = NULLIF(run.error_message, ''),
			stop_reason = CASE WHEN run.status = 'failed' THEN run.stop_reason ELSE 'orphan_recovered' END,
			finished_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		FROM agent_runs AS run
		WHERE attempt.agent_run_id = run.id
		  AND attempt.attempt_count = run.attempt_count
		  AND attempt.status = 'running'
		  AND run.status IN ('pending', 'failed')
		  AND run.error_message IN ('worker lease expired', 'worker lease expired: maximum attempts reached')`); err != nil {
		return fmt.Errorf("record expired agent run attempts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit requeue expired agent runs: %w", err)
	}
	return nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, id int64, leaseToken string) error {
	if id <= 0 || leaseToken == "" {
		return ErrInvalidRun
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs
		SET lease_until = CURRENT_TIMESTAMP + INTERVAL '5 minutes',
			heartbeat_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running' AND lease_token = $2`, id, leaseToken)
	if err != nil {
		return fmt.Errorf("renew agent run lease: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return fmt.Errorf("renew agent run lease: no running row")
	}
	return nil
}

func (s *PostgresStore) MarkSucceeded(ctx context.Context, id int64, leaseToken string) error {
	return s.markFinished(ctx, id, leaseToken, StatusSucceeded, "")
}

func (s *PostgresStore) MarkFailed(ctx context.Context, id int64, message, leaseToken string) error {
	return s.MarkFailedWithReason(ctx, id, message, StopReasonInternalError, leaseToken)
}

func (s *PostgresStore) MarkFailedWithReason(ctx context.Context, id int64, message, reason, leaseToken string) error {
	return s.markFinishedWithReason(ctx, id, leaseToken, StatusFailed, message, reason)
}

func (s *PostgresStore) MarkTimedOut(ctx context.Context, id int64, message, leaseToken string) error {
	return s.markFinishedWithReason(ctx, id, leaseToken, StatusTimeout, message, StopReasonTimeout)
}

func (s *PostgresStore) MarkCanceled(ctx context.Context, id int64, leaseToken string) error {
	return s.MarkCanceledWithReason(ctx, id, StopReasonCanceled, leaseToken)
}

func (s *PostgresStore) MarkCanceledWithReason(ctx context.Context, id int64, reason, leaseToken string) error {
	return s.markFinishedWithReason(ctx, id, leaseToken, StatusCanceled, "", reason)
}

func (s *PostgresStore) markFinished(ctx context.Context, id int64, leaseToken string, status Status, message string) error {
	return s.markFinishedWithReason(ctx, id, leaseToken, status, message, "")
}

func (s *PostgresStore) markFinishedWithReason(ctx context.Context, id int64, leaseToken string, status Status, message, reason string) error {
	if id <= 0 || leaseToken == "" {
		return ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark agent run %s: %w", status, err)
	}
	defer func() { _ = tx.Rollback() }()
	var attemptCount int
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_runs
		SET status = $2, error_message = NULLIF($3, ''), stop_reason = NULLIF($4, ''), finished_at = CURRENT_TIMESTAMP,
			lease_until = NULL, heartbeat_at = NULL, lease_token = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running' AND lease_token = $5
		RETURNING attempt_count`, id, status, message, reason, leaseToken).Scan(&attemptCount)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("mark agent run %s: no running row", status)
	}
	if err != nil {
		return fmt.Errorf("mark agent run %s: %w", status, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET status = $2, error_message = NULLIF($3, ''), stop_reason = NULLIF($4, ''),
			finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE agent_run_id = $1 AND attempt_count = $5 AND status IN ('running', 'waiting_children')`, id, status, message, reason, attemptCount); err != nil {
		return fmt.Errorf("update agent run attempt %s: %w", status, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent run %s: %w", status, err)
	}
	return nil
}

func (s *PostgresStore) MarkWaitingChildren(ctx context.Context, id int64, leaseToken string) error {
	if id <= 0 || strings.TrimSpace(leaseToken) == "" {
		return ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin waiting children update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attemptCount int
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_runs
		SET status = 'waiting_children', lease_until = NULL, lease_token = NULL,
			heartbeat_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running' AND lease_token = $2
		RETURNING attempt_count`, id, leaseToken).Scan(&attemptCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("mark agent run waiting for children: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET status = 'waiting_children', updated_at = CURRENT_TIMESTAMP
		WHERE agent_run_id = $1 AND attempt_count = $2 AND status = 'running'`, id, attemptCount); err != nil {
		return fmt.Errorf("update waiting child attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit waiting children update: %w", err)
	}
	return nil
}

// MarkWaitingApproval releases the Worker lease after the Engine has saved an
// approval interrupt in the unified checkpoint. The run can therefore be
// resumed by any Worker after the user's decision arrives.
func (s *PostgresStore) MarkWaitingApproval(ctx context.Context, id int64, leaseToken string) error {
	if id <= 0 || strings.TrimSpace(leaseToken) == "" {
		return ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin waiting approval update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attemptCount int
	if err := tx.QueryRowContext(ctx, `
		UPDATE agent_runs
		SET status = 'waiting_approval', lease_until = NULL, lease_token = NULL,
			heartbeat_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running' AND lease_token = $2
		RETURNING attempt_count`, id, leaseToken).Scan(&attemptCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("mark agent run waiting for approval: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET status = 'requeued', updated_at = CURRENT_TIMESTAMP
		WHERE agent_run_id = $1 AND attempt_count = $2 AND status = 'running'`, id, attemptCount); err != nil {
		return fmt.Errorf("update waiting approval attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit waiting approval update: %w", err)
	}
	return nil
}

// ResolveApproval updates the interrupt inside the latest unified checkpoint
// and requeues the run in one transaction. Approved calls are executed once
// by the next Worker; rejected calls are returned to the model as a tool
// observation so it can choose another plan.
func (s *PostgresStore) ResolveApproval(ctx context.Context, runID string, knowledgeBaseID int64, approved bool) error {
	if strings.TrimSpace(runID) == "" || knowledgeBaseID <= 0 {
		return ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin resolve agent approval: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var internalID int64
	var attemptCount int
	var status Status
	if err := tx.QueryRowContext(ctx, `
		SELECT id, attempt_count, status
		FROM agent_runs
		WHERE run_id = $1 AND knowledge_base_id = $2
		FOR UPDATE`, runID, knowledgeBaseID).Scan(&internalID, &attemptCount, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("lock agent run for approval: %w", err)
	}
	if status != StatusWaitingApproval {
		return ErrApprovalNotFound
	}
	var checkpointDBID int64
	var rawState []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT id, state
		FROM agent_checkpoints
		WHERE agent_run_id = $1
		ORDER BY attempt_count DESC, step_number DESC, id DESC
		LIMIT 1
		FOR UPDATE`, internalID).Scan(&checkpointDBID, &rawState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrApprovalNotFound
		}
		return fmt.Errorf("load approval checkpoint: %w", err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(rawState, &state); err != nil {
		return fmt.Errorf("decode approval checkpoint: %w", err)
	}
	var interrupt agentruntime.InterruptState
	interruptPayload, ok := state["interrupt"]
	if !ok || json.Unmarshal(interruptPayload, &interrupt) != nil || interrupt.ToolCallID == "" {
		return ErrApprovalNotFound
	}
	key := "rejected_tool_call_ids"
	if approved {
		key = "approved_tool_call_ids"
	}
	var resolvedIDs []string
	if existing := state[key]; len(existing) > 0 {
		if err := json.Unmarshal(existing, &resolvedIDs); err != nil {
			return fmt.Errorf("decode approval decisions: %w", err)
		}
	}
	resolvedIDs = append(resolvedIDs, interrupt.ToolCallID)
	state[key], _ = json.Marshal(resolvedIDs)
	delete(state, "interrupt")
	var version int
	if versionPayload := state["version"]; len(versionPayload) > 0 {
		_ = json.Unmarshal(versionPayload, &version)
	}
	if version <= 0 {
		version = 1
	}
	state["version"], _ = json.Marshal(version + 1)
	updatedState, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode approval checkpoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_checkpoints
		SET state = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1`, checkpointDBID, updatedState); err != nil {
		return fmt.Errorf("save approval checkpoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = 'pending', lease_until = NULL, lease_token = NULL,
			heartbeat_at = NULL, started_at = NULL, finished_at = NULL,
			error_message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'waiting_approval'`, internalID); err != nil {
		return fmt.Errorf("requeue approved agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET status = 'requeued', updated_at = CURRENT_TIMESTAMP
		WHERE agent_run_id = $1 AND attempt_count = $2 AND status = 'requeued'`, internalID, attemptCount); err != nil {
		return fmt.Errorf("record approval resolution: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent approval: %w", err)
	}
	return nil
}

func (s *PostgresStore) ResumeParentIfChildrenTerminal(ctx context.Context, parentID int64) (bool, error) {
	if parentID <= 0 {
		return false, ErrInvalidRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin parent resume transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var status Status
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id = $1 FOR UPDATE`, parentID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrRunNotFound
		}
		return false, fmt.Errorf("lock parent agent run: %w", err)
	}
	if status != StatusWaitingChildren {
		return false, nil
	}
	var unfinished bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_runs
			WHERE parent_run_id = $1
			  AND status NOT IN ('succeeded', 'failed', 'timeout', 'canceled')
		)`, parentID).Scan(&unfinished); err != nil {
		return false, fmt.Errorf("check child agent runs: %w", err)
	}
	if unfinished {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = 'pending', started_at = NULL, finished_at = NULL,
			error_message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'waiting_children'`, parentID); err != nil {
		return false, fmt.Errorf("requeue parent agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit parent agent resume: %w", err)
	}
	return true, nil
}
