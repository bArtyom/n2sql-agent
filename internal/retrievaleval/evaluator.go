// Package retrievaleval evaluates retrieval thresholds against a small labeled
// question set. It does not call a chat model or judge answer wording.
package retrievaleval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

var (
	ErrInvalidEvaluator = errors.New("invalid retrieval evaluator")
	ErrEmptyCases       = errors.New("retrieval evaluation cases are required")
	ErrInvalidCase      = errors.New("invalid retrieval evaluation case")
	ErrInvalidCaseFile  = errors.New("invalid retrieval evaluation case file")
	ErrInvalidThreshold = errors.New("retrieval threshold must be between 0 and 2")
)

var DefaultThresholds = []float64{0.55, 0.60, 0.65, 0.70, 0.75}

// Case labels whether at least one useful chunk should exist for a question.
type Case struct {
	ID                  string   `json:"id"`
	KnowledgeBaseID     int64    `json:"knowledge_base_id"`
	Question            string   `json:"question"`
	ExpectedRelevant    bool     `json:"expected_relevant"`
	ExpectedDocumentIDs []int64  `json:"expected_document_ids,omitempty"`
	ExpectedChunkIDs    []string `json:"expected_chunk_ids,omitempty"`
	ReferenceAnswer     string   `json:"reference_answer,omitempty"`
	Notes               string   `json:"notes,omitempty"`
}

type CaseResult struct {
	ID                        string             `json:"id"`
	ExpectedRelevant          bool               `json:"expected_relevant"`
	Retrieved                 int                `json:"retrieved"`
	MinimumDistance           *float64           `json:"minimum_distance,omitempty"`
	RelevantRetrieved         bool               `json:"relevant_retrieved,omitempty"`
	FirstRelevantRank         int                `json:"first_relevant_rank,omitempty"`
	FirstRelevantMatchType    string             `json:"first_relevant_match_type,omitempty"`
	FirstRelevantHeadingScore float64            `json:"first_relevant_heading_score,omitempty"`
	HeadingPathHits           int                `json:"heading_path_hits,omitempty"`
	SummaryHits               int                `json:"summary_hits,omitempty"`
	PassageRecall             float64            `json:"passage_recall,omitempty"`
	PrecisionAt3              float64            `json:"precision_at_3,omitempty"`
	PrecisionAt10             float64            `json:"precision_at_10,omitempty"`
	NDCG3                     float64            `json:"ndcg3,omitempty"`
	NDCG10                    float64            `json:"ndcg10,omitempty"`
	ChunkMRR                  float64            `json:"chunk_mrr,omitempty"`
	MAP                       float64            `json:"map,omitempty"`
	results                   []retrieval.Result `json:"-"`
}

type ThresholdResult struct {
	Threshold            float64      `json:"threshold"`
	Total                int          `json:"total"`
	ExpectedRelevant     int          `json:"expected_relevant"`
	ExpectedIrrelevant   int          `json:"expected_irrelevant"`
	RelevantHits         int          `json:"relevant_hits"`
	FalseRefusals        int          `json:"false_refusals"`
	CorrectRefusals      int          `json:"correct_refusals"`
	UnsupportedAccepts   int          `json:"unsupported_accepts"`
	Recall               float64      `json:"recall"`
	RefusalRate          float64      `json:"refusal_rate"`
	Accuracy             float64      `json:"accuracy"`
	LabeledDocumentCases int          `json:"labeled_document_cases,omitempty"`
	DocumentHits         int          `json:"document_hits,omitempty"`
	DocumentRecall       float64      `json:"document_recall,omitempty"`
	MRR                  float64      `json:"mrr,omitempty"`
	HeadingPathHits      int          `json:"heading_path_hits,omitempty"`
	SummaryHits          int          `json:"summary_hits,omitempty"`
	LabeledChunkCases    int          `json:"labeled_chunk_cases,omitempty"`
	PassageRecall        float64      `json:"passage_recall,omitempty"`
	PrecisionAt3         float64      `json:"precision_at_3,omitempty"`
	PrecisionAt10        float64      `json:"precision_at_10,omitempty"`
	NDCG3                float64      `json:"ndcg3,omitempty"`
	NDCG10               float64      `json:"ndcg10,omitempty"`
	ChunkMRR             float64      `json:"chunk_mrr,omitempty"`
	MAP                  float64      `json:"map,omitempty"`
	Cases                []CaseResult `json:"cases"`
}

type Report struct {
	CaseCount  int               `json:"case_count"`
	Thresholds []ThresholdResult `json:"thresholds"`
}

func LoadCases(reader io.Reader) ([]Case, error) {
	if reader == nil {
		return nil, ErrInvalidCaseFile
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var cases []Case
	if err := decoder.Decode(&cases); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrEmptyCases
		}
		return nil, ErrInvalidCaseFile
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalidCaseFile
	}
	if len(cases) == 0 {
		return nil, ErrEmptyCases
	}
	for _, evaluationCase := range cases {
		if evaluationCase.ID == "" || strings.TrimSpace(evaluationCase.ID) != evaluationCase.ID ||
			evaluationCase.KnowledgeBaseID <= 0 || strings.TrimSpace(evaluationCase.Question) == "" {
			return nil, ErrInvalidCase
		}
		if err := validateDocumentIDs(evaluationCase.ExpectedDocumentIDs); err != nil {
			return nil, err
		}
		if err := validateChunkIDs(evaluationCase.ExpectedChunkIDs); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

func LimitCases(cases []Case, maxCases int) ([]Case, error) {
	if maxCases <= 0 {
		return nil, ErrInvalidCase
	}
	if len(cases) <= maxCases {
		return append([]Case(nil), cases...), nil
	}
	return append([]Case(nil), cases[:maxCases]...), nil
}

func ValidateThresholds(thresholds []float64) error {
	if len(thresholds) == 0 {
		return ErrInvalidThreshold
	}
	for _, threshold := range thresholds {
		if threshold < 0 || threshold > 2 {
			return ErrInvalidThreshold
		}
	}
	return nil
}

func Evaluate(ctx context.Context, searcher retrieval.Searcher, cases []Case, thresholds []float64) (Report, error) {
	if ctx == nil || searcher == nil {
		return Report{}, ErrInvalidEvaluator
	}
	if len(cases) == 0 {
		return Report{}, ErrEmptyCases
	}
	if err := ValidateThresholds(thresholds); err != nil {
		return Report{}, err
	}
	for _, evaluationCase := range cases {
		if evaluationCase.ID == "" || evaluationCase.KnowledgeBaseID <= 0 || strings.TrimSpace(evaluationCase.Question) == "" {
			return Report{}, ErrInvalidCase
		}
		if err := validateDocumentIDs(evaluationCase.ExpectedDocumentIDs); err != nil {
			return Report{}, err
		}
		if err := validateChunkIDs(evaluationCase.ExpectedChunkIDs); err != nil {
			return Report{}, err
		}
	}

	caseResults := make([]CaseResult, 0, len(cases))
	for _, evaluationCase := range cases {
		results, err := searcher.Search(ctx, evaluationCase.KnowledgeBaseID, evaluationCase.Question, retrieval.MaxResults)
		if err != nil {
			return Report{}, err
		}
		result := CaseResult{ID: evaluationCase.ID, ExpectedRelevant: evaluationCase.ExpectedRelevant, Retrieved: len(results)}
		for _, item := range results {
			if item.HeadingScore > 0 {
				result.HeadingPathHits++
			}
			if item.ChunkKind == "summary" {
				result.SummaryHits++
			}
		}
		if len(evaluationCase.ExpectedDocumentIDs) > 0 {
			expected := make(map[int64]struct{}, len(evaluationCase.ExpectedDocumentIDs))
			for _, documentID := range evaluationCase.ExpectedDocumentIDs {
				expected[documentID] = struct{}{}
			}
			for rank, item := range results {
				if _, ok := expected[item.DocumentID]; !ok {
					continue
				}
				result.RelevantRetrieved = true
				result.FirstRelevantRank = rank + 1
				result.FirstRelevantMatchType = item.MatchType
				result.FirstRelevantHeadingScore = item.HeadingScore
				break
			}
		}
		for _, item := range results {
			if result.MinimumDistance == nil || item.Distance < *result.MinimumDistance {
				distance := item.Distance
				result.MinimumDistance = &distance
			}
		}
		result.results = results
		caseResults = append(caseResults, result)
	}

	report := Report{CaseCount: len(cases), Thresholds: make([]ThresholdResult, 0, len(thresholds))}
	for _, threshold := range thresholds {
		thresholdReport := ThresholdResult{Threshold: threshold, Total: len(cases), Cases: append([]CaseResult(nil), caseResults...)}
		for index, evaluationCase := range cases {
			if evaluationCase.ExpectedRelevant {
				thresholdReport.ExpectedRelevant++
			} else {
				thresholdReport.ExpectedIrrelevant++
			}
			eligible := resultsWithinThreshold(caseResults[index].results, threshold)
			hit := len(eligible) > 0
			if evaluationCase.ExpectedRelevant {
				if hit {
					thresholdReport.RelevantHits++
				} else {
					thresholdReport.FalseRefusals++
				}
			} else if hit {
				thresholdReport.UnsupportedAccepts++
			} else {
				thresholdReport.CorrectRefusals++
			}
			caseResult := caseResults[index]
			thresholdReport.HeadingPathHits += caseResult.HeadingPathHits
			thresholdReport.SummaryHits += caseResult.SummaryHits
			if len(evaluationCase.ExpectedChunkIDs) > 0 {
				metrics := chunkMetrics(eligible, evaluationCase.ExpectedChunkIDs)
				caseResult.PassageRecall = metrics.Recall
				caseResult.PrecisionAt3 = metrics.PrecisionAt3
				caseResult.PrecisionAt10 = metrics.PrecisionAt10
				caseResult.NDCG3 = metrics.NDCG3
				caseResult.NDCG10 = metrics.NDCG10
				caseResult.ChunkMRR = metrics.MRR
				caseResult.MAP = metrics.MAP
			}
			thresholdReport.Cases[index] = caseResult
			if len(evaluationCase.ExpectedDocumentIDs) > 0 {
				thresholdReport.LabeledDocumentCases++
				if hasRelevantDocument(eligible, evaluationCase.ExpectedDocumentIDs) {
					thresholdReport.DocumentHits++
					thresholdReport.MRR += reciprocalRankForDocuments(eligible, evaluationCase.ExpectedDocumentIDs)
				}
			}
			if len(evaluationCase.ExpectedChunkIDs) > 0 {
				thresholdReport.LabeledChunkCases++
				metrics := chunkMetrics(eligible, evaluationCase.ExpectedChunkIDs)
				thresholdReport.PassageRecall += metrics.Recall
				thresholdReport.PrecisionAt3 += metrics.PrecisionAt3
				thresholdReport.PrecisionAt10 += metrics.PrecisionAt10
				thresholdReport.NDCG3 += metrics.NDCG3
				thresholdReport.NDCG10 += metrics.NDCG10
				thresholdReport.ChunkMRR += metrics.MRR
				thresholdReport.MAP += metrics.MAP
			}
		}
		if thresholdReport.ExpectedRelevant > 0 {
			thresholdReport.Recall = float64(thresholdReport.RelevantHits) / float64(thresholdReport.ExpectedRelevant)
		}
		if thresholdReport.ExpectedIrrelevant > 0 {
			thresholdReport.RefusalRate = float64(thresholdReport.CorrectRefusals) / float64(thresholdReport.ExpectedIrrelevant)
		}
		if thresholdReport.Total > 0 {
			thresholdReport.Accuracy = float64(thresholdReport.RelevantHits+thresholdReport.CorrectRefusals) / float64(thresholdReport.Total)
		}
		if thresholdReport.LabeledDocumentCases > 0 {
			thresholdReport.DocumentRecall = float64(thresholdReport.DocumentHits) / float64(thresholdReport.LabeledDocumentCases)
			thresholdReport.MRR /= float64(thresholdReport.LabeledDocumentCases)
		}
		if thresholdReport.LabeledChunkCases > 0 {
			count := float64(thresholdReport.LabeledChunkCases)
			thresholdReport.PassageRecall /= count
			thresholdReport.PrecisionAt3 /= count
			thresholdReport.PrecisionAt10 /= count
			thresholdReport.NDCG3 /= count
			thresholdReport.NDCG10 /= count
			thresholdReport.ChunkMRR /= count
			thresholdReport.MAP /= count
		}
		report.Thresholds = append(report.Thresholds, thresholdReport)
	}
	return report, nil
}

func validateDocumentIDs(documentIDs []int64) error {
	if len(documentIDs) == 0 {
		return nil
	}
	ids := append([]int64(nil), documentIDs...)
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	for index, documentID := range ids {
		if documentID <= 0 || index > 0 && ids[index-1] == documentID {
			return ErrInvalidCase
		}
	}
	return nil
}
