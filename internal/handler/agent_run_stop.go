package handler

import (
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// NewAgentRunStop cancels an active standard Agent run that was started by
// POST /agent-chat/stream. The knowledge base ID in the path scopes the
// lookup, so a run cannot be stopped through another knowledge base. The
// Agent engine turns the canceled execution context into a run_canceled SSE
// event on the existing stream; this endpoint only requests the stop and
// returns once the cancel function has been invoked.
func NewAgentRunStop(hub *agentstream.Hub) http.Handler {
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
		if err := hub.Cancel(runID, knowledgeBaseID); err != nil {
			if errors.Is(err, agentstream.ErrRunNotFound) {
				http.Error(w, `{"error":"agent run not found or expired"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"unable to stop agent run"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"canceled"}`))
	})
}
