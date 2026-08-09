// Package retrievaleval evaluates retrieval thresholds against a small labeled
// question set. It does not call a chat model or judge answer wording.
package retrievaleval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	ID               string `json:"id"`
	KnowledgeBaseID  int64  `json:"knowledge_base_id"`
	Question         string `json:"question"`
	ExpectedRelevant bool   `json:"expected_relevant"`
	Notes            string `json:"notes,omitempty"`
}

type CaseResult struct {
	ID               string   `json:"id"`
	ExpectedRelevant bool     `json:"expected_relevant"`
	Retrieved        int      `json:"retrieved"`
	MinimumDistance  *float64 `json:"minimum_distance,omitempty"`
}

type ThresholdResult struct {
	Threshold          float64      `json:"threshold"`
	Total              int          `json:"total"`
	ExpectedRelevant   int          `json:"expected_relevant"`
	ExpectedIrrelevant int          `json:"expected_irrelevant"`
	RelevantHits       int          `json:"relevant_hits"`
	FalseRefusals      int          `json:"false_refusals"`
	CorrectRefusals    int          `json:"correct_refusals"`
	UnsupportedAccepts int          `json:"unsupported_accepts"`
	Recall             float64      `json:"recall"`
	RefusalRate        float64      `json:"refusal_rate"`
	Accuracy           float64      `json:"accuracy"`
	Cases              []CaseResult `json:"cases"`
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
	}

	caseResults := make([]CaseResult, 0, len(cases))
	for _, evaluationCase := range cases {
		results, err := searcher.Search(ctx, evaluationCase.KnowledgeBaseID, evaluationCase.Question, retrieval.MaxResults)
		if err != nil {
			return Report{}, err
		}
		result := CaseResult{ID: evaluationCase.ID, ExpectedRelevant: evaluationCase.ExpectedRelevant, Retrieved: len(results)}
		for _, item := range results {
			if result.MinimumDistance == nil || item.Distance < *result.MinimumDistance {
				distance := item.Distance
				result.MinimumDistance = &distance
			}
		}
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
			hit := caseResults[index].MinimumDistance != nil && *caseResults[index].MinimumDistance <= threshold
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
		report.Thresholds = append(report.Thresholds, thresholdReport)
	}
	return report, nil
}
