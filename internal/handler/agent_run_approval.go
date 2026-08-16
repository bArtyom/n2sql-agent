package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

type agentRunApprovalRequest struct {
	Approved bool `json:"approved"`
}

// NewAgentRunApproval resolves the tool confirmation currently blocking an
// Agent run. The run and knowledge-base IDs are both checked by the Hub.
func NewAgentRunApproval(hub *agentstream.Hub) http.Handler {
	if hub == nil {
		hub = agentstream.NewHub()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		runID := r.PathValue("runID")
		if runID == "" {
			http.Error(w, `{"error":"invalid agent run ID"}`, http.StatusBadRequest)
			return
		}
		var request agentRunApprovalRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, `{"error":"invalid approval request"}`, http.StatusBadRequest)
			return
		}
		if err := hub.ResolveApproval(runID, knowledgeBaseID, request.Approved); err != nil {
			if errors.Is(err, agentstream.ErrApprovalNotFound) {
				http.Error(w, `{"error":"agent approval not found or already resolved"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"unable to resolve agent approval"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"resolved"}`))
	})
}
