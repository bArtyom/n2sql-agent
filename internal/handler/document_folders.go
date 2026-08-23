package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

func parseKnowledgeBaseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid knowledge base ID")
	}
	return id, nil
}

func NewDocumentFolderTree(reader document.FolderTreeReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := parseKnowledgeBaseID(r)
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		tree, err := reader.ListFolderTree(r.Context(), knowledgeBaseID)
		if errors.Is(err, document.ErrKnowledgeBaseNotFound) {
			http.Error(w, `{"error":"knowledge base not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to list document folders"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, tree)
	})
}

type moveDocumentsRequest struct {
	DocumentIDs []int64 `json:"documentIds"`
	FolderPath  string  `json:"folderPath"`
}

func NewDocumentMoveToFolder(mover document.FolderMover) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := parseKnowledgeBaseID(r)
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		var request moveDocumentsRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		moved, err := mover.MoveToFolder(r.Context(), knowledgeBaseID, request.DocumentIDs, request.FolderPath)
		if errors.Is(err, document.ErrKnowledgeBaseNotFound) || errors.Is(err, document.ErrDocumentNotFound) {
			http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, document.ErrNoDocumentsSelected) || errors.Is(err, document.ErrInvalidFolderPath) {
			http.Error(w, `{"error":"invalid folder request"}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to move documents"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct {
			MovedCount int64  `json:"movedCount"`
			FolderPath string `json:"folderPath"`
		}{MovedCount: moved, FolderPath: request.FolderPath})
	})
}

type renameDocumentFolderRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func NewDocumentFolderRename(renamer document.FolderRenamer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := parseKnowledgeBaseID(r)
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		var request renameDocumentFolderRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		moved, err := renamer.RenameFolder(r.Context(), knowledgeBaseID, request.From, request.To)
		if errors.Is(err, document.ErrKnowledgeBaseNotFound) {
			http.Error(w, `{"error":"knowledge base not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, document.ErrInvalidFolderPath) || errors.Is(err, document.ErrFolderMoveConflict) {
			http.Error(w, `{"error":"invalid folder rename"}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to rename document folder"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct {
			MovedCount int64  `json:"movedCount"`
			FolderPath string `json:"folderPath"`
		}{MovedCount: moved, FolderPath: request.To})
	})
}
