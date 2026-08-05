package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

const (
	maxFailureMessageBytes = 2000
	embeddingBatchSize     = 10
)

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
	Replace(context.Context, int64, []string, [][]float32) error
}
type Embedder interface {
	Embed(context.Context, []string) (modelclient.EmbeddingResponse, error)
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
		return chunks.Replace(ctx, task.DocumentID, parts, nil)
	}
}

func NewEmbeddingChunkingProcessor(extractor TextExtractor, splitter TextSplitter, chunks ChunkStore, embedder Embedder) Processor {
	return func(ctx context.Context, task Task) error {
		text, err := extractor.Extract(ctx, task.StoragePath, task.ContentType)
		if err != nil {
			return err
		}
		parts := splitter.Split(text)
		if len(parts) == 0 {
			return errors.New("document contains no chunks")
		}
		embeddings, err := embedChunks(ctx, embedder, parts)
		if err != nil {
			return fmt.Errorf("embed document chunks: %w", err)
		}
		return chunks.Replace(ctx, task.DocumentID, parts, embeddings)
	}
}

func embedChunks(ctx context.Context, embedder Embedder, parts []string) ([][]float32, error) {
	embeddings := make([][]float32, 0, len(parts))
	for start := 0; start < len(parts); start += embeddingBatchSize {
		end := start + embeddingBatchSize
		if end > len(parts) {
			end = len(parts)
		}
		batch := parts[start:end]
		response, err := embedder.Embed(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end-1, err)
		}
		if len(response.Data) != len(batch) {
			return nil, fmt.Errorf("embedding count for batch %d-%d = %d, want %d", start, end-1, len(response.Data), len(batch))
		}
		for _, embedding := range response.Data {
			embeddings = append(embeddings, embedding.Vector)
		}
	}
	return embeddings, nil
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

func (r *Runner) Run(ctx context.Context, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := r.RunOnce(ctx); err != nil && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
