package retrieval

// Explanation is a bounded explanation of why one final retrieval result was
// returned. It contains ranking evidence only; document content is already
// present in the result and is intentionally not duplicated here.
type Explanation struct {
	Rank         int     `json:"rank"`
	DocumentID   int64   `json:"documentId"`
	Position     int     `json:"position"`
	MatchType    string  `json:"matchType,omitempty"`
	Distance     float64 `json:"distance"`
	KeywordScore float64 `json:"keywordScore,omitempty"`
	HeadingScore float64 `json:"headingScore,omitempty"`
	FusionScore  float64 `json:"fusionScore,omitempty"`
	Reason       string  `json:"reason"`
}

// ExplainResults converts only the bounded final result set into stable
// evidence. Candidate contents and pre-filtered rows are never exposed.
func ExplainResults(results []Result) []Explanation {
	if len(results) == 0 {
		return nil
	}
	explanations := make([]Explanation, 0, len(results))
	for index, result := range results {
		explanations = append(explanations, Explanation{
			Rank:         index + 1,
			DocumentID:   result.DocumentID,
			Position:     result.Position,
			MatchType:    result.MatchType,
			Distance:     result.Distance,
			KeywordScore: result.KeywordScore,
			HeadingScore: result.HeadingScore,
			FusionScore:  result.FusionScore,
			Reason:       explainReason(result),
		})
	}
	return explanations
}

func explainReason(result Result) string {
	hasKeyword := result.KeywordScoreKnown || result.KeywordScore > 0
	hasHeading := result.HeadingScore > 0
	switch {
	case result.MatchType == "hybrid" && hasHeading:
		return "向量+关键词+标题命中"
	case result.MatchType == "hybrid" || hasKeyword && result.MatchType != "keyword":
		return "向量+关键词命中"
	case result.MatchType == "keyword" && hasHeading:
		return "关键词+标题命中"
	case result.MatchType == "keyword":
		return "关键词命中"
	case hasHeading:
		return "向量+标题命中"
	default:
		return "向量命中"
	}
}
