package retrievaleval

import (
	"strings"
	"unicode"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

// ScoreRAGQuality adds deterministic RAG-quality baselines to the lexical
// generation metrics. These scores are intentionally not presented as an LLM
// judge: they are reproducible, free, and useful for regression tests.
func ScoreRAGQuality(answer, question, reference string, sources []retrieval.Result, expectedChunkIDs []string) GenerationMetrics {
	metrics := GenerationMetrics{}
	metrics.Faithfulness = faithfulnessScore(answer, sources)
	metrics.AnswerRelevance = answerRelevanceScore(answer, question, reference)
	if len(expectedChunkIDs) > 0 {
		expected := expectedChunkSet(expectedChunkIDs)
		actual := uniqueResultKeys(sources)
		metrics.CitationRecall = recallAt(actual, expected)
		metrics.CitationPrecision = precisionAll(actual, expected)
	}
	return metrics
}

func faithfulnessScore(answer string, sources []retrieval.Result) float64 {
	answerSentences := splitMetricSentences(answer)
	if len(answerSentences) == 0 || len(sources) == 0 {
		return 0
	}
	sourceTokens := make(map[string]struct{})
	for _, source := range sources {
		for _, token := range metricTokens(source.Content) {
			if usefulMetricToken(token) {
				sourceTokens[token] = struct{}{}
			}
		}
	}
	if len(sourceTokens) == 0 {
		return 0
	}
	total := 0.0
	for _, sentence := range answerSentences {
		tokens := uniqueUsefulTokens(metricTokens(sentence))
		if len(tokens) == 0 {
			continue
		}
		hits := 0
		for token := range tokens {
			if _, ok := sourceTokens[token]; ok {
				hits++
			}
		}
		total += float64(hits) / float64(len(tokens))
	}
	return total / float64(len(answerSentences))
}

func answerRelevanceScore(answer, question, reference string) float64 {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return 0
	}
	if strings.TrimSpace(reference) != "" {
		return rougeLScore(metricTokens(answer), metricTokens(reference))
	}
	questionTokens := uniqueUsefulTokens(metricTokens(question))
	answerTokens := uniqueUsefulTokens(metricTokens(answer))
	if len(questionTokens) == 0 || len(answerTokens) == 0 {
		return 0
	}
	hits := 0
	for token := range questionTokens {
		if _, ok := answerTokens[token]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(questionTokens))
}

func splitMetricSentences(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '。' || r == '！' || r == '？' || r == '!' || r == '?' || r == '\n'
	})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, part)
		}
	}
	return result
}

func uniqueUsefulTokens(tokens []string) map[string]struct{} {
	result := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if usefulMetricToken(token) {
			result[token] = struct{}{}
		}
	}
	return result
}

func usefulMetricToken(token string) bool {
	for _, character := range token {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || character == '_' {
			return true
		}
	}
	return false
}

func isRefusalAnswer(answer string) bool {
	value := strings.ToLower(strings.TrimSpace(answer))
	if value == "" {
		return true
	}
	markers := []string{
		"知识库中没有", "没有找到足够", "没有足够资料", "暂时无法回答", "无法回答",
		"i don't know", "not enough information", "cannot answer", "no relevant information",
	}
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
