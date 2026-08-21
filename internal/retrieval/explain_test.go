package retrieval_test

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

func TestExplainResultsDescribesHybridHeadingEvidence(t *testing.T) {
	result := retrieval.ExplainResults([]retrieval.Result{{
		DocumentID:        11,
		Position:          2,
		Distance:          0.12,
		MatchType:         "hybrid",
		KeywordScore:      0.7,
		KeywordScoreKnown: true,
		HeadingScore:      0.4,
		FusionScore:       0.8,
	}})

	if len(result) != 1 {
		t.Fatalf("explanation count = %d, want 1", len(result))
	}
	if result[0].Rank != 1 || result[0].Reason != "向量+关键词+标题命中" {
		t.Fatalf("explanation = %#v", result[0])
	}
}

func TestExplainResultsDescribesKeywordOnlyEvidence(t *testing.T) {
	result := retrieval.ExplainResults([]retrieval.Result{{
		MatchType:         "keyword",
		KeywordScore:      0.6,
		KeywordScoreKnown: true,
	}})

	if result[0].Reason != "关键词命中" {
		t.Fatalf("reason = %q, want keyword evidence", result[0].Reason)
	}
}
