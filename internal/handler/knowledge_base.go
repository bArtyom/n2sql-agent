package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
)

const (
	maxKnowledgeBaseRequestBytes = 4096
	maxKnowledgeBaseNameBytes    = 200
	maxKnowledgeBaseDescription  = 2000
)

func NewKnowledgeBases(store knowledgebase.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listKnowledgeBases(w, r, store)
		case http.MethodPost:
			createKnowledgeBase(w, r, store)
		case http.MethodDelete:
			deleteKnowledgeBase(w, r, store)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func listKnowledgeBases(w http.ResponseWriter, r *http.Request, store knowledgebase.Store) {
	knowledgeBases, err := store.List(r.Context())
	if err != nil {
		http.Error(w, `{"error":"unable to list knowledge bases"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, knowledgeBases)
}

func createKnowledgeBase(w http.ResponseWriter, r *http.Request, store knowledgebase.Store) {
	var request struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKnowledgeBaseRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeKnowledgeBaseDecodeError(w, err)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, `{"error":"invalid knowledge base"}`, http.StatusBadRequest)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Description = strings.TrimSpace(request.Description)
	if request.Name == "" || len(request.Name) > maxKnowledgeBaseNameBytes || len(request.Description) > maxKnowledgeBaseDescription {
		http.Error(w, `{"error":"invalid knowledge base"}`, http.StatusBadRequest)
		return
	}

	knowledgeBase, err := store.Create(r.Context(), knowledgebase.CreateInput{Name: request.Name, Description: request.Description})
	if errors.Is(err, knowledgebase.ErrConflict) {
		http.Error(w, `{"error":"knowledge base already exists"}`, http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"unable to create knowledge base"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, knowledgeBase)
}

func deleteKnowledgeBase(w http.ResponseWriter, r *http.Request, store knowledgebase.Store) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
		return
	}
	if err := store.Delete(r.Context(), id); errors.Is(err, knowledgebase.ErrNotFound) {
		http.Error(w, `{"error":"knowledge base not found"}`, http.StatusNotFound)
	} else if errors.Is(err, knowledgebase.ErrProcessing) {
		http.Error(w, `{"error":"knowledge base has documents in processing"}`, http.StatusConflict)
	} else if err != nil {
		http.Error(w, `{"error":"unable to delete knowledge base"}`, http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeKnowledgeBaseDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(w, `{"error":"knowledge base request is too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, `{"error":"invalid knowledge base"}`, http.StatusBadRequest)
}
