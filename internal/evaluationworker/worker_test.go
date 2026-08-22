package evaluationworker_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/evaluationworker"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/retrievaleval"
)

type storeStub struct {
	run       evaluationrun.Run
	results   []evaluationrun.CaseResult
	succeeded bool
	failed    bool
}

func (s *storeStub) Get(context.Context, int64, int64) (evaluationrun.Run, error) { return s.run, nil }
func (s *storeStub) ListResults(context.Context, int64) ([]evaluationrun.CaseResult, error) {
	return s.results, nil
}

func (s *storeStub) Create(context.Context, evaluationrun.CreateInput) (evaluationrun.Run, error) {
	return s.run, nil
}
func (s *storeStub) RequeueExpired(context.Context) error                 { return nil }
func (s *storeStub) ClaimNext(context.Context) (evaluationrun.Run, error) { return s.run, nil }
func (s *storeStub) SaveCaseResult(_ context.Context, result evaluationrun.CaseResult) error {
	s.results = append(s.results, result)
	return nil
}
func (s *storeStub) MarkSucceeded(context.Context, int64, string) error {
	s.succeeded = true
	return nil
}
func (s *storeStub) MarkFailed(context.Context, int64, string, string) error {
	s.failed = true
	return nil
}

type providerStub struct{ cases []retrievaleval.Case }

func (p providerStub) Cases(context.Context, evaluationrun.Run) ([]retrievaleval.Case, error) {
	return p.cases, nil
}

type answererStub struct{}

func (answererStub) Answer(context.Context, int64, string, int) (rag.Response, error) {
	return rag.Response{Answer: "生成答案", Sources: []retrieval.Result{{DocumentID: 1, Position: 0}}}, nil
}

func TestRunOncePersistsEachCaseBeforeCompletingRun(t *testing.T) {
	store := &storeStub{run: evaluationrun.Run{ID: 9, KnowledgeBaseID: 3, LeaseToken: "lease"}}
	worker := evaluationworker.Worker{
		Store: store, Cases: providerStub{cases: []retrievaleval.Case{
			{ID: "1", KnowledgeBaseID: 3, Question: "问题一", ExpectedChunkIDs: []string{"1:0"}},
			{ID: "2", KnowledgeBaseID: 3, Question: "问题二", ExpectedChunkIDs: []string{"1:0"}},
		}}, Answerer: answererStub{}, TopK: 5,
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.results) != 2 || !store.succeeded || store.failed {
		t.Fatalf("unexpected worker state: results=%d succeeded=%v failed=%v", len(store.results), store.succeeded, store.failed)
	}
	if store.results[0].RunID != 9 || store.results[0].CaseID != 1 {
		t.Fatalf("unexpected persisted result: %#v", store.results[0])
	}
	if !json.Valid(store.results[0].RetrievedIDs) {
		t.Fatalf("retrieved ids are not JSON: %s", store.results[0].RetrievedIDs)
	}
}

type countingAnswerer struct{ calls int }

func (a *countingAnswerer) Answer(context.Context, int64, string, int) (rag.Response, error) {
	a.calls++
	return rag.Response{Answer: "生成答案", Sources: []retrieval.Result{{DocumentID: 1, Position: 0}}}, nil
}

func TestRunOnceSkipsCasesAlreadyPersistedAfterLeaseRecovery(t *testing.T) {
	store := &storeStub{
		run:     evaluationrun.Run{ID: 10, KnowledgeBaseID: 3, LeaseToken: "lease"},
		results: []evaluationrun.CaseResult{{RunID: 10, CaseID: 1}},
	}
	answerer := &countingAnswerer{}
	worker := evaluationworker.Worker{
		Store: store, Cases: providerStub{cases: []retrievaleval.Case{
			{ID: "1", KnowledgeBaseID: 3, Question: "已完成", ExpectedChunkIDs: []string{"1:0"}},
			{ID: "2", KnowledgeBaseID: 3, Question: "待完成", ExpectedChunkIDs: []string{"1:0"}},
		}}, Answerer: answerer, TopK: 5,
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if answerer.calls != 1 || len(store.results) != 2 {
		t.Fatalf("recovery replayed completed case: calls=%d results=%d", answerer.calls, len(store.results))
	}
}
