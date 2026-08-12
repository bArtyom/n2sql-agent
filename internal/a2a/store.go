package a2a

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

var ErrNoTask = errors.New("no A2A task available")

type TaskStore interface {
	Create(context.Context, CreateInput) (Task, error)
	Get(context.Context, string) (Task, error)
	ClaimNext(context.Context, time.Duration) (Task, error)
	MarkCompleted(context.Context, string, multiagent.Response) error
	MarkFailed(context.Context, string, string) error
}

type CreateInput struct {
	ID              string
	KnowledgeBaseID int64
	Message         string
	TopK            int
}

type Task struct {
	ID              string
	KnowledgeBaseID int64
	Message         string
	TopK            int
	Status          TaskStatus
	Response        multiagent.Response
	Error           string
	AttemptCount    int
	CreatedAt       time.Time
	StartedAt       time.Time
	CompletedAt     time.Time
	UpdatedAt       time.Time
}

// MemoryStore keeps the old behavior for fast unit tests and local examples.
type MemoryStore struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{tasks: make(map[string]Task)} }

func (s *MemoryStore) Create(_ context.Context, input CreateInput) (Task, error) {
	now := time.Now().UTC()
	task := Task{ID: input.ID, KnowledgeBaseID: input.KnowledgeBaseID, Message: input.Message, TopK: input.TopK, Status: StatusSubmitted, CreatedAt: now, UpdatedAt: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	return task, nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Task, error) {
	s.mu.RLock()
	task, ok := s.tasks[id]
	s.mu.RUnlock()
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	return task, nil
}

func (s *MemoryStore) ClaimNext(_ context.Context, lease time.Duration) (Task, error) {
	_ = lease
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, task := range s.tasks {
		if task.Status != StatusSubmitted {
			continue
		}
		task.Status = StatusWorking
		task.AttemptCount++
		task.StartedAt = time.Now().UTC()
		task.UpdatedAt = task.StartedAt
		s.tasks[id] = task
		return task, nil
	}
	return Task{}, ErrNoTask
}

func (s *MemoryStore) MarkCompleted(_ context.Context, id string, response multiagent.Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	task.Status = StatusCompleted
	task.Response = response
	task.CompletedAt = time.Now().UTC()
	task.UpdatedAt = task.CompletedAt
	s.tasks[id] = task
	return nil
}

func (s *MemoryStore) MarkFailed(_ context.Context, id string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	task.Status = StatusFailed
	task.Error = message
	task.CompletedAt = time.Now().UTC()
	task.UpdatedAt = task.CompletedAt
	s.tasks[id] = task
	return nil
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO a2a_tasks (id, administrator_id, knowledge_base_id, message, top_k, status)
		SELECT $1, settings.administrator_id, kb.id, $3, $4, 'submitted'
		FROM system_settings AS settings
		JOIN knowledge_bases AS kb ON kb.administrator_id = settings.administrator_id
		WHERE settings.id = 1 AND kb.id = $2
		RETURNING id, knowledge_base_id, message, top_k, status, attempt_count, created_at, updated_at`,
		input.ID, input.KnowledgeBaseID, input.Message, input.TopK).Scan(
		&task.ID, &task.KnowledgeBaseID, &task.Message, &task.TopK, &task.Status,
		&task.AttemptCount, &task.CreatedAt, &task.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("%w: knowledge base is unavailable", ErrTaskNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("create A2A task: %w", err)
	}
	return task, nil
}

func (s *PostgresStore) Get(ctx context.Context, id string) (Task, error) {
	var task Task
	var response []byte
	var errorCode sql.NullString
	var startedAt, completedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, knowledge_base_id, message, top_k, status, response, error_code,
		       attempt_count, created_at, started_at, completed_at, updated_at
		FROM a2a_tasks
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id).Scan(
		&task.ID, &task.KnowledgeBaseID, &task.Message, &task.TopK, &task.Status, &response, &errorCode,
		&task.AttemptCount, &task.CreatedAt, &startedAt, &completedAt, &task.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("get A2A task: %w", err)
	}
	if startedAt.Valid {
		task.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = completedAt.Time
	}
	if errorCode.Valid {
		task.Error = errorCode.String
	}
	if len(response) > 0 {
		if err := json.Unmarshal(response, &task.Response); err != nil {
			return Task{}, fmt.Errorf("decode A2A task response: %w", err)
		}
	}
	return task, nil
}

func (s *PostgresStore) ClaimNext(ctx context.Context, lease time.Duration) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(ctx, `
		WITH next_task AS (
			SELECT id
			FROM a2a_tasks
			WHERE status = 'submitted'
			   OR (status = 'working' AND lease_until <= CURRENT_TIMESTAMP)
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE a2a_tasks AS task
		SET status = 'working', attempt_count = task.attempt_count + 1,
			started_at = COALESCE(task.started_at, CURRENT_TIMESTAMP),
			lease_until = CURRENT_TIMESTAMP + ($1 * INTERVAL '1 second'),
			updated_at = CURRENT_TIMESTAMP
		FROM next_task
		WHERE task.id = next_task.id
		RETURNING task.id, task.knowledge_base_id, task.message, task.top_k,
		          task.status, task.attempt_count, task.created_at,
			 task.started_at, task.updated_at`, int64(lease.Seconds())).Scan(
		&task.ID, &task.KnowledgeBaseID, &task.Message, &task.TopK, &task.Status,
		&task.AttemptCount, &task.CreatedAt, &task.StartedAt, &task.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNoTask
	}
	if err != nil {
		return Task{}, fmt.Errorf("claim next A2A task: %w", err)
	}
	return task, nil
}

func (s *PostgresStore) MarkCompleted(ctx context.Context, id string, response multiagent.Response) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode A2A task response: %w", err)
	}
	return s.markTerminal(ctx, id, "completed", encoded)
}

func (s *PostgresStore) MarkFailed(ctx context.Context, id string, message string) error {
	return s.markTerminal(ctx, id, "failed", []byte("null"), message)
}

func (s *PostgresStore) markTerminal(ctx context.Context, id, status string, response []byte, errorMessage ...string) error {
	message := ""
	if len(errorMessage) > 0 {
		message = errorMessage[0]
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE a2a_tasks
		SET status = $2, response = $3::jsonb, error_code = NULLIF($4, ''),
			lease_until = NULL, completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'working'`, id, status, string(response), message)
	if err != nil {
		return fmt.Errorf("mark A2A task %s: %w", status, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated A2A tasks: %w", err)
	}
	if affected == 0 {
		return ErrTaskNotFound
	}
	return nil
}
