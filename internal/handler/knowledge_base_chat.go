package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

const (
	maxChatQuestion = rag.MaxQuestionBytes
	maxChatBody     = maxChatQuestion + 4096
)

type knowledgeBaseChatRequest struct {
	Message             string  `json:"message"`
	TopK                int     `json:"topK"`
	SimilarityThreshold float64 `json:"similarity_threshold,omitempty"`
	KeywordThreshold    float64 `json:"keyword_threshold,omitempty"`
	DocumentIDs         []int64 `json:"document_ids,omitempty"`
	TagIDs              []int64 `json:"tag_ids,omitempty"`
	FolderPath          *string `json:"folder_path,omitempty"`
	FolderRecursive     bool    `json:"folder_recursive,omitempty"`
	QueryRewrite        bool    `json:"query_rewrite,omitempty"`
}

func NewKnowledgeBaseChat(answerer rag.Answerer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, request, ok := decodeKnowledgeBaseChatRequest(w, r)
		if !ok {
			return
		}

		var response rag.Response
		var err error
		if len(request.DocumentIDs) > 0 || len(request.TagIDs) > 0 || request.FolderPath != nil || request.QueryRewrite || request.KeywordThreshold != 0 {
			optionsAnswerer, ok := answerer.(rag.OptionsAnswerer)
			if !ok {
				writeKnowledgeBaseChatError(w, rag.ErrThresholdUnavailable)
				return
			}
			response, err = optionsAnswerer.AnswerWithSearchOptions(r.Context(), knowledgeBaseID, request.Message, request.TopK, request.SimilarityThreshold, retrieval.SearchOptions{DocumentIDs: request.DocumentIDs, TagIDs: request.TagIDs, FolderPath: request.FolderPath, FolderRecursive: request.FolderRecursive, QueryRewrite: request.QueryRewrite, KeywordThreshold: request.KeywordThreshold})
		} else if request.SimilarityThreshold != 0 {
			thresholdAnswerer, ok := answerer.(rag.ThresholdAnswerer)
			if !ok {
				writeKnowledgeBaseChatError(w, rag.ErrThresholdUnavailable)
				return
			}
			response, err = thresholdAnswerer.AnswerWithThreshold(r.Context(), knowledgeBaseID, request.Message, request.TopK, request.SimilarityThreshold)
		} else {
			response, err = answerer.Answer(r.Context(), knowledgeBaseID, request.Message, request.TopK)
		}
		if err != nil {
			writeKnowledgeBaseChatError(w, err)
			return
		}
		writeJSON(w, response)
	})
}

func writeKnowledgeBaseChatError(w http.ResponseWriter, err error) {
	message, status := knowledgeBaseChatError(err)
	http.Error(w, fmt.Sprintf(`{"error":%q}`, message), status)
}

func knowledgeBaseChatError(err error) (string, int) {
	switch {
	case errors.Is(err, rag.ErrNoSources):
		return "no relevant document sources found", http.StatusNotFound
	case errors.Is(err, retrieval.ErrInvalidKnowledgeBase), errors.Is(err, retrieval.ErrInvalidQuery), errors.Is(err, retrieval.ErrInvalidLimit), errors.Is(err, retrieval.ErrInvalidDocumentIDs), errors.Is(err, retrieval.ErrInvalidFolderPath), errors.Is(err, retrieval.ErrInvalidTagIDs):
		return "invalid chat request", http.StatusBadRequest
	case errors.Is(err, retrieval.ErrInvalidMaxDistance):
		return "invalid similarity threshold", http.StatusBadRequest
	case errors.Is(err, retrieval.ErrInvalidKeywordThreshold):
		return "invalid keyword threshold", http.StatusBadRequest
	case errors.Is(err, rag.ErrThresholdUnavailable):
		return "similarity threshold is unavailable", http.StatusInternalServerError
	case errors.Is(err, retrieval.ErrQueryRewriteUnavailable):
		return "query rewrite is unavailable", http.StatusInternalServerError
	case errors.Is(err, modelprovider.ErrNotFound):
		return "model provider not configured", http.StatusNotFound
	case errors.Is(err, modelruntime.ErrAPIKeyEnvironmentMismatch), errors.Is(err, modelruntime.ErrAPIKeyNotConfigured):
		return "model provider API key is not configured", http.StatusBadRequest
	case errors.Is(err, modelruntime.ErrStreamingUnavailable):
		return "streaming chat is unavailable", http.StatusInternalServerError
	default:
		var callError *modelruntime.ChatCallError
		if errors.As(err, &callError) {
			return "chat request failed", http.StatusBadGateway
		}
		return "unable to answer question", http.StatusInternalServerError
	}
}

func decodeKnowledgeBaseChatRequest(w http.ResponseWriter, r *http.Request) (int64, knowledgeBaseChatRequest, bool) {
	knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
	if !ok {
		return 0, knowledgeBaseChatRequest{}, false
	}

	var request knowledgeBaseChatRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeKnowledgeBaseChatDecodeError(w, err)
		return 0, knowledgeBaseChatRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, `{"error":"invalid chat request"}`, http.StatusBadRequest)
		return 0, knowledgeBaseChatRequest{}, false
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" || len(request.Message) > maxChatQuestion {
		http.Error(w, `{"error":"invalid chat message"}`, http.StatusBadRequest)
		return 0, knowledgeBaseChatRequest{}, false
	}
	if request.TopK == 0 {
		request.TopK = retrieval.DefaultResults
	}
	if request.TopK < 1 || request.TopK > retrieval.MaxResults {
		http.Error(w, `{"error":"invalid chat topK"}`, http.StatusBadRequest)
		return 0, knowledgeBaseChatRequest{}, false
	}
	if request.SimilarityThreshold != 0 {
		if err := retrieval.ValidateMaxDistance(request.SimilarityThreshold); err != nil {
			http.Error(w, `{"error":"invalid chat similarity_threshold"}`, http.StatusBadRequest)
			return 0, knowledgeBaseChatRequest{}, false
		}
	}
	if err := retrieval.ValidateKeywordThreshold(request.KeywordThreshold); err != nil {
		http.Error(w, `{"error":"invalid chat keyword_threshold"}`, http.StatusBadRequest)
		return 0, knowledgeBaseChatRequest{}, false
	}
	normalizedDocumentIDs, err := retrieval.NormalizeDocumentIDs(request.DocumentIDs)
	if err != nil {
		http.Error(w, `{"error":"invalid chat document_ids"}`, http.StatusBadRequest)
		return 0, knowledgeBaseChatRequest{}, false
	}
	request.DocumentIDs = normalizedDocumentIDs
	normalizedTagIDs, err := retrieval.NormalizeTagIDs(request.TagIDs)
	if err != nil {
		http.Error(w, `{"error":"invalid chat tag_ids"}`, http.StatusBadRequest)
		return 0, knowledgeBaseChatRequest{}, false
	}
	request.TagIDs = normalizedTagIDs
	if request.FolderPath != nil {
		if _, err := document.NormalizeFolderPath(*request.FolderPath); err != nil {
			http.Error(w, `{"error":"invalid chat folder_path"}`, http.StatusBadRequest)
			return 0, knowledgeBaseChatRequest{}, false
		}
	}
	return knowledgeBaseID, request, true
}

func decodeKnowledgeBaseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || knowledgeBaseID <= 0 {
		http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
		return 0, false
	}
	return knowledgeBaseID, true
}

func writeKnowledgeBaseChatDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(w, `{"error":"chat request is too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, `{"error":"invalid chat request"}`, http.StatusBadRequest)
}
