package retrievaleval

import (
	"context"
	"errors"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/rag"
)

var ErrInvalidRAGEvaluator = errors.New("invalid RAG evaluator")

// RetrievalMetrics uses the same field names as WeKnora's evaluation result.
type RetrievalMetrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	NDCG3     float64 `json:"ndcg3"`
	NDCG10    float64 `json:"ndcg10"`
	MRR       float64 `json:"mrr"`
	MAP       float64 `json:"map"`
}

type MetricResult struct {
	RetrievalMetrics  RetrievalMetrics  `json:"retrieval_metrics"`
	GenerationMetrics GenerationMetrics `json:"generation_metrics"`
}

type RAGCaseResult struct {
	ID             string       `json:"id"`
	Answer         string       `json:"answer,omitempty"`
	RetrievedIDs   []string     `json:"retrieved_ids,omitempty"`
	Sources        int          `json:"sources"`
	Refused        bool         `json:"refused"`
	CorrectRefusal bool         `json:"correct_refusal"`
	Metrics        MetricResult `json:"metrics"`
	Error          string       `json:"error,omitempty"`
}

type RAGReport struct {
	Total              int             `json:"total"`
	ExpectedRelevant   int             `json:"expected_relevant"`
	ExpectedIrrelevant int             `json:"expected_irrelevant"`
	CorrectRefusals    int             `json:"correct_refusals"`
	FalseRefusals      int             `json:"false_refusals"`
	UnsupportedAccepts int             `json:"unsupported_accepts"`
	Recall             float64         `json:"recall"`
	RefusalRate        float64         `json:"refusal_rate"`
	Accuracy           float64         `json:"accuracy"`
	Metric             MetricResult    `json:"metric"`
	Cases              []RAGCaseResult `json:"cases"`
}

// EvaluateRAG runs the complete retrieval + generation path for each case.
// It intentionally does not use the Agent runtime: this isolates RAG quality
// from tool retries, child agents and runtime scheduling.
func EvaluateRAG(ctx context.Context, answerer rag.Answerer, cases []Case, topK int) (RAGReport, error) {
	if ctx == nil || answerer == nil || topK <= 0 || len(cases) == 0 {
		return RAGReport{}, ErrInvalidRAGEvaluator
	}
	for _, evaluationCase := range cases {
		if err := validateRAGCase(evaluationCase); err != nil {
			return RAGReport{}, err
		}
	}

	report := RAGReport{Total: len(cases), Cases: make([]RAGCaseResult, 0, len(cases))}
	var retrievalSum RetrievalMetrics
	var generationSum GenerationMetrics
	var qualitySum GenerationMetrics
	var retrievalCount, generationCount, qualityCount int
	for _, evaluationCase := range cases {
		caseResult, err := EvaluateRAGCase(ctx, answerer, evaluationCase, topK)
		if err != nil {
			return RAGReport{}, err
		}
		if len(evaluationCase.ExpectedChunkIDs) > 0 {
			retrievalSum = addRetrievalMetrics(retrievalSum, caseResult.Metrics.RetrievalMetrics)
			retrievalCount++
		}
		if strings.TrimSpace(evaluationCase.ReferenceAnswer) != "" {
			generationSum = addGenerationMetrics(generationSum, caseResult.Metrics.GenerationMetrics)
			generationCount++
		}
		if !caseResult.Refused {
			qualitySum = addGenerationMetrics(qualitySum, caseResult.Metrics.GenerationMetrics)
			qualityCount++
		}
		report.Cases = append(report.Cases, caseResult)
		if evaluationCase.ExpectedRelevant {
			report.ExpectedRelevant++
			if caseResult.Refused {
				report.FalseRefusals++
			}
		} else {
			report.ExpectedIrrelevant++
			if caseResult.Refused {
				report.CorrectRefusals++
			} else {
				report.UnsupportedAccepts++
			}
		}
	}
	if retrievalCount > 0 {
		report.Metric.RetrievalMetrics = divideRetrievalMetrics(retrievalSum, float64(retrievalCount))
	}
	if generationCount > 0 {
		report.Metric.GenerationMetrics = divideGenerationMetrics(generationSum, float64(generationCount))
	}
	if qualityCount > 0 {
		qualityMetrics := divideGenerationMetrics(qualitySum, float64(qualityCount))
		report.Metric.GenerationMetrics.Faithfulness = qualityMetrics.Faithfulness
		report.Metric.GenerationMetrics.AnswerRelevance = qualityMetrics.AnswerRelevance
		report.Metric.GenerationMetrics.CitationRecall = qualityMetrics.CitationRecall
		report.Metric.GenerationMetrics.CitationPrecision = qualityMetrics.CitationPrecision
	}
	if report.ExpectedRelevant > 0 {
		report.Recall = float64(report.ExpectedRelevant-report.FalseRefusals) / float64(report.ExpectedRelevant)
	}
	if report.ExpectedIrrelevant > 0 {
		report.RefusalRate = float64(report.CorrectRefusals) / float64(report.ExpectedIrrelevant)
	}
	if report.Total > 0 {
		report.Accuracy = float64(report.ExpectedRelevant-report.FalseRefusals+report.CorrectRefusals) / float64(report.Total)
	}
	return report, nil
}

// EvaluateRAGCase evaluates one question. Keeping this operation separate
// lets a durable worker persist each case before moving to the next one.
func EvaluateRAGCase(ctx context.Context, answerer rag.Answerer, evaluationCase Case, topK int) (RAGCaseResult, error) {
	if ctx == nil || answerer == nil || topK <= 0 {
		return RAGCaseResult{}, ErrInvalidRAGEvaluator
	}
	if err := validateRAGCase(evaluationCase); err != nil {
		return RAGCaseResult{}, err
	}
	response, err := answerer.Answer(ctx, evaluationCase.KnowledgeBaseID, evaluationCase.Question, topK)
	if err != nil {
		if errors.Is(err, rag.ErrNoSources) {
			return RAGCaseResult{ID: evaluationCase.ID, Refused: true, CorrectRefusal: !evaluationCase.ExpectedRelevant}, nil
		}
		return RAGCaseResult{}, err
	}
	caseResult := RAGCaseResult{ID: evaluationCase.ID, Answer: response.Answer, Sources: len(response.Sources), RetrievedIDs: uniqueResultKeys(response.Sources)}
	caseResult.Refused = isRefusalAnswer(response.Answer)
	caseResult.CorrectRefusal = caseResult.Refused && !evaluationCase.ExpectedRelevant
	if len(evaluationCase.ExpectedChunkIDs) > 0 {
		metrics := chunkMetrics(response.Sources, evaluationCase.ExpectedChunkIDs)
		precision := precisionAll(uniqueResultKeys(response.Sources), expectedChunkSet(evaluationCase.ExpectedChunkIDs))
		caseResult.Metrics.RetrievalMetrics = RetrievalMetrics{Precision: precision, Recall: metrics.Recall, NDCG3: metrics.NDCG3, NDCG10: metrics.NDCG10, MRR: metrics.MRR, MAP: metrics.MAP}
	}
	if strings.TrimSpace(evaluationCase.ReferenceAnswer) != "" {
		caseResult.Metrics.GenerationMetrics = ScoreGeneration(response.Answer, evaluationCase.ReferenceAnswer)
	}
	quality := ScoreRAGQuality(response.Answer, evaluationCase.Question, evaluationCase.ReferenceAnswer, response.Sources, evaluationCase.ExpectedChunkIDs)
	caseResult.Metrics.GenerationMetrics.Faithfulness = quality.Faithfulness
	caseResult.Metrics.GenerationMetrics.AnswerRelevance = quality.AnswerRelevance
	caseResult.Metrics.GenerationMetrics.CitationRecall = quality.CitationRecall
	caseResult.Metrics.GenerationMetrics.CitationPrecision = quality.CitationPrecision
	return caseResult, nil
}

func validateRAGCase(evaluationCase Case) error {
	if evaluationCase.ID == "" || strings.TrimSpace(evaluationCase.ID) != evaluationCase.ID ||
		evaluationCase.KnowledgeBaseID <= 0 || strings.TrimSpace(evaluationCase.Question) == "" {
		return ErrInvalidCase
	}
	if err := validateDocumentIDs(evaluationCase.ExpectedDocumentIDs); err != nil {
		return err
	}
	return validateChunkIDs(evaluationCase.ExpectedChunkIDs)
}
