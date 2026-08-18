package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/memory"
)

const maxMemoryRequestBytes = 4096

func NewMemories(store memory.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, authenticated := auth.UserFromContext(r.Context())
		if !authenticated {
			writeMemoryError(w, memory.ErrUnauthorized)
			return
		}
		knowledgeBaseID, err := parsePositiveID(r.PathValue("id"))
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		if r.PathValue("memoryID") != "" {
			memoryID, err := parsePositiveID(r.PathValue("memoryID"))
			if err != nil {
				http.Error(w, `{"error":"invalid memory ID"}`, http.StatusBadRequest)
				return
			}
			if r.Method != http.MethodDelete {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := store.Delete(r.Context(), user.ID, knowledgeBaseID, memoryID); err != nil {
				writeMemoryError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch r.Method {
		case http.MethodGet:
			items, err := store.List(r.Context(), user.ID, knowledgeBaseID)
			if err != nil {
				writeMemoryError(w, err)
				return
			}
			writeJSON(w, struct {
				Memories []memory.Memory `json:"memories"`
			}{Memories: items})
		case http.MethodPost:
			var request struct {
				Content string `json:"content"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMemoryRequestBytes))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				http.Error(w, `{"error":"invalid memory request"}`, http.StatusBadRequest)
				return
			}
			content := strings.TrimSpace(request.Content)
			if content == "" || len([]byte(content)) > memory.MaxContentBytes {
				writeMemoryError(w, memory.ErrInvalidContent)
				return
			}
			item, err := store.Create(r.Context(), user.ID, memory.CreateInput{KnowledgeBaseID: knowledgeBaseID, Content: content})
			if err != nil {
				writeMemoryError(w, err)
				return
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, item)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func writeMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrInvalidKnowledgeBase), errors.Is(err, memory.ErrInvalidMemory), errors.Is(err, memory.ErrInvalidContent):
		http.Error(w, `{"error":"invalid memory request"}`, http.StatusBadRequest)
	case errors.Is(err, memory.ErrUnauthorized):
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	case errors.Is(err, memory.ErrNotFound):
		http.Error(w, `{"error":"memory not found"}`, http.StatusNotFound)
	default:
		http.Error(w, `{"error":"memory operation failed"}`, http.StatusInternalServerError)
	}
}
