package retrievaleval

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// GenerationMetrics follows WeKnora's generation metric names and ranges.
// The evaluator will populate it after the full RAG pipeline produces an
// answer for a case.
type GenerationMetrics struct {
	BLEU1             float64 `json:"bleu1"`
	BLEU2             float64 `json:"bleu2"`
	BLEU4             float64 `json:"bleu4"`
	ROUGE1            float64 `json:"rouge1"`
	ROUGE2            float64 `json:"rouge2"`
	ROUGEL            float64 `json:"rougel"`
	Faithfulness      float64 `json:"faithfulness,omitempty"`
	AnswerRelevance   float64 `json:"answer_relevance,omitempty"`
	CitationRecall    float64 `json:"citation_recall,omitempty"`
	CitationPrecision float64 `json:"citation_precision,omitempty"`
}

// ScoreGeneration calculates the same six metric families used by WeKnora:
// smoothed BLEU-1/2/4 and ROUGE-1/2/L F1.
func ScoreGeneration(candidate, reference string) GenerationMetrics {
	candidateTokens := metricTokens(candidate)
	referenceTokens := metricTokens(reference)
	return GenerationMetrics{
		BLEU1:  bleuScore(candidateTokens, referenceTokens, []float64{1}, true),
		BLEU2:  bleuScore(candidateTokens, referenceTokens, []float64{0.5, 0.5}, true),
		BLEU4:  bleuScore(candidateTokens, referenceTokens, []float64{0.25, 0.25, 0.25, 0.25}, true),
		ROUGE1: rougeNScore(candidateTokens, referenceTokens, 1),
		ROUGE2: rougeNScore(candidateTokens, referenceTokens, 2),
		ROUGEL: rougeLScore(candidateTokens, referenceTokens),
	}
}

var metricTokenPattern = regexp.MustCompile(`([\p{Han}]+)|([a-zA-Z0-9_.,!?]+)|(\p{P})`)

// metricTokens mirrors WeKnora's sentence/punctuation-aware tokenization.
// Chinese blocks use rune tokens as a pure-Go fallback because this project
// deliberately does not require the CGO Jieba runtime. The tokenizer remains
// isolated so Jieba-compatible segmentation can be injected later without
// changing metric formulas.
func metricTokens(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == '。' || r == '.' })
	tokens := make([]string, 0)
	for _, sentence := range parts {
		for _, match := range metricTokenPattern.FindAllStringSubmatch(sentence, -1) {
			switch {
			case match[1] != "":
				for _, r := range match[1] {
					tokens = append(tokens, string(r))
				}
			case match[2] != "":
				tokens = append(tokens, strings.ToLower(match[2]))
			case match[3] != "":
				tokens = append(tokens, match[3])
			}
		}
	}
	return tokens
}

func bleuScore(candidate, reference []string, weights []float64, smoothing bool) float64 {
	if len(candidate) == 0 || len(reference) == 0 {
		return 0
	}
	logSum := 0.0
	positiveOrders := 0
	for order, weight := range weights {
		if weight == 0 {
			continue
		}
		precision := modifiedPrecision(candidate, reference, order+1, smoothing)
		if precision <= 0 {
			return 0
		}
		logSum += weight * math.Log(precision)
		positiveOrders++
	}
	if positiveOrders == 0 {
		return 0
	}
	candidateLength := len(candidate)
	referenceLength := len(reference)
	brevityPenalty := 1.0
	if candidateLength <= referenceLength {
		brevityPenalty = math.Exp(1 - float64(referenceLength)/float64(candidateLength))
	}
	return brevityPenalty * math.Exp(logSum)
}

func modifiedPrecision(candidate, reference []string, order int, smoothing bool) float64 {
	candidateNGrams := nGramCounts(candidate, order)
	if len(candidateNGrams) == 0 {
		return 0
	}
	referenceNGrams := nGramCounts(reference, order)
	hitCount := 0
	totalCount := 0
	for gram, count := range candidateNGrams {
		totalCount += count
		if count > referenceNGrams[gram] {
			hitCount += referenceNGrams[gram]
		} else {
			hitCount += count
		}
	}
	if smoothing {
		return float64(hitCount+1) / float64(totalCount+1)
	}
	return float64(hitCount) / float64(totalCount)
}

func rougeNScore(candidate, reference []string, order int) float64 {
	candidateNGrams := nGramCounts(candidate, order)
	referenceNGrams := nGramCounts(reference, order)
	if len(candidateNGrams) == 0 || len(referenceNGrams) == 0 {
		return 0
	}
	hits := 0
	for gram, count := range candidateNGrams {
		if count > referenceNGrams[gram] {
			hits += referenceNGrams[gram]
		} else {
			hits += count
		}
	}
	precision := float64(hits) / float64(lenNGrams(candidate, order))
	recall := float64(hits) / float64(lenNGrams(reference, order))
	return f1(precision, recall)
}

func rougeLScore(candidate, reference []string) float64 {
	if len(candidate) == 0 || len(reference) == 0 {
		return 0
	}
	lcs := longestCommonSubsequence(candidate, reference)
	return f1(float64(lcs)/float64(len(candidate)), float64(lcs)/float64(len(reference)))
}

func nGramCounts(tokens []string, order int) map[string]int {
	counts := make(map[string]int)
	if order <= 0 || len(tokens) < order {
		return counts
	}
	for index := 0; index <= len(tokens)-order; index++ {
		counts[strings.Join(tokens[index:index+order], "\x00")]++
	}
	return counts
}

func lenNGrams(tokens []string, order int) int {
	if order <= 0 || len(tokens) < order {
		return 0
	}
	return len(tokens) - order + 1
}

func f1(precision, recall float64) float64 {
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func longestCommonSubsequence(left, right []string) int {
	previous := make([]int, len(right)+1)
	for _, leftToken := range left {
		current := make([]int, len(right)+1)
		for index, rightToken := range right {
			if leftToken == rightToken {
				current[index+1] = previous[index] + 1
			} else if current[index] > previous[index+1] {
				current[index+1] = current[index]
			} else {
				current[index+1] = previous[index+1]
			}
		}
		previous = current
	}
	return previous[len(right)]
}

func isHan(r rune) bool { return unicode.Is(unicode.Han, r) }
