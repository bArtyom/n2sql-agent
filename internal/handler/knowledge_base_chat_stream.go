package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

func NewKnowledgeBaseChatStream(answerer rag.StreamAnswerer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, request, ok := decodeKnowledgeBaseChatRequest(w, r)
		if !ok {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming is not supported"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		emit := func(event rag.StreamEvent) error {
			return writeSSEEvent(w, flusher, event.Type, event)
		}
		var err error
		if len(request.DocumentIDs) > 0 || request.QueryRewrite {
			optionsAnswerer, ok := answerer.(rag.OptionsStreamAnswerer)
			if !ok {
				err = rag.ErrThresholdUnavailable
			} else {
				err = optionsAnswerer.StreamWithSearchOptions(r.Context(), knowledgeBaseID, request.Message, request.TopK, request.SimilarityThreshold, retrieval.SearchOptions{DocumentIDs: request.DocumentIDs, QueryRewrite: request.QueryRewrite}, emit)
			}
		} else if request.SimilarityThreshold != 0 {
			thresholdAnswerer, ok := answerer.(rag.ThresholdStreamAnswerer)
			if !ok {
				err = rag.ErrThresholdUnavailable
			} else {
				err = thresholdAnswerer.StreamWithThreshold(r.Context(), knowledgeBaseID, request.Message, request.TopK, request.SimilarityThreshold, emit)
			}
		} else {
			err = answerer.Stream(r.Context(), knowledgeBaseID, request.Message, request.TopK, emit)
		}
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			message, _ := knowledgeBaseChatError(err)
			_ = writeSSEEvent(w, flusher, "error", struct {
				Error string `json:"error"`
			}{Error: message})
			return
		}
		_ = writeSSEEvent(w, flusher, "done", struct{}{})
	})
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, value any) error {
	switch event {
	case "sources", "delta", "done", "error":
	default:
		return fmt.Errorf("invalid SSE event type")
	}
	return writeSSEMessage(w, flusher, event, value)
}

func writeSSEMessage(w http.ResponseWriter, flusher http.Flusher, event string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode SSE event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return fmt.Errorf("write SSE event: %w", err)
	}
	flusher.Flush()
	return nil
}
