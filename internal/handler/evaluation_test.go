package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type evaluationStoreStub struct{ created evaluationrun.CreateInput }

func (s *evaluationStoreStub) Create(_ context.Context, input evaluationrun.CreateInput) (evaluationrun.Run, error) {
	s.created = input
	return evaluationrun.Run{ID: 12, Status: evaluationrun.StatusPending, TotalCases: input.TotalCases}, nil
}
func (*evaluationStoreStub) ClaimNext(context.Context) (evaluationrun.Run, error) {
	return evaluationrun.Run{}, evaluationrun.ErrNoRun
}
func (*evaluationStoreStub) RequeueExpired(context.Context) error { return nil }
func (*evaluationStoreStub) SaveCaseResult(context.Context, evaluationrun.CaseResult) error {
	return nil
}
func (*evaluationStoreStub) MarkSucceeded(context.Context, int64, string) error      { return nil }
func (*evaluationStoreStub) MarkFailed(context.Context, int64, string, string) error { return nil }

type evaluationReaderStub struct{}

func (evaluationReaderStub) Get(context.Context, int64, int64) (evaluationrun.Run, error) {
	now := time.Now()
	return evaluationrun.Run{
		ID:                      12,
		KnowledgeBaseID:         7,
		Status:                  evaluationrun.StatusSucceeded,
		TotalCases:              2,
		FinishedCases:           2,
		ExpectedRelevantCases:   1,
		ExpectedIrrelevantCases: 1,
		CorrectRefusals:         1,
		FalseRefusals:           0,
		UnsupportedAccepts:      0,
		CreatedAt:               now,
		UpdatedAt:               now,
	}, nil
}
func (evaluationReaderStub) ListResults(context.Context, int64) ([]evaluationrun.CaseResult, error) {
	return []evaluationrun.CaseResult{{RunID: 12, CaseID: 1, Question: "问题", GeneratedAnswer: "答案", RetrievedIDs: json.RawMessage(`[]`), RetrievalMetrics: json.RawMessage(`{}`), GenerationMetrics: json.RawMessage(`{}`)}}, nil
}

func TestEvaluationCreatesAndReadsWeKnoraSnapshot(t *testing.T) {
	store := &evaluationStoreStub{}
	handler := handler.NewEvaluation(store, evaluationReaderStub{})
	body := `{"queries":[{"id":1,"text":"问题"}],"corpus":[{"id":10,"text":"段落"}],"answers":[{"id":20,"text":"答案"}],"qrels":[{"qid":1,"pid":10}],"qas":[{"qid":1,"aid":20}],"passage_chunk_ids":{"10":"1:0"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/evaluations", strings.NewReader(body))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || store.created.TotalCases != 1 {
		t.Fatalf("create status=%d input=%#v body=%s", response.Code, store.created, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/evaluations/12", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "12")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"finished_cases":2`) ||
		!strings.Contains(response.Body.String(), `"recall":1`) ||
		!strings.Contains(response.Body.String(), `"refusal_rate":1`) ||
		!strings.Contains(response.Body.String(), `"accuracy":1`) {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
}
