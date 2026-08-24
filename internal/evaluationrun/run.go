// Package evaluationrun owns durable evaluation jobs. It deliberately does
// not know how RAG answers or metrics are calculated.
package evaluationrun

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	ErrNoRun       = errors.New("no pending evaluation run")
	ErrRunNotFound = errors.New("evaluation run not found")
	ErrInvalidRun  = errors.New("invalid evaluation run")
)

func IsTerminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCanceled
}

type CaseStatus string

const (
	CaseStatusSucceeded CaseStatus = "succeeded"
	CaseStatusFailed    CaseStatus = "failed"
)

type Run struct {
	ID                    int64
	KnowledgeBaseID       int64
	DatasetSnapshot       json.RawMessage
	Config                json.RawMessage
	DatasetVersion        string
	KnowledgeBaseSnapshot json.RawMessage
	ModelConfig           json.RawMessage
	Status                Status
	TotalCases            int
	FinishedCases         int
	FailedCases           int
	AttemptCount          int
	LeaseToken            string
	LeaseUntil            *time.Time
	ErrorMessage          string
	CreatedAt             time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
	UpdatedAt             time.Time
	DurationMS            int64
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	EstimatedCostMicros   int64
}

type CreateInput struct {
	KnowledgeBaseID       int64
	DatasetSnapshot       json.RawMessage
	Config                json.RawMessage
	DatasetVersion        string
	KnowledgeBaseSnapshot json.RawMessage
	ModelConfig           json.RawMessage
	TotalCases            int
}

type CaseResult struct {
	RunID               int64
	CaseID              int64
	Question            string
	ReferenceAnswer     string
	GeneratedAnswer     string
	RetrievedIDs        json.RawMessage
	RetrievalMetrics    json.RawMessage
	GenerationMetrics   json.RawMessage
	ErrorMessage        string
	Status              CaseStatus
	AttemptCount        int
	DurationMS          int64
	PromptTokens        int
	CompletionTokens    int
	TotalTokens         int
	EstimatedCostMicros int64
	StartedAt           *time.Time
	FinishedAt          *time.Time
}

type Store interface {
	Create(context.Context, CreateInput) (Run, error)
	ClaimNext(context.Context) (Run, error)
	RequeueExpired(context.Context) error
	SaveCaseResult(context.Context, CaseResult) error
	MarkSucceeded(context.Context, int64, string) error
	MarkFailed(context.Context, int64, string, string) error
}

type Reader interface {
	Get(context.Context, int64, int64) (Run, error)
	ListResults(context.Context, int64) ([]CaseResult, error)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrInvalidRun
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Run, error) {
	if input.KnowledgeBaseID <= 0 || input.TotalCases < 0 || !validJSON(input.DatasetSnapshot) ||
		!validJSONOrEmpty(input.Config) || !validJSONOrEmpty(input.KnowledgeBaseSnapshot) || !validJSONOrEmpty(input.ModelConfig) {
		return Run{}, ErrInvalidRun
	}
	config := input.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	datasetVersion := input.DatasetVersion
	if datasetVersion == "" {
		hash := sha256.Sum256(input.DatasetSnapshot)
		datasetVersion = hex.EncodeToString(hash[:])
	}
	knowledgeSnapshot := input.KnowledgeBaseSnapshot
	if len(knowledgeSnapshot) == 0 {
		knowledgeSnapshot = json.RawMessage(`{}`)
	}
	modelConfig := input.ModelConfig
	if len(modelConfig) == 0 {
		modelConfig = json.RawMessage(`{}`)
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO evaluation_runs (knowledge_base_id, dataset_snapshot, dataset_version, knowledge_base_snapshot, model_config, config, total_cases)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, knowledge_base_id, dataset_snapshot, config, status, total_cases,
			dataset_version, knowledge_base_snapshot, model_config, finished_cases, failed_cases,
			attempt_count, created_at, updated_at`,
		input.KnowledgeBaseID, input.DatasetSnapshot, datasetVersion, knowledgeSnapshot, modelConfig, config, input.TotalCases).Scan(
		&run.ID, &run.KnowledgeBaseID, &run.DatasetSnapshot, &run.Config, &run.Status,
		&run.TotalCases, &run.DatasetVersion, &run.KnowledgeBaseSnapshot, &run.ModelConfig,
		&run.FinishedCases, &run.FailedCases, &run.AttemptCount, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create evaluation run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) ClaimNext(ctx context.Context) (Run, error) {
	var run Run
	err := s.db.QueryRowContext(ctx, `
		WITH next_run AS (
			SELECT id FROM evaluation_runs
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE evaluation_runs AS run
		SET status = 'running', attempt_count = run.attempt_count + 1,
			started_at = CURRENT_TIMESTAMP, finished_at = NULL, error_message = NULL,
			lease_until = CURRENT_TIMESTAMP + INTERVAL '5 minutes',
			lease_token = md5(random()::text || clock_timestamp()::text || run.id::text),
			updated_at = CURRENT_TIMESTAMP
		FROM next_run
		WHERE run.id = next_run.id
		RETURNING run.id, run.knowledge_base_id, run.dataset_snapshot, run.config,
			run.status, run.total_cases, run.dataset_version, run.knowledge_base_snapshot, run.model_config,
			run.finished_cases, run.failed_cases, run.attempt_count,
			run.lease_token, run.lease_until, run.created_at, run.started_at,
			run.finished_at, run.updated_at, run.duration_ms, run.prompt_tokens,
			run.completion_tokens, run.total_tokens, run.estimated_cost_micros`,
	).Scan(&run.ID, &run.KnowledgeBaseID, &run.DatasetSnapshot, &run.Config, &run.Status,
		&run.TotalCases, &run.DatasetVersion, &run.KnowledgeBaseSnapshot, &run.ModelConfig,
		&run.FinishedCases, &run.FailedCases, &run.AttemptCount, &run.LeaseToken,
		&run.LeaseUntil, &run.CreatedAt, &run.StartedAt, &run.FinishedAt, &run.UpdatedAt,
		&run.DurationMS, &run.PromptTokens, &run.CompletionTokens, &run.TotalTokens, &run.EstimatedCostMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNoRun
	}
	if err != nil {
		return Run{}, fmt.Errorf("claim evaluation run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) Get(ctx context.Context, id, knowledgeBaseID int64) (Run, error) {
	if id <= 0 || knowledgeBaseID <= 0 {
		return Run{}, ErrInvalidRun
	}
	var run Run
	err := s.db.QueryRowContext(ctx, `
		SELECT id, knowledge_base_id, dataset_snapshot, config, status, total_cases,
			dataset_version, knowledge_base_snapshot, model_config, finished_cases, failed_cases,
			attempt_count, COALESCE(error_message,''), created_at, started_at, finished_at, updated_at,
			duration_ms, prompt_tokens, completion_tokens, total_tokens, estimated_cost_micros
		FROM evaluation_runs WHERE id = $1 AND knowledge_base_id = $2`, id, knowledgeBaseID).Scan(
		&run.ID, &run.KnowledgeBaseID, &run.DatasetSnapshot, &run.Config, &run.Status,
		&run.TotalCases, &run.DatasetVersion, &run.KnowledgeBaseSnapshot, &run.ModelConfig,
		&run.FinishedCases, &run.FailedCases, &run.AttemptCount, &run.ErrorMessage,
		&run.CreatedAt, &run.StartedAt, &run.FinishedAt, &run.UpdatedAt, &run.DurationMS,
		&run.PromptTokens, &run.CompletionTokens, &run.TotalTokens, &run.EstimatedCostMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get evaluation run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) ListResults(ctx context.Context, runID int64) ([]CaseResult, error) {
	if runID <= 0 {
		return nil, ErrInvalidRun
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT evaluation_run_id, case_id, question, COALESCE(reference_answer,''),
			COALESCE(generated_answer,''), retrieved_ids, retrieval_metrics,
			generation_metrics, COALESCE(error_message,''), status, attempt_count,
			duration_ms, prompt_tokens, completion_tokens, total_tokens, estimated_cost_micros,
			started_at, finished_at
		FROM evaluation_case_results WHERE evaluation_run_id = $1 ORDER BY case_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list evaluation results: %w", err)
	}
	defer rows.Close()
	results := make([]CaseResult, 0)
	for rows.Next() {
		var result CaseResult
		if err := rows.Scan(&result.RunID, &result.CaseID, &result.Question, &result.ReferenceAnswer,
			&result.GeneratedAnswer, &result.RetrievedIDs, &result.RetrievalMetrics,
			&result.GenerationMetrics, &result.ErrorMessage, &result.Status, &result.AttemptCount,
			&result.DurationMS, &result.PromptTokens, &result.CompletionTokens, &result.TotalTokens,
			&result.EstimatedCostMicros, &result.StartedAt, &result.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan evaluation result: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evaluation results: %w", err)
	}
	return results, nil
}

func (s *PostgresStore) RequeueExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE evaluation_runs
		SET status = 'pending', lease_token = NULL, lease_until = NULL,
			started_at = NULL, error_message = 'worker lease expired', updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running' AND lease_until < CURRENT_TIMESTAMP`)
	if err != nil {
		return fmt.Errorf("requeue expired evaluation runs: %w", err)
	}
	return nil
}

func (s *PostgresStore) SaveCaseResult(ctx context.Context, result CaseResult) error {
	if result.RunID <= 0 || result.CaseID <= 0 || result.Question == "" ||
		!validJSONOrEmpty(result.RetrievedIDs) || !validJSONOrEmpty(result.RetrievalMetrics) || !validJSONOrEmpty(result.GenerationMetrics) {
		return ErrInvalidRun
	}
	defaults := func(value json.RawMessage, fallback string) json.RawMessage {
		if len(value) == 0 {
			return json.RawMessage(fallback)
		}
		return value
	}
	result.RetrievedIDs = defaults(result.RetrievedIDs, `[]`)
	result.RetrievalMetrics = defaults(result.RetrievalMetrics, `{}`)
	result.GenerationMetrics = defaults(result.GenerationMetrics, `{}`)
	if result.Status == "" {
		result.Status = CaseStatusSucceeded
	}
	if result.AttemptCount <= 0 {
		result.AttemptCount = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin evaluation result transaction: %w", err)
	}
	defer tx.Rollback()
	var inserted bool
	err = tx.QueryRowContext(ctx, `
		INSERT INTO evaluation_case_results
			(evaluation_run_id, case_id, question, reference_answer, generated_answer,
			 retrieved_ids, retrieval_metrics, generation_metrics, error_message, status,
			 attempt_count, duration_ms, prompt_tokens, completion_tokens, total_tokens,
			 estimated_cost_micros, started_at, finished_at)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,NULLIF($9,''),$10,
			$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (evaluation_run_id, case_id) DO NOTHING
		RETURNING true`, result.RunID, result.CaseID, result.Question, result.ReferenceAnswer,
		result.GeneratedAnswer, result.RetrievedIDs, result.RetrievalMetrics, result.GenerationMetrics,
		result.ErrorMessage, result.Status, result.AttemptCount, result.DurationMS, result.PromptTokens,
		result.CompletionTokens, result.TotalTokens, result.EstimatedCostMicros, result.StartedAt, result.FinishedAt).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("save evaluation case result: %w", err)
	}
	if inserted {
		if _, err = tx.ExecContext(ctx, `UPDATE evaluation_runs SET finished_cases = finished_cases + 1,
			failed_cases = failed_cases + CASE WHEN $2 = 'failed' THEN 1 ELSE 0 END,
			duration_ms = duration_ms + $3, prompt_tokens = prompt_tokens + $4,
			completion_tokens = completion_tokens + $5, total_tokens = total_tokens + $6,
			estimated_cost_micros = estimated_cost_micros + $7, updated_at = CURRENT_TIMESTAMP WHERE id = $1`,
			result.RunID, result.Status, result.DurationMS, result.PromptTokens, result.CompletionTokens,
			result.TotalTokens, result.EstimatedCostMicros); err != nil {
			return fmt.Errorf("advance evaluation progress: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit evaluation result: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkSucceeded(ctx context.Context, id int64, leaseToken string) error {
	return s.markTerminal(ctx, id, leaseToken, StatusSucceeded, "")
}

func (s *PostgresStore) MarkFailed(ctx context.Context, id int64, leaseToken, message string) error {
	return s.markTerminal(ctx, id, leaseToken, StatusFailed, message)
}

func (s *PostgresStore) markTerminal(ctx context.Context, id int64, leaseToken string, status Status, message string) error {
	if id <= 0 || leaseToken == "" {
		return ErrInvalidRun
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE evaluation_runs
		SET status = $3, lease_token = NULL, lease_until = NULL,
			finished_at = CURRENT_TIMESTAMP,
			duration_ms = CASE WHEN started_at IS NULL THEN duration_ms
				ELSE GREATEST(duration_ms, (EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - started_at)) * 1000)::BIGINT) END,
			error_message = NULLIF($4,''), updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND lease_token = $2 AND status = 'running'`, id, leaseToken, status, message)
	if err != nil {
		return fmt.Errorf("mark evaluation run %s: %w", status, err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrRunNotFound
	}
	return nil
}

func validJSON(value json.RawMessage) bool        { return len(value) > 0 && json.Valid(value) }
func validJSONOrEmpty(value json.RawMessage) bool { return len(value) == 0 || json.Valid(value) }
