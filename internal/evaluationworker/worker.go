// Package evaluationworker executes durable RAG evaluation runs one case at
// a time so completed cases survive a worker crash.
package evaluationworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrievaleval"
)

var ErrInvalidWorker = errors.New("invalid evaluation worker")

type CaseProvider interface {
	Cases(context.Context, evaluationrun.Run) ([]retrievaleval.Case, error)
}

type Worker struct {
	Store    evaluationrun.Store
	Cases    CaseProvider
	Answerer rag.Answerer
	TopK     int
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
	for _, evaluationCase := range cases {
		result, err := retrievaleval.EvaluateRAGCase(ctx, w.Answerer, evaluationCase, w.TopK)
		if err != nil {
			return w.fail(ctx, run, err)
		}
		if err := w.Store.SaveCaseResult(ctx, evaluationrun.CaseResult{
			RunID: run.ID, CaseID: parseCaseID(evaluationCase.ID), Question: evaluationCase.Question,
			ReferenceAnswer: evaluationCase.ReferenceAnswer, GeneratedAnswer: result.Answer,
			RetrievedIDs: mustJSON(result.RetrievedIDs), RetrievalMetrics: mustJSON(result.Metrics.RetrievalMetrics),
			GenerationMetrics: mustJSON(result.Metrics.GenerationMetrics),
		}); err != nil {
			return w.fail(ctx, run, err)
		}
	}
	if err := w.Store.MarkSucceeded(ctx, run.ID, run.LeaseToken); err != nil {
		return err
	}
	return nil
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
