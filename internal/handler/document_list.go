package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

func NewDocumentList(reader document.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		var documents []document.Document
		folderPath, hasFolder := r.URL.Query()["folder_path"]
		if hasFolder {
			folderReader, ok := reader.(document.FolderReader)
			if !ok {
				http.Error(w, `{"error":"folder listing is unavailable"}`, http.StatusNotImplemented)
				return
			}
			recursive := r.URL.Query().Get("folder_recursive") == "true"
			path := ""
			if len(folderPath) > 0 {
				path = folderPath[0]
			}
			documents, err = folderReader.ListInFolder(r.Context(), knowledgeBaseID, path, recursive)
		} else {
			documents, err = reader.List(r.Context(), knowledgeBaseID)
		}
		if errors.Is(err, document.ErrKnowledgeBaseNotFound) {
			http.Error(w, `{"error":"knowledge base not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, document.ErrInvalidFolderPath) {
			http.Error(w, `{"error":"invalid folder path"}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to list documents"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, documents)
	})
}
