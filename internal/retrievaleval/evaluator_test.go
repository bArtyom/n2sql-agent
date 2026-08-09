package retrievaleval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/retrievaleval"
)

type searcherStub struct {
	distances map[string][]float64
}

func (s searcherStub) Search(_ context.Context, _ int64, query string, _ int) ([]retrieval.Result, error) {
	results := make([]retrieval.Result, 0)
	for index, distance := range s.distances[query] {
		results = append(results, retrieval.Result{DocumentID: int64(index + 1), Distance: distance})
	}
	return results, nil
}

func TestEvaluateComparesThresholdsWithoutCallingChat(t *testing.T) {
	cases := []retrievaleval.Case{
		{ID: "in-doc", KnowledgeBaseID: 1, Question: "文档问题", ExpectedRelevant: true},
		{ID: "out-of-doc", KnowledgeBaseID: 1, Question: "无关问题", ExpectedRelevant: false},
	}
	report, err := retrievaleval.Evaluate(context.Background(), searcherStub{distances: map[string][]float64{
		"文档问题": {0.64},
		"无关问题": {0.72},
	}}, cases, []float64{0.65, 0.75})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Thresholds[0].Recall != 1 || report.Thresholds[0].RefusalRate != 1 {
		t.Fatalf("threshold 0.65 = %#v, want full recall and refusal", report.Thresholds[0])
	}
	if report.Thresholds[1].UnsupportedAccepts != 1 {
		t.Fatalf("threshold 0.75 = %#v, want one unsupported accept", report.Thresholds[1])
	}
}

func TestLoadCasesRejectsUnknownFields(t *testing.T) {
	_, err := retrievaleval.LoadCases(strings.NewReader(`[{"id":"q1","knowledge_base_id":1,"question":"问题","expected_relevant":true,"extra":1}]`))
	if err == nil {
		t.Fatal("LoadCases() error = nil, want invalid case file")
	}
}

func TestValidateThresholdsRejectsOutsideCosineDistanceRange(t *testing.T) {
	if err := retrievaleval.ValidateThresholds([]float64{-0.1}); err == nil {
		t.Fatal("ValidateThresholds() error = nil for negative threshold")
	}
	if err := retrievaleval.ValidateThresholds([]float64{2.1}); err == nil {
		t.Fatal("ValidateThresholds() error = nil for threshold over 2")
	}
}
