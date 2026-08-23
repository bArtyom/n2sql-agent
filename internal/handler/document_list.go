package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documenttag"
)

func NewDocumentList(reader document.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		var documents []document.Document
		tagIDs, err := parseDocumentTagIDs(r.URL.Query()["tag_ids"])
		if err != nil {
			http.Error(w, `{"error":"invalid tag_ids"}`, http.StatusBadRequest)
			return
		}
		folderPath, hasFolder := r.URL.Query()["folder_path"]
		if hasFolder {
			recursive := r.URL.Query().Get("folder_recursive") == "true"
			path := ""
			if len(folderPath) > 0 {
				path = folderPath[0]
			}
			if len(tagIDs) > 0 {
				folderTagReader, ok := reader.(document.FolderTagReader)
				if !ok {
					http.Error(w, `{"error":"folder tag listing is unavailable"}`, http.StatusNotImplemented)
					return
				}
				documents, err = folderTagReader.ListInFolderWithTags(r.Context(), knowledgeBaseID, path, recursive, tagIDs)
			} else {
				folderReader, ok := reader.(document.FolderReader)
				if !ok {
					http.Error(w, `{"error":"folder listing is unavailable"}`, http.StatusNotImplemented)
					return
				}
				documents, err = folderReader.ListInFolder(r.Context(), knowledgeBaseID, path, recursive)
			}
		} else if len(tagIDs) > 0 {
			tagReader, ok := reader.(document.TagReader)
			if !ok {
				http.Error(w, `{"error":"tag listing is unavailable"}`, http.StatusNotImplemented)
				return
			}
			documents, err = tagReader.ListWithTags(r.Context(), knowledgeBaseID, tagIDs)
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
		if errors.Is(err, documenttag.ErrInvalidTagIDs) || errors.Is(err, documenttag.ErrTagNotFound) {
			http.Error(w, `{"error":"invalid tag_ids"}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to list documents"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, documents)
	})
}

func parseDocumentTagIDs(values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	parts := strings.Split(strings.Join(values, ","), ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, documenttag.ErrInvalidTagIDs
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, documenttag.ErrInvalidTagIDs
		}
		ids = append(ids, id)
	}
	return documenttag.NormalizeIDs(ids)
}
