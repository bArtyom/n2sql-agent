// Package evaluationworker executes durable RAG evaluation runs one case at
// a time so completed cases survive a worker crash.
package evaluationworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/evaluationdataset"
	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrievaleval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var ErrInvalidWorker = errors.New("invalid evaluation worker")

type CaseProvider interface {
	Cases(context.Context, evaluationrun.Run) ([]retrievaleval.Case, error)
}

type SnapshotCaseProvider struct{}

func (SnapshotCaseProvider) Cases(_ context.Context, run evaluationrun.Run) ([]retrievaleval.Case, error) {
	var snapshot evaluationdataset.Snapshot
	if err := json.Unmarshal(run.DatasetSnapshot, &snapshot); err != nil {
		return nil, fmt.Errorf("decode evaluation dataset snapshot: %w", err)
	}
	dataset := snapshot.Dataset()
	pairs, err := dataset.Pairs()
	if err != nil {
		return nil, err
	}
	cases := make([]retrievaleval.Case, 0, len(pairs))
	for _, pair := range pairs {
		ids := make([]string, 0, len(pair.PIDs))
		for _, pid := range pair.PIDs {
			chunkID, ok := snapshot.PassageChunkIDs[strconv.FormatInt(pid, 10)]
			if !ok || chunkID == "" {
				return nil, fmt.Errorf("passage %d has no chunk mapping", pid)
			}
			ids = append(ids, chunkID)
		}
		cases = append(cases, retrievaleval.Case{ID: strconv.FormatInt(pair.QID, 10), KnowledgeBaseID: run.KnowledgeBaseID, Question: pair.Question, ExpectedRelevant: len(ids) > 0, ExpectedChunkIDs: ids, ReferenceAnswer: pair.Answer})
	}
	return cases, nil
}

type Worker struct {
	Store    evaluationrun.Store
	Cases    CaseProvider
	Answerer rag.Answerer
	TopK     int
}

type evaluationConfig struct {
	MaxCaseAttempts           int   `json:"max_case_attempts"`
	PromptCostPer1KMicros     int64 `json:"prompt_cost_per_1k_micros"`
	CompletionCostPer1KMicros int64 `json:"completion_cost_per_1k_micros"`
}

func (w Worker) RunOnce(ctx context.Context) error {
	if ctx == nil || w.Store == nil || w.Cases == nil || w.Answerer == nil || w.TopK <= 0 {
		return ErrInvalidWorker
	}
	if err := w.Store.RequeueExpired(ctx); err != nil {
		return err
	}
	run, err := w.Store.ClaimNext(ctx)
	if errors.Is(err, evaluationrun.ErrNoRun) {
		return nil
	}
	if err != nil {
		return err
	}
	cases, err := w.Cases.Cases(ctx, run)
	if err != nil {
		return w.fail(ctx, run, err)
	}
	config := decodeConfig(run.Config)
	completed := make(map[int64]struct{})
	if reader, ok := w.Store.(evaluationrun.Reader); ok {
		results, readErr := reader.ListResults(ctx, run.ID)
		if readErr != nil {
			return w.fail(ctx, run, readErr)
		}
		for _, result := range results {
			completed[result.CaseID] = struct{}{}
		}
	}
	for _, evaluationCase := range cases {
		caseID := parseCaseID(evaluationCase.ID)
		if _, ok := completed[caseID]; ok {
			continue
		}
		maxAttempts := config.MaxCaseAttempts
		if maxAttempts <= 0 {
			maxAttempts = 2
		}
		var result retrievaleval.RAGCaseResult
		var caseErr error
		var tracker *usage.CallTracker
		attempts := 0
		started := time.Now()
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			attempts = attempt
			tracker = usage.NewCallTracker()
			caseCtx := usage.WithObserver(ctx, tracker)
			caseCtx = usage.WithCallObserver(caseCtx, tracker)
			result, caseErr = retrievaleval.EvaluateRAGCase(caseCtx, w.Answerer, evaluationCase, w.TopK)
			if caseErr == nil {
				break
			}
		}
		finished := time.Now()
		status := evaluationrun.CaseStatusSucceeded
		if caseErr != nil {
			status = evaluationrun.CaseStatusFailed
		}
		caseResult := evaluationrun.CaseResult{
			RunID: run.ID, CaseID: caseID, Question: evaluationCase.Question,
			ReferenceAnswer: evaluationCase.ReferenceAnswer, GeneratedAnswer: result.Answer,
			RetrievedIDs: mustJSON(result.RetrievedIDs), RetrievalMetrics: mustJSON(result.Metrics.RetrievalMetrics),
			GenerationMetrics: mustJSON(result.Metrics.GenerationMetrics), Status: status,
			ExpectedRelevant: evaluationCase.ExpectedRelevant, Refused: result.Refused, CorrectRefusal: result.CorrectRefusal,
			AttemptCount: attempts, DurationMS: finished.Sub(started).Milliseconds(),
			StartedAt: &started, FinishedAt: &finished,
		}
		if caseErr != nil {
			caseResult.ErrorMessage = caseErr.Error()
		}
		caseResult.PromptTokens, caseResult.CompletionTokens, caseResult.TotalTokens, caseResult.EstimatedCostMicros = summarizeUsage(tracker, config)
		if err := w.Store.SaveCaseResult(ctx, caseResult); err != nil {
			return w.fail(ctx, run, err)
		}
	}
	if err := w.Store.MarkSucceeded(ctx, run.ID, run.LeaseToken); err != nil {
		return err
	}
	return nil
}

func decodeConfig(raw json.RawMessage) evaluationConfig {
	var config evaluationConfig
	if json.Unmarshal(raw, &config) != nil {
		return evaluationConfig{}
	}
	if config.MaxCaseAttempts > 8 {
		config.MaxCaseAttempts = 8
	}
	if config.MaxCaseAttempts < 0 {
		config.MaxCaseAttempts = 0
	}
	if config.PromptCostPer1KMicros < 0 {
		config.PromptCostPer1KMicros = 0
	}
	if config.CompletionCostPer1KMicros < 0 {
		config.CompletionCostPer1KMicros = 0
	}
	return config
}

func summarizeUsage(tracker *usage.CallTracker, config evaluationConfig) (prompt, completion, total int, cost int64) {
	if tracker == nil {
		return
	}
	for _, snapshot := range tracker.ModelCallSnapshots() {
		prompt += snapshot.PromptTokens
		completion += snapshot.CompletionTokens
		total += snapshot.TotalTokens
	}
	cost = (int64(prompt)*config.PromptCostPer1KMicros + int64(completion)*config.CompletionCostPer1KMicros) / 1000
	return
}

func (w Worker) fail(ctx context.Context, run evaluationrun.Run, err error) error {
	message := err.Error()
	if markErr := w.Store.MarkFailed(ctx, run.ID, run.LeaseToken, message); markErr != nil {
		return fmt.Errorf("evaluation failed: %v; mark failed: %w", err, markErr)
	}
	return err
}

func parseCaseID(value string) int64 {
	var id int64
	if _, err := fmt.Sscan(value, &id); err != nil {
		return 0
	}
	return id
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
