package agentrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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
	ErrNoRun      = errors.New("no pending agent run")
	ErrInvalidRun = errors.New("invalid agent run")
)

const defaultLeaseDuration = 5 * time.Minute

type Run struct {
	ID              int64           `json:"id"`
	RunID           string          `json:"run_id"`
	KnowledgeBaseID int64           `json:"knowledge_base_id"`
	ConversationID  int64           `json:"conversation_id,omitempty"`
	Request         json.RawMessage `json:"request"`
	Status          Status          `json:"status"`
	AttemptCount    int             `json:"attempt_count"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	LeaseUntil      *time.Time      `json:"lease_until,omitempty"`
	HeartbeatAt     *time.Time      `json:"heartbeat_at,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
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

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Run, error) {
	if input.RunID == "" || input.KnowledgeBaseID <= 0 || len(input.Request) == 0 || !json.Valid(input.Request) {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_runs (run_id, knowledge_base_id, conversation_id, request)
		VALUES ($1, $2, NULLIF($3, 0), $4)
		RETURNING id, run_id, knowledge_base_id, COALESCE(conversation_id, 0), request,
			status, attempt_count, COALESCE(error_message, ''), created_at, started_at, finished_at,
			lease_until, heartbeat_at, updated_at`,
		input.RunID, input.KnowledgeBaseID, input.ConversationID, input.Request).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.Request,
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
			run.request, run.status, run.attempt_count, COALESCE(run.error_message, ''),
			run.created_at, run.started_at, run.finished_at, run.lease_until, run.heartbeat_at, run.updated_at`).Scan(
		&run.ID, &run.RunID, &run.KnowledgeBaseID, &run.ConversationID, &run.Request,
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
