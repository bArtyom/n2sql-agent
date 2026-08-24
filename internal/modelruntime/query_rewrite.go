package modelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

const maxQueryRewriteBytes = 16 * 1024

// QueryRewriteService asks the configured chat model for a very small list of
// alternative search phrasings. It does not answer the user and never changes
// the original query, which remains the first retrieval query.
type QueryRewriteService struct {
	chat MessageChatRunner
}

var _ retrieval.QueryRewriter = (*QueryRewriteService)(nil)

func NewQueryRewriteService(chat MessageChatRunner) *QueryRewriteService {
	return &QueryRewriteService{chat: chat}
}

func (s *QueryRewriteService) Rewrite(ctx context.Context, query string, maxVariants int) ([]string, error) {
	if s == nil || s.chat == nil {
		return nil, retrieval.ErrQueryRewriteUnavailable
	}
	query = strings.TrimSpace(query)
	if query == "" || maxVariants < 1 || maxVariants > retrieval.MaxQueryVariants {
		return nil, errors.New("invalid query rewrite request")
	}
	questionPayload, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Content: query})
	if err != nil {
		return nil, fmt.Errorf("encode query rewrite question: %w", err)
	}
	messages := []modelclient.ChatMessage{
		{Role: "system", Content: "你是检索查询改写器。只输出 JSON 数组，例如 [\"查询一\",\"查询二\"]。保留原问题的事实范围，不回答问题，不添加原文没有的实体或条件。最多输出指定数量的简洁检索表达。"},
		{Role: "user", Content: "<question_json>\n" + string(questionPayload) + "\n</question_json>\n<max_variants>" + fmt.Sprint(maxVariants) + "</max_variants>"},
	}
	response, err := s.chat.ChatMessages(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("rewrite query with chat model: %w", err)
	}
	if observer := usage.ObserverFromContext(ctx); observer != nil && response.Usage != nil {
		if _, alreadyObserved := observer.(usage.CallObserver); !alreadyObserved {
			observer.ObserveChatTokens(*response.Usage)
		}
	}
	content := strings.TrimSpace(response.Message)
	if len(content) > maxQueryRewriteBytes {
		return nil, errors.New("query rewrite response is too large")
	}
	content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(content, "```"), "```json"))
	var variants []string
	if err := json.Unmarshal([]byte(content), &variants); err != nil {
		var payload struct {
			Queries []string `json:"queries"`
		}
		if objectErr := json.Unmarshal([]byte(content), &payload); objectErr != nil {
			return nil, fmt.Errorf("decode query rewrite response: %w", err)
		}
		variants = payload.Queries
	}
	result := make([]string, 0, maxVariants)
	seen := make(map[string]struct{}, maxVariants)
	for _, variant := range variants {
		variant = strings.TrimSpace(variant)
		if variant == "" || len(variant) > 8000 {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(variant), " "))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, variant)
		if len(result) == maxVariants {
			break
		}
	}
	return result, nil
}
