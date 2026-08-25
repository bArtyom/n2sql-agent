package knowledgegraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

// QueryEntityExtractor is deliberately narrower than Extractor so the query
// path can be tested without a model and can later use a dedicated NER model.
type QueryEntityExtractor interface {
	ExtractQueryEntities(context.Context, string) ([]string, *modelclient.TokenUsage, error)
}

// Retriever turns graph references back into normal document chunks. The
// graph is only a recall sidecar: ranking, context expansion and citations
// remain owned by the existing Hybrid RAG service.
type Retriever struct {
	store     Store
	extractor QueryEntityExtractor
	chunks    documentchunk.Reader
}

func NewRetriever(store Store, extractor QueryEntityExtractor, chunks documentchunk.Reader) *Retriever {
	return &Retriever{store: store, extractor: extractor, chunks: chunks}
}

// SearchGraph performs query entity extraction followed by one-hop graph
// recall. It returns ordinary SearchResult values so the retrieval package can
// merge them with vector and keyword candidates before one final rerank.
func (r *Retriever) SearchGraph(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64) ([]documentchunk.SearchResult, error) {
	if r == nil || r.store == nil || r.extractor == nil || r.chunks == nil {
		return nil, nil
	}
	if knowledgeBaseID <= 0 || limit <= 0 {
		return nil, nil
	}
	entities, _, err := r.extractor.ExtractQueryEntities(ctx, strings.TrimSpace(query))
	if err != nil {
		return nil, fmt.Errorf("extract graph query entities: %w", err)
	}
	entities = normalizeQueryEntities(entities)
	if len(entities) == 0 {
		return nil, nil
	}
	graph, err := r.store.Search(ctx, knowledgeBaseID, entities, documentIDs)
	if err != nil {
		return nil, fmt.Errorf("search knowledge graph: %w", err)
	}
	refs := make([]ChunkRef, 0, len(graph.Chunks))
	seen := make(map[string]struct{}, len(graph.Chunks))
	for _, ref := range graph.Chunks {
		key := ref.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	sort.SliceStable(refs, func(left, right int) bool {
		if refs[left].DocumentID != refs[right].DocumentID {
			return refs[left].DocumentID < refs[right].DocumentID
		}
		return refs[left].Position < refs[right].Position
	})
	if len(refs) > limit {
		refs = refs[:limit]
	}
	results := make([]documentchunk.SearchResult, 0, len(refs))
	for _, ref := range refs {
		result, readErr := r.chunks.Read(ctx, knowledgeBaseID, ref.DocumentID, ref.Position)
		if readErr != nil {
			// A stale graph reference must not make the complete hybrid recall
			// fail. The next document re-index/delete will remove it.
			continue
		}
		result.MatchType = "graph"
		// Graph hits have no vector distance. They are kept by the final
		// distance filter and are ranked by the unified reranker/fusion pass.
		result.Distance = 0
		result.FusionScore = 0.25
		results = append(results, result)
	}
	return results, nil
}

func normalizeQueryEntities(entities []string) []string {
	seen := make(map[string]struct{}, len(entities))
	result := make([]string, 0, len(entities))
	for _, entity := range entities {
		entity = strings.TrimSpace(entity)
		if entity == "" {
			continue
		}
		key := strings.ToLower(entity)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, entity)
	}
	return result
}
