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

type explainableSearcherStub struct{}

type passageSearcherStub struct{}

func (explainableSearcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{
		{DocumentID: 9, Position: 2, Distance: 0.2, MatchType: "hybrid", HeadingScore: 0.8},
		{DocumentID: 4, Position: 1, Distance: 0.3, MatchType: "keyword", ChunkKind: "summary"},
	}, nil
}

func (passageSearcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{
		{DocumentID: 1, Position: 0, Distance: 0.10},
		{DocumentID: 2, Position: 0, Distance: 0.20},
		{DocumentID: 1, Position: 1, Distance: 0.30},
		{DocumentID: 3, Position: 0, Distance: 0.40},
	}, nil
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

func TestEvaluateReportsDocumentHitAndReciprocalRank(t *testing.T) {
	cases := []retrievaleval.Case{
		{
			ID:                  "labeled",
			KnowledgeBaseID:     1,
			Question:            "目标文档",
			ExpectedRelevant:    true,
			ExpectedDocumentIDs: []int64{2},
		},
	}
	report, err := retrievaleval.Evaluate(context.Background(), searcherStub{distances: map[string][]float64{
		"目标文档": {0.61, 0.62},
	}}, cases, []float64{0.65})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	result := report.Thresholds[0]
	if result.DocumentHits != 1 || result.DocumentRecall != 1 || result.MRR != 0.5 {
		t.Fatalf("document metrics = %#v, want hit=1 recall=1 mrr=0.5", result)
	}
	if !report.Thresholds[0].Cases[0].RelevantRetrieved || report.Thresholds[0].Cases[0].FirstRelevantRank != 2 {
		t.Fatalf("case result = %#v, want hit at rank 2", report.Thresholds[0].Cases[0])
	}
}

func TestEvaluateReportsHeadingPathEvidence(t *testing.T) {
	report, err := retrievaleval.Evaluate(context.Background(), explainableSearcherStub{}, []retrievaleval.Case{{
		ID: "heading", KnowledgeBaseID: 1, Question: "Windows 安装", ExpectedRelevant: true, ExpectedDocumentIDs: []int64{9},
	}}, []float64{0.5})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	result := report.Thresholds[0]
	if result.HeadingPathHits != 1 || result.SummaryHits != 1 || result.Cases[0].FirstRelevantMatchType != "hybrid" || result.Cases[0].FirstRelevantHeadingScore != 0.8 {
		t.Fatalf("retrieval evidence = %#v, want heading and summary hits", result)
	}
}

func TestEvaluateReportsPassageRankingMetrics(t *testing.T) {
	report, err := retrievaleval.Evaluate(context.Background(), passageSearcherStub{}, []retrievaleval.Case{{
		ID:               "passages",
		KnowledgeBaseID:  1,
		Question:         "段落问题",
		ExpectedRelevant: true,
		ExpectedChunkIDs: []string{"1:0", "1:1"},
	}}, []float64{0.35})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	result := report.Thresholds[0]
	if result.LabeledChunkCases != 1 || result.PassageRecall != 1 || result.ChunkMRR != 1 {
		t.Fatalf("passage recall metrics = %#v", result)
	}
	if result.PrecisionAt3 != 2.0/3.0 || result.PrecisionAt10 != 0.2 {
		t.Fatalf("precision metrics = %#v", result)
	}
	if result.NDCG3 <= 0.7 || result.NDCG10 <= 0.7 || result.MAP <= 0.8 {
		t.Fatalf("ranking metrics = %#v", result)
	}
	caseResult := result.Cases[0]
	if caseResult.PassageRecall != 1 || caseResult.ChunkMRR != 1 || caseResult.MAP <= 0.8 {
		t.Fatalf("case passage metrics = %#v", caseResult)
	}
}

func TestLoadCasesRejectsUnknownFields(t *testing.T) {
	_, err := retrievaleval.LoadCases(strings.NewReader(`[{"id":"q1","knowledge_base_id":1,"question":"问题","expected_relevant":true,"extra":1}]`))
	if err == nil {
		t.Fatal("LoadCases() error = nil, want invalid case file")
	}
}

func TestLoadCasesRejectsDuplicateExpectedDocumentIDs(t *testing.T) {
	_, err := retrievaleval.LoadCases(strings.NewReader(`[{
		"id":"q1",
		"knowledge_base_id":1,
		"question":"问题",
		"expected_relevant":true,
		"expected_document_ids":[2,2]
	}]`))
	if err == nil {
		t.Fatal("LoadCases() error = nil, want invalid document labels")
	}
}

func TestLoadCasesRejectsDuplicateExpectedChunkIDs(t *testing.T) {
	_, err := retrievaleval.LoadCases(strings.NewReader(`[{"id":"q1","knowledge_base_id":1,"question":"问题","expected_relevant":true,"expected_chunk_ids":["1:0","1:0"]}]`))
	if err == nil {
		t.Fatal("LoadCases() error = nil, want invalid chunk labels")
	}
}

func TestScoreGenerationMatchesMetricShape(t *testing.T) {
	perfect := retrievaleval.ScoreGeneration("PostgreSQL supports vector search.", "PostgreSQL supports vector search.")
	if perfect.BLEU1 != 1 || perfect.BLEU2 != 1 || perfect.BLEU4 != 1 ||
		perfect.ROUGE1 != 1 || perfect.ROUGE2 != 1 || perfect.ROUGEL != 1 {
		t.Fatalf("perfect generation metrics = %#v", perfect)
	}
	empty := retrievaleval.ScoreGeneration("", "参考答案")
	if empty.BLEU1 != 0 || empty.ROUGE1 != 0 {
		t.Fatalf("empty generation metrics = %#v", empty)
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
