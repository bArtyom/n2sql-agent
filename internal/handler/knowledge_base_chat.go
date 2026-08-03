package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

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
	Message string `json:"message"`
	TopK    int    `json:"topK"`
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

		response, err := answerer.Answer(r.Context(), knowledgeBaseID, request.Message, request.TopK)
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
	case errors.Is(err, retrieval.ErrInvalidKnowledgeBase), errors.Is(err, retrieval.ErrInvalidQuery), errors.Is(err, retrieval.ErrInvalidLimit):
		return "invalid chat request", http.StatusBadRequest
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
	knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || knowledgeBaseID <= 0 {
		http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
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
	return knowledgeBaseID, request, true
}

func writeKnowledgeBaseChatDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(w, `{"error":"chat request is too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, `{"error":"invalid chat request"}`, http.StatusBadRequest)
}
