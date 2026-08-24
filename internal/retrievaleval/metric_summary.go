package retrievaleval

// MetricSample is one persisted evaluation case projected into the aggregate
// metric calculator. The include flags keep retrieval, reference-based
// generation, and answer-quality denominators independent.
type MetricSample struct {
	RetrievalMetrics  RetrievalMetrics
	GenerationMetrics GenerationMetrics
	IncludeRetrieval  bool
	IncludeGeneration bool
	IncludeQuality    bool
}

type MetricSummary struct {
	RetrievalMetrics    RetrievalMetrics  `json:"retrieval_metrics"`
	GenerationMetrics   GenerationMetrics `json:"generation_metrics"`
	RetrievalCaseCount  int               `json:"retrieval_case_count"`
	GenerationCaseCount int               `json:"generation_case_count"`
	QualityCaseCount    int               `json:"quality_case_count"`
}

// AggregateMetricSamples calculates macro averages. Quality metrics use all
// non-refused answers, while BLEU/ROUGE use only cases with a reference
// answer, matching EvaluateRAG's single-process report semantics.
func AggregateMetricSamples(samples []MetricSample) MetricSummary {
	var summary MetricSummary
	var retrievalSum RetrievalMetrics
	var generationSum GenerationMetrics
	var qualitySum GenerationMetrics
	for _, sample := range samples {
		if sample.IncludeRetrieval {
			retrievalSum = addRetrievalMetrics(retrievalSum, sample.RetrievalMetrics)
			summary.RetrievalCaseCount++
		}
		if sample.IncludeGeneration {
			generationSum = addGenerationMetrics(generationSum, sample.GenerationMetrics)
			summary.GenerationCaseCount++
		}
		if sample.IncludeQuality {
			qualitySum = addGenerationMetrics(qualitySum, sample.GenerationMetrics)
			summary.QualityCaseCount++
		}
	}
	if summary.RetrievalCaseCount > 0 {
		summary.RetrievalMetrics = divideRetrievalMetrics(retrievalSum, float64(summary.RetrievalCaseCount))
	}
	if summary.GenerationCaseCount > 0 {
		summary.GenerationMetrics = divideGenerationMetrics(generationSum, float64(summary.GenerationCaseCount))
	}
	if summary.QualityCaseCount > 0 {
		qualityMetrics := divideGenerationMetrics(qualitySum, float64(summary.QualityCaseCount))
		summary.GenerationMetrics.Faithfulness = qualityMetrics.Faithfulness
		summary.GenerationMetrics.AnswerRelevance = qualityMetrics.AnswerRelevance
		summary.GenerationMetrics.CitationRecall = qualityMetrics.CitationRecall
		summary.GenerationMetrics.CitationPrecision = qualityMetrics.CitationPrecision
	}
	return summary
}

func addRetrievalMetrics(left, right RetrievalMetrics) RetrievalMetrics {
	return RetrievalMetrics{Precision: left.Precision + right.Precision, Recall: left.Recall + right.Recall, NDCG3: left.NDCG3 + right.NDCG3, NDCG10: left.NDCG10 + right.NDCG10, MRR: left.MRR + right.MRR, MAP: left.MAP + right.MAP}
}

func divideRetrievalMetrics(value RetrievalMetrics, divisor float64) RetrievalMetrics {
	return RetrievalMetrics{Precision: value.Precision / divisor, Recall: value.Recall / divisor, NDCG3: value.NDCG3 / divisor, NDCG10: value.NDCG10 / divisor, MRR: value.MRR / divisor, MAP: value.MAP / divisor}
}

func addGenerationMetrics(left, right GenerationMetrics) GenerationMetrics {
	return GenerationMetrics{BLEU1: left.BLEU1 + right.BLEU1, BLEU2: left.BLEU2 + right.BLEU2, BLEU4: left.BLEU4 + right.BLEU4, ROUGE1: left.ROUGE1 + right.ROUGE1, ROUGE2: left.ROUGE2 + right.ROUGE2, ROUGEL: left.ROUGEL + right.ROUGEL, Faithfulness: left.Faithfulness + right.Faithfulness, AnswerRelevance: left.AnswerRelevance + right.AnswerRelevance, CitationRecall: left.CitationRecall + right.CitationRecall, CitationPrecision: left.CitationPrecision + right.CitationPrecision}
}

func divideGenerationMetrics(value GenerationMetrics, divisor float64) GenerationMetrics {
	return GenerationMetrics{BLEU1: value.BLEU1 / divisor, BLEU2: value.BLEU2 / divisor, BLEU4: value.BLEU4 / divisor, ROUGE1: value.ROUGE1 / divisor, ROUGE2: value.ROUGE2 / divisor, ROUGEL: value.ROUGEL / divisor, Faithfulness: value.Faithfulness / divisor, AnswerRelevance: value.AnswerRelevance / divisor, CitationRecall: value.CitationRecall / divisor, CitationPrecision: value.CitationPrecision / divisor}
}
