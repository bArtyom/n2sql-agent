package agentrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

var (
	ErrNoRun       = errors.New("no pending agent run")
	ErrRunNotFound = errors.New("agent run not found")
	ErrInvalidRun  = errors.New("invalid agent run")
)

const defaultLeaseDuration = 5 * time.Minute

type Run struct {
	ID              int64            `json:"id"`
	RunID           string           `json:"run_id"`
	KnowledgeBaseID int64            `json:"knowledge_base_id"`
	ConversationID  int64            `json:"conversation_id,omitempty"`
	Request         json.RawMessage  `json:"request"`
	Response        json.RawMessage  `json:"response,omitempty"`
	Status          Status           `json:"status"`
	AttemptCount    int              `json:"attempt_count"`
	ErrorMessage    string           `json:"error_message,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	StartedAt       *time.Time       `json:"started_at,omitempty"`
	FinishedAt      *time.Time       `json:"finished_at,omitempty"`
	LeaseUntil      *time.Time       `json:"lease_until,omitempty"`
	HeartbeatAt     *time.Time       `json:"heartbeat_at,omitempty"`
	UpdatedAt       time.Time        `json:"updated_at"`
	Checkpoints     []ToolCheckpoint `json:"-"`
}

type CreateInput struct {
	RunID           string
	KnowledgeBaseID int64
	ConversationID  int64
	Request         json.RawMessage
}

type Store interface {
	Create(context.Context, CreateInput) (Run, error)
	ClaimNext(context.Context) (Run, error)
	RequeueExpired(context.Context) error
	RenewLease(context.Context, int64) error
	MarkSucceeded(context.Context, int64) error
	MarkFailed(context.Context, int64, string) error
	MarkCanceled(context.Context, int64) error
}

// Reader exposes safe run metadata without exposing the persisted request
// snapshot. It is intentionally separate from Store so read-only API handlers
// do not need task mutation capabilities.
type Reader interface {
	Get(context.Context, string, int64) (Run, error)
}

type ResultWriter interface {
	SaveResponse(context.Context, int64, json.RawMessage) error
}

// ToolCheckpointStore persists metadata for a completed tool call. Large
// results are externalized by the configured blob store instead of being
// copied into PostgreSQL.
type ToolCheckpointStore interface {
	SaveToolCheckpoint(context.Context, ToolCheckpoint) error
	ListToolCheckpoints(context.Context, int64) ([]ToolCheckpoint, error)
}

type ToolCheckpoint struct {
	AgentRunID    int64
	AttemptCount  int
	StepNumber    int
	ToolCallID    string
	ToolName      string
	ArgumentsHash string
	Content       string
	Payload       json.RawMessage
}

type PostgresStore struct {
	db               *sql.DB
	checkpointBlobs  *ToolResultFileStore
	checkpointInline int
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
		SELECT id, run_id, knowledge_base_id, COALESCE(conversation_id, 0), request, response,
			status, attempt_count, COALESCE(error_message, ''), created_at, started_at,
			finished_at, lease_until, heartbeat_at, updated_at
		FROM agent_runs
		WHERE run_id = $1 AND knowledge_base_id = $2`, runID, knowledgeBaseID).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt,
		&run.FinishedAt, &run.LeaseUntil, &run.HeartbeatAt, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get agent run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Run, error) {
	if input.RunID == "" || input.KnowledgeBaseID <= 0 || len(input.Request) == 0 || !json.Valid(input.Request) {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_runs (run_id, knowledge_base_id, conversation_id, request)
		VALUES ($1, $2, NULLIF($3, 0), $4)
		RETURNING id, run_id, knowledge_base_id, COALESCE(conversation_id, 0), request, response,
			status, attempt_count, COALESCE(error_message, ''), created_at, started_at, finished_at,
			lease_until, heartbeat_at, updated_at`,
		input.RunID, input.KnowledgeBaseID, input.ConversationID, input.Request).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.LeaseUntil, &run.HeartbeatAt, &run.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create agent run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) ClaimNext(ctx context.Context) (Run, error) {
	var run Run
	err := s.db.QueryRowContext(ctx, `
		WITH next_run AS (
			SELECT id FROM agent_runs
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE agent_runs AS run
		SET status = 'running', attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP, finished_at = NULL,
			error_message = NULL,
			lease_until = CURRENT_TIMESTAMP + INTERVAL '5 minutes',
			heartbeat_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		FROM next_run
		WHERE run.id = next_run.id
		RETURNING run.id, run.run_id, run.knowledge_base_id, COALESCE(run.conversation_id, 0),
			run.request, run.response, run.status, run.attempt_count, COALESCE(run.error_message, ''),
			run.created_at, run.started_at, run.finished_at, run.lease_until, run.heartbeat_at, run.updated_at`).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.Request, &run.Response,
		&run.Status, &run.AttemptCount, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.LeaseUntil, &run.HeartbeatAt, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNoRun
	}
	if err != nil {
		return Run{}, fmt.Errorf("claim agent run: %w", err)
	}
	return run, nil
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

func (s *PostgresStore) SaveToolCheckpoint(ctx context.Context, checkpoint ToolCheckpoint) error {
	if checkpoint.AgentRunID <= 0 || checkpoint.AttemptCount <= 0 || checkpoint.StepNumber <= 0 ||
		checkpoint.ToolCallID == "" || checkpoint.ToolName == "" || checkpoint.ArgumentsHash == "" || checkpoint.Content == "" ||
		len(checkpoint.Payload) == 0 || !json.Valid(checkpoint.Payload) {
		return ErrInvalidRun
	}
	envelope := struct {
		ArgumentsHash string          `json:"arguments_hash"`
		Content       string          `json:"content,omitempty"`
		ContentRef    string          `json:"content_ref,omitempty"`
		ContentBytes  int             `json:"content_bytes"`
		Event         json.RawMessage `json:"event"`
	}{
		ArgumentsHash: checkpoint.ArgumentsHash,
		ContentBytes:  len(checkpoint.Content),
		Event:         checkpoint.Payload,
	}
	if s.checkpointBlobs != nil && len(checkpoint.Content) > s.checkpointInline {
		ref, err := s.checkpointBlobs.Put(ctx,
			fmt.Sprintf("run-%d/attempt-%d/%s", checkpoint.AgentRunID, checkpoint.AttemptCount, checkpoint.ToolCallID),
			checkpoint.Content)
		if err != nil {
			return fmt.Errorf("externalize agent tool checkpoint: %w", err)
		}
		envelope.ContentRef = ref
		envelope.Content = truncateCheckpointText(checkpoint.Content, 4096)
	} else {
		envelope.Content = truncateCheckpointText(checkpoint.Content, s.checkpointInline)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode agent tool checkpoint: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_run_checkpoints
			(agent_run_id, attempt_count, step_number, tool_call_id, tool_name, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (agent_run_id, attempt_count, tool_call_id)
		DO UPDATE SET step_number = EXCLUDED.step_number,
		              tool_name = EXCLUDED.tool_name,
		              payload = EXCLUDED.payload`,
		checkpoint.AgentRunID, checkpoint.AttemptCount, checkpoint.StepNumber,
		checkpoint.ToolCallID, checkpoint.ToolName, payload)
	if err != nil {
		return fmt.Errorf("save agent tool checkpoint: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListToolCheckpoints(ctx context.Context, agentRunID int64) ([]ToolCheckpoint, error) {
	if agentRunID <= 0 {
		return nil, ErrInvalidRun
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT attempt_count, step_number, tool_call_id, tool_name, payload
		FROM agent_run_checkpoints
		WHERE agent_run_id = $1
		ORDER BY attempt_count, step_number, id`, agentRunID)
	if err != nil {
		return nil, fmt.Errorf("list agent tool checkpoints: %w", err)
	}
	defer rows.Close()
	checkpoints := make([]ToolCheckpoint, 0)
	for rows.Next() {
		var checkpoint ToolCheckpoint
		var envelope struct {
			ArgumentsHash string          `json:"arguments_hash"`
			Content       string          `json:"content"`
			ContentRef    string          `json:"content_ref"`
			Event         json.RawMessage `json:"event"`
		}
		if err := rows.Scan(&checkpoint.AttemptCount, &checkpoint.StepNumber, &checkpoint.ToolCallID, &checkpoint.ToolName, &checkpoint.Payload); err != nil {
			return nil, fmt.Errorf("scan agent tool checkpoint: %w", err)
		}
		if err := json.Unmarshal(checkpoint.Payload, &envelope); err != nil || envelope.ArgumentsHash == "" || (envelope.Content == "" && envelope.ContentRef == "") || len(envelope.Event) == 0 {
			continue
		}
		if envelope.ContentRef != "" {
			if s.checkpointBlobs == nil {
				continue
			}
			content, err := s.checkpointBlobs.Get(ctx, envelope.ContentRef)
			if err != nil {
				// A missing temporary blob is a cache miss. The Worker will
				// safely re-run a read-only tool instead of using a partial result.
				continue
			}
			envelope.Content = content
		}
		checkpoint.AgentRunID = agentRunID
		checkpoint.ArgumentsHash = envelope.ArgumentsHash
		checkpoint.Content = envelope.Content
		checkpoint.Payload = envelope.Event
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent tool checkpoints: %w", err)
	}
	return checkpoints, nil
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
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = 'pending', lease_until = NULL, heartbeat_at = NULL,
			error_message = 'worker lease expired', updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running' AND lease_until IS NOT NULL AND lease_until <= CURRENT_TIMESTAMP`)
	if err != nil {
		return fmt.Errorf("requeue expired agent runs: %w", err)
	}
	return nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrInvalidRun
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs
		SET lease_until = CURRENT_TIMESTAMP + INTERVAL '5 minutes',
			heartbeat_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running'`, id)
	if err != nil {
		return fmt.Errorf("renew agent run lease: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return fmt.Errorf("renew agent run lease: no running row")
	}
	return nil
}

func (s *PostgresStore) MarkSucceeded(ctx context.Context, id int64) error {
	return s.markFinished(ctx, id, StatusSucceeded, "")
}

func (s *PostgresStore) MarkFailed(ctx context.Context, id int64, message string) error {
	return s.markFinished(ctx, id, StatusFailed, message)
}

func (s *PostgresStore) MarkCanceled(ctx context.Context, id int64) error {
	return s.markFinished(ctx, id, StatusCanceled, "")
}

func (s *PostgresStore) markFinished(ctx context.Context, id int64, status Status, message string) error {
	if id <= 0 {
		return ErrInvalidRun
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = $2, error_message = NULLIF($3, ''), finished_at = CURRENT_TIMESTAMP,
			lease_until = NULL, heartbeat_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'running'`, id, status, message)
	if err != nil {
		return fmt.Errorf("mark agent run %s: %w", status, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return fmt.Errorf("mark agent run %s: no running row", status)
	}
	return nil
}
