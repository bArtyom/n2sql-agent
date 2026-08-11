package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

const (
	maxFailureMessageBytes = 2000
	embeddingBatchSize     = 10
)

var ErrNoTask = errors.New("no pending document processing task")

type Task struct {
	ID           int64
	DocumentID   int64
	AttemptCount int
	StoragePath  string
	ContentType  string
}

type Store interface {
	ClaimNext(context.Context) (Task, error)
	MarkSucceeded(context.Context, int64) error
	MarkFailed(context.Context, int64, string) error
}

type RetryStore interface {
	Requeue(context.Context, int64, string, time.Time) error
	MarkDeadLetter(context.Context, int64, string) error
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
			return Permanent(errors.New("document contains no chunks"))
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
			return Permanent(errors.New("document contains no chunks"))
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
	store       Store
	processor   Processor
	metrics     *metrics.Registry
	retryPolicy RetryPolicy
}

func NewRunner(store Store, processor Processor) *Runner {
	return NewRunnerWithMetricsAndPolicy(store, processor, nil, DefaultRetryPolicy)
}

func NewRunnerWithMetrics(store Store, processor Processor, registry *metrics.Registry) *Runner {
	return NewRunnerWithMetricsAndPolicy(store, processor, registry, DefaultRetryPolicy)
}

func NewRunnerWithPolicy(store Store, processor Processor, policy RetryPolicy) *Runner {
	return NewRunnerWithMetricsAndPolicy(store, processor, nil, policy)
}

func NewRunnerWithMetricsAndPolicy(store Store, processor Processor, registry *metrics.Registry, policy RetryPolicy) *Runner {
	if policy.MaxAttempts <= 0 {
		policy = DefaultRetryPolicy
	}
	return &Runner{store: store, processor: processor, metrics: registry, retryPolicy: policy}
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	claimStarted := time.Now()
	task, err := r.store.ClaimNext(ctx)
	if errors.Is(err, ErrNoTask) {
		return false, nil
	}
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveWorker(metrics.WorkerObservation{Status: metrics.WorkerStatusClaimFailed, Duration: time.Since(claimStarted)})
		}
		slog.ErrorContext(ctx, "document_task_claim_failed",
			"status", "claim_failed",
			"duration_ms", time.Since(claimStarted).Milliseconds(),
			"error", err,
		)
		return false, fmt.Errorf("claim document processing task: %w", err)
	}
	started := time.Now()
	r.recordTask(ctx, slog.LevelInfo, "document_task_started", task, metrics.WorkerStatusStarted, started)
	if err := r.processor(ctx, task); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			r.recordTask(ctx, slog.LevelWarn, "document_task_canceled", task, metrics.WorkerStatusCanceled, started)
			return true, nil
		}
		message := err.Error()
		if len(message) > maxFailureMessageBytes {
			message = message[:maxFailureMessageBytes]
		}
		r.recordTask(ctx, slog.LevelError, "document_task_failed", task, metrics.WorkerStatusFailed, started, "error", message)
		if markErr := r.finishFailedTask(context.WithoutCancel(ctx), task, message, err, started); markErr != nil {
			return true, markErr
		}
		return true, nil
	}
	if err := r.store.MarkSucceeded(context.WithoutCancel(ctx), task.ID); err != nil {
		if r.metrics != nil {
			r.metrics.ObserveWorkerDuration(time.Since(started))
		}
		r.recordTask(ctx, slog.LevelError, "document_task_status_update_failed", task, metrics.WorkerStatusStatusUpdateFailed, started, "target_status", "succeeded", "error", err)
		return true, fmt.Errorf("mark document processing task succeeded: %w", err)
	}
	r.recordTask(ctx, slog.LevelInfo, "document_task_succeeded", task, metrics.WorkerStatusSucceeded, started)
	return true, nil
}

func (r *Runner) finishFailedTask(ctx context.Context, task Task, message string, processingErr error, started time.Time) error {
	retryStore, supportsRetry := r.store.(RetryStore)
	if !supportsRetry {
		if err := r.store.MarkFailed(ctx, task.ID, message); err != nil {
			r.recordTask(ctx, slog.LevelError, "document_task_status_update_failed", task, metrics.WorkerStatusStatusUpdateFailed, started, "target_status", "failed", "error", err)
			return fmt.Errorf("mark document processing task failed: %w", err)
		}
		return nil
	}

	attempt := task.AttemptCount
	if attempt < 1 {
		attempt = 1
	}
	if isRetryable(processingErr) {
		if retryAt, ok := r.retryPolicy.NextRetryAt(time.Now(), attempt); ok {
			if err := retryStore.Requeue(ctx, task.ID, message, retryAt); err != nil {
				r.recordTask(ctx, slog.LevelError, "document_task_status_update_failed", task, metrics.WorkerStatusStatusUpdateFailed, started, "target_status", "pending", "error", err)
				return fmt.Errorf("requeue document processing task: %w", err)
			}
			r.recordTask(ctx, slog.LevelWarn, "document_task_requeued", task, metrics.WorkerStatusRetryScheduled, started, "attempt", attempt, "retry_at", retryAt)
			return nil
		}
	}
	if err := retryStore.MarkDeadLetter(ctx, task.ID, message); err != nil {
		r.recordTask(ctx, slog.LevelError, "document_task_status_update_failed", task, metrics.WorkerStatusStatusUpdateFailed, started, "target_status", "dead_letter", "error", err)
		return fmt.Errorf("mark document processing task dead letter: %w", err)
	}
	r.recordTask(ctx, slog.LevelError, "document_task_dead_letter", task, metrics.WorkerStatusDeadLetter, started, "attempt", attempt)
	return nil
}

func isRetryable(err error) bool {
	if err == nil || errors.Is(err, ErrPermanent) {
		return false
	}
	for _, permanentErr := range []error{
		documentextractor.ErrInvalidStoragePath,
		documentextractor.ErrUnsupportedType,
		documentextractor.ErrInvalidPDF,
		documentextractor.ErrEmptyText,
		fs.ErrNotExist,
	} {
		if errors.Is(err, permanentErr) {
			return false
		}
	}
	return true
}

func (r *Runner) recordTask(ctx context.Context, level slog.Level, event string, task Task, status string, started time.Time, attrs ...any) {
	logTask(ctx, level, event, task, status, started, attrs...)
	if r.metrics != nil {
		r.metrics.ObserveWorker(metrics.WorkerObservation{Status: status, Duration: time.Since(started)})
	}
}

func logTask(ctx context.Context, level slog.Level, event string, task Task, status string, started time.Time, attrs ...any) {
	fields := []any{
		"task_id", task.ID,
		"document_id", task.DocumentID,
		"status", status,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	fields = append(fields, attrs...)
	slog.Default().Log(ctx, level, event, fields...)
}

func (r *Runner) Run(ctx context.Context, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
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
			  AND next_attempt_at <= CURRENT_TIMESTAMP
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE document_processing_tasks AS task
		SET status = 'processing', attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP, completed_at = NULL, error_message = NULL,
			next_attempt_at = CURRENT_TIMESTAMP
		FROM next_task, documents AS document
		WHERE task.id = next_task.id
		  AND document.id = task.document_id
			RETURNING task.id, document.id, task.attempt_count, document.storage_path, document.content_type`).Scan(
		&task.ID, &task.DocumentID, &task.AttemptCount, &task.StoragePath, &task.ContentType,
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

func (s *PostgresStore) Requeue(ctx context.Context, id int64, message string, retryAt time.Time) error {
	return s.updateRetryState(ctx, id, "pending", message, &retryAt)
}

func (s *PostgresStore) MarkDeadLetter(ctx context.Context, id int64, message string) error {
	return s.updateRetryState(ctx, id, "dead_letter", message, nil)
}

func (s *PostgresStore) updateRetryState(ctx context.Context, id int64, status, message string, retryAt *time.Time) error {
	var retryAtValue any
	if retryAt != nil {
		retryAtValue = *retryAt
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE document_processing_tasks
		SET status = $2, error_message = NULLIF($3, ''), next_attempt_at = COALESCE($4, CURRENT_TIMESTAMP), completed_at = CASE WHEN $2 = 'pending' THEN NULL ELSE CURRENT_TIMESTAMP END
		WHERE id = $1 AND status = 'processing'`, id, status, message, retryAtValue)
	if err != nil {
		return fmt.Errorf("update retried document processing task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated retried document processing tasks: %w", err)
	}
	if affected == 0 {
		return errors.New("document processing task is not processing")
	}
	return nil
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
