package handler

import (
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// NewAgentRunStop cancels an active standard Agent run that was started by
// POST /agent-chat/stream. The knowledge base ID in the path scopes the
// lookup, so a run cannot be stopped through another knowledge base. The
// Agent engine turns the canceled execution context into a run_canceled SSE
// event on the existing stream; this endpoint only requests the stop and
// returns once the cancel function has been invoked.
func NewAgentRunStop(hub *agentstream.Hub) http.Handler {
	return NewAgentRunStopWithStore(hub, nil)
}

func NewAgentRunStopWithStore(hub *agentstream.Hub, store agentrun.Store) http.Handler {
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
		var childIDs []string
		persistedCancellation := false
		if canceller, ok := store.(agentrun.CancellationStore); ok {
			persistedCancellation = true
			var err error
			childIDs, err = canceller.CancelTree(r.Context(), runID, knowledgeBaseID)
			if err != nil && !errors.Is(err, agentrun.ErrRunNotFound) {
				http.Error(w, `{"error":"unable to persist agent cancellation"}`, http.StatusInternalServerError)
				return
			}
			if errors.Is(err, agentrun.ErrRunNotFound) {
				http.Error(w, `{"error":"agent run not found or expired"}`, http.StatusNotFound)
				return
			}
		}
		cancelErr := hub.Cancel(runID, knowledgeBaseID)
		if cancelErr != nil {
			if errors.Is(cancelErr, agentstream.ErrRunNotFound) && !persistedCancellation {
				http.Error(w, `{"error":"agent run not found or expired"}`, http.StatusNotFound)
				return
			}
			if !errors.Is(cancelErr, agentstream.ErrRunNotFound) {
				http.Error(w, `{"error":"unable to stop agent run"}`, http.StatusInternalServerError)
				return
			}
		}
		for _, childID := range childIDs {
			_ = hub.Cancel(childID, knowledgeBaseID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"canceled"}`))
	})
}
