package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const maxFailureMessageBytes = 2000

var ErrNoTask = errors.New("no pending document processing task")

type Task struct {
	ID          int64
	DocumentID  int64
	StoragePath string
	ContentType string
}

type Store interface {
	ClaimNext(context.Context) (Task, error)
	MarkSucceeded(context.Context, int64) error
	MarkFailed(context.Context, int64, string) error
}

type Processor func(context.Context, Task) error

type TextExtractor interface {
	Extract(context.Context, string, string) (string, error)
}

type TextSplitter interface{ Split(string) []string }
type ChunkStore interface {
	Replace(context.Context, int64, []string) error
}

func NewChunkingProcessor(extractor TextExtractor, splitter TextSplitter, chunks ChunkStore) Processor {
	return func(ctx context.Context, task Task) error {
		text, err := extractor.Extract(ctx, task.StoragePath, task.ContentType)
		if err != nil {
			return err
		}
		parts := splitter.Split(text)
		if len(parts) == 0 {
			return errors.New("document contains no chunks")
		}
		return chunks.Replace(ctx, task.DocumentID, parts)
	}
}

func NewTextExtractionProcessor(extractor TextExtractor) Processor {
	return func(ctx context.Context, task Task) error {
		_, err := extractor.Extract(ctx, task.StoragePath, task.ContentType)
		return err
	}
}

type Runner struct {
	store     Store
	processor Processor
}

func NewRunner(store Store, processor Processor) *Runner {
	return &Runner{store: store, processor: processor}
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	task, err := r.store.ClaimNext(ctx)
	if errors.Is(err, ErrNoTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim document processing task: %w", err)
	}
	if err := r.processor(ctx, task); err != nil {
		message := err.Error()
		if len(message) > maxFailureMessageBytes {
			message = message[:maxFailureMessageBytes]
		}
		if markErr := r.store.MarkFailed(context.WithoutCancel(ctx), task.ID, message); markErr != nil {
			return true, fmt.Errorf("mark document processing task failed: %w", markErr)
		}
		return true, nil
	}
	if err := r.store.MarkSucceeded(context.WithoutCancel(ctx), task.ID); err != nil {
		return true, fmt.Errorf("mark document processing task succeeded: %w", err)
	}
	return true, nil
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) ClaimNext(ctx context.Context) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(ctx, `
		WITH next_task AS (
			SELECT id
			FROM document_processing_tasks
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE document_processing_tasks AS task
		SET status = 'processing', attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP, completed_at = NULL, error_message = NULL
		FROM next_task, documents AS document
		WHERE task.id = next_task.id
		  AND document.id = task.document_id
		RETURNING task.id, document.id, document.storage_path, document.content_type`).Scan(
		&task.ID, &task.DocumentID, &task.StoragePath, &task.ContentType,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNoTask
	}
	if err != nil {
		return Task{}, fmt.Errorf("claim next document processing task: %w", err)
	}
	return task, nil
}

func (s *PostgresStore) MarkSucceeded(ctx context.Context, id int64) error {
	return s.mark(ctx, id, "succeeded", "")
}

func (s *PostgresStore) MarkFailed(ctx context.Context, id int64, message string) error {
	return s.mark(ctx, id, "failed", message)
}

func (s *PostgresStore) mark(ctx context.Context, id int64, status, message string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE document_processing_tasks
		SET status = $2, error_message = NULLIF($3, ''), completed_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'processing'`, id, status, message)
	if err != nil {
		return fmt.Errorf("update document processing task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated document processing tasks: %w", err)
	}
	if affected == 0 {
		return errors.New("document processing task is not processing")
	}
	return nil
}
