package retrievaleval

import (
	"fmt"
	"math"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type chunkMetricResult struct {
	PrecisionAt3  float64
	PrecisionAt10 float64
	Recall        float64
	NDCG3         float64
	NDCG10        float64
	MRR           float64
	MAP           float64
}

func validateChunkIDs(chunkIDs []string) error {
	seen := make(map[string]struct{}, len(chunkIDs))
	for _, chunkID := range chunkIDs {
		if chunkID == "" {
			return ErrInvalidCase
		}
		if _, ok := seen[chunkID]; ok {
			return ErrInvalidCase
		}
		seen[chunkID] = struct{}{}
	}
	return nil
}

func resultsWithinThreshold(results []retrieval.Result, threshold float64) []retrieval.Result {
	eligible := make([]retrieval.Result, 0, len(results))
	for _, result := range results {
		// Keyword-only results do not have a meaningful vector distance. They
		// have already passed the lexical threshold in the retrieval service.
		if result.MatchType == "keyword" || result.Distance <= threshold {
			eligible = append(eligible, result)
		}
	}
	return eligible
}

func chunkMetrics(results []retrieval.Result, expected []string) chunkMetricResult {
	expectedSet := make(map[string]struct{}, len(expected))
	for _, chunkID := range expected {
		expectedSet[chunkID] = struct{}{}
	}
	keys := uniqueResultKeys(results)
	return chunkMetricResult{
		PrecisionAt3:  precisionAt(keys, expectedSet, 3),
		PrecisionAt10: precisionAt(keys, expectedSet, 10),
		Recall:        recallAt(keys, expectedSet),
		NDCG3:         ndcgAt(keys, expectedSet, 3),
		NDCG10:        ndcgAt(keys, expectedSet, 10),
		MRR:           reciprocalRank(keys, expectedSet),
		MAP:           averagePrecision(keys, expectedSet),
	}
}

func uniqueResultKeys(results []retrieval.Result) []string {
	keys := make([]string, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		key := resultKey(result)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func resultKey(result retrieval.Result) string {
	if result.ChunkKind == "summary" {
		return fmt.Sprintf("%d:%d:summary", result.DocumentID, result.Position)
	}
	return fmt.Sprintf("%d:%d", result.DocumentID, result.Position)
}

func precisionAt(results []string, expected map[string]struct{}, k int) float64 {
	if k <= 0 {
		return 0
	}
	limit := minMetricInt(k, len(results))
	if limit == 0 {
		return 0
	}
	hits := 0
	for _, result := range results[:limit] {
		if _, ok := expected[result]; ok {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

func recallAt(results []string, expected map[string]struct{}) float64 {
	if len(expected) == 0 {
		return 0
	}
	hits := 0
	seen := make(map[string]struct{}, len(expected))
	for _, result := range results {
		if _, ok := expected[result]; !ok {
			continue
		}
		if _, counted := seen[result]; counted {
			continue
		}
		seen[result] = struct{}{}
		hits++
	}
	return float64(hits) / float64(len(expected))
}

func reciprocalRank(results []string, expected map[string]struct{}) float64 {
	for index, result := range results {
		if _, ok := expected[result]; ok {
			return 1 / float64(index+1)
		}
	}
	return 0
}

func averagePrecision(results []string, expected map[string]struct{}) float64 {
	if len(expected) == 0 {
		return 0
	}
	hits := 0
	sum := 0.0
	seen := make(map[string]struct{}, len(expected))
	for index, result := range results {
		if _, ok := expected[result]; !ok {
			continue
		}
		if _, counted := seen[result]; counted {
			continue
		}
		seen[result] = struct{}{}
		hits++
		sum += float64(hits) / float64(index+1)
	}
	return sum / float64(len(expected))
}

func ndcgAt(results []string, expected map[string]struct{}, k int) float64 {
	if k <= 0 || len(expected) == 0 {
		return 0
	}
	limit := minMetricInt(k, len(results))
	dcg := 0.0
	for index, result := range results[:limit] {
		if _, ok := expected[result]; ok {
			dcg += 1 / math.Log2(float64(index+2))
		}
	}
	idealHits := minMetricInt(k, len(expected))
	idcg := 0.0
	for index := 0; index < idealHits; index++ {
		idcg += 1 / math.Log2(float64(index+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func hasRelevantDocument(results []retrieval.Result, expected []int64) bool {
	for _, result := range results {
		for _, documentID := range expected {
			if result.DocumentID == documentID {
				return true
			}
		}
	}
	return false
}

func reciprocalRankForDocuments(results []retrieval.Result, expected []int64) float64 {
	for index, result := range results {
		for _, documentID := range expected {
			if result.DocumentID == documentID {
				return 1 / float64(index+1)
			}
		}
	}
	return 0
}

func minMetricInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
