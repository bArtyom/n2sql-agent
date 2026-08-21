package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

const (
	maxSearchQuery = 8000
	maxSearchBody  = maxSearchQuery + 4096
)

func NewKnowledgeBaseSearch(searcher retrieval.Searcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}

		var request struct {
			Query            string  `json:"query"`
			Limit            int     `json:"limit"`
			DocumentIDs      []int64 `json:"document_ids,omitempty"`
			QueryRewrite     bool    `json:"query_rewrite,omitempty"`
			KeywordThreshold float64 `json:"keyword_threshold,omitempty"`
			Debug            bool    `json:"debug,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSearchBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeSearchDecodeError(w, err)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, `{"error":"invalid search request"}`, http.StatusBadRequest)
			return
		}
		request.Query = strings.TrimSpace(request.Query)
		if request.Query == "" || len(request.Query) > maxSearchQuery {
			http.Error(w, `{"error":"invalid search query"}`, http.StatusBadRequest)
			return
		}
		if request.Limit == 0 {
			request.Limit = retrieval.DefaultResults
		}
		if request.Limit < 1 || request.Limit > retrieval.MaxResults {
			http.Error(w, `{"error":"invalid search limit"}`, http.StatusBadRequest)
			return
		}
		if err := retrieval.ValidateKeywordThreshold(request.KeywordThreshold); err != nil {
			http.Error(w, `{"error":"invalid search keyword_threshold"}`, http.StatusBadRequest)
			return
		}
		normalizedDocumentIDs, err := retrieval.NormalizeDocumentIDs(request.DocumentIDs)
		if err != nil {
			http.Error(w, `{"error":"invalid search document_ids"}`, http.StatusBadRequest)
			return
		}

		var results []retrieval.Result
		retrievalTracker := usage.NewRetrievalTracker()
		searchContext := usage.WithRetrievalObserver(r.Context(), retrievalTracker)
		if len(normalizedDocumentIDs) == 0 && !request.QueryRewrite && request.KeywordThreshold == 0 {
			results, err = searcher.Search(searchContext, knowledgeBaseID, request.Query, request.Limit)
		} else {
			filtered, ok := searcher.(retrieval.FilteredSearcher)
			if !ok {
				writeSearchError(w, retrieval.ErrDocumentFilterUnavailable)
				return
			}
			results, err = filtered.SearchWithOptions(searchContext, knowledgeBaseID, request.Query, request.Limit, retrieval.SearchOptions{DocumentIDs: normalizedDocumentIDs, QueryRewrite: request.QueryRewrite, KeywordThreshold: request.KeywordThreshold})
		}
		if err != nil {
			writeSearchError(w, err)
			return
		}
		stats := retrievalTracker.RetrievalSnapshot()
		var statsPointer *usage.RetrievalObservation
		if stats.HasData() {
			statsPointer = &stats
		}
		response := struct {
			Results   []retrieval.Result          `json:"results"`
			Retrieval *usage.RetrievalObservation `json:"retrieval,omitempty"`
			Explain   []retrieval.Explanation     `json:"explain,omitempty"`
		}{Results: results, Retrieval: statsPointer}
		if request.Debug {
			response.Explain = retrieval.ExplainResults(results)
		}
		writeJSON(w, response)
	})
}

func writeSearchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, retrieval.ErrInvalidKnowledgeBase), errors.Is(err, retrieval.ErrInvalidQuery), errors.Is(err, retrieval.ErrInvalidLimit), errors.Is(err, retrieval.ErrInvalidDocumentIDs), errors.Is(err, retrieval.ErrInvalidKeywordThreshold):
		http.Error(w, `{"error":"invalid search request"}`, http.StatusBadRequest)
	case errors.Is(err, retrieval.ErrQueryRewriteUnavailable):
		http.Error(w, `{"error":"query rewrite is unavailable"}`, http.StatusInternalServerError)
	case errors.Is(err, modelprovider.ErrNotFound):
		http.Error(w, `{"error":"model provider not configured"}`, http.StatusNotFound)
	case errors.Is(err, modelruntime.ErrAPIKeyEnvironmentMismatch), errors.Is(err, modelruntime.ErrAPIKeyNotConfigured):
		http.Error(w, `{"error":"model provider API key is not configured"}`, http.StatusBadRequest)
	default:
		var callError *modelruntime.EmbeddingCallError
		if errors.As(err, &callError) {
			http.Error(w, `{"error":"embedding request failed"}`, http.StatusBadGateway)
			return
		}
		http.Error(w, `{"error":"unable to search document chunks"}`, http.StatusInternalServerError)
	}
}

func writeSearchDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(w, `{"error":"search request is too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, `{"error":"invalid search request"}`, http.StatusBadRequest)
}
