package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/documenttag"
)

const maxDocumentTagRequestBytes = 16 << 10

type documentTagRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type documentTagUpdateRequest struct {
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty"`
}

type documentTagAssignmentRequest struct {
	TagIDs []int64 `json:"tagIds"`
}

// NewDocumentTags handles tag CRUD and the document-to-tag replacement
// operation. The replacement operation is intentionally atomic: an empty
// tagIds array clears all tags, while unknown tag IDs reject the whole update.
func NewDocumentTags(tags documenttag.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := parsePositivePathID(r, "id")
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		if documentIDText := r.PathValue("documentID"); documentIDText != "" {
			handleDocumentTags(w, r, tags, knowledgeBaseID, documentIDText)
			return
		}
		handleTagCollection(w, r, tags, knowledgeBaseID)
	})
}

func handleTagCollection(w http.ResponseWriter, r *http.Request, tags documenttag.Store, knowledgeBaseID int64) {
	switch r.Method {
	case http.MethodGet:
		items, err := tags.List(r.Context(), knowledgeBaseID)
		if err != nil {
			writeDocumentTagError(w, err)
			return
		}
		writeJSON(w, items)
	case http.MethodPost:
		var request documentTagRequest
		if err := decodeDocumentTagJSON(w, r, &request); err != nil {
			return
		}
		name, color, err := validateDocumentTagFields(request.Name, request.Color)
		if err != nil {
			writeDocumentTagError(w, err)
			return
		}
		item, err := tags.Create(r.Context(), knowledgeBaseID, documenttag.CreateInput{Name: name, Color: color})
		if err != nil {
			writeDocumentTagError(w, err)
			return
		}
		writeDocumentTagJSONStatus(w, http.StatusCreated, item)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleDocumentTags(w http.ResponseWriter, r *http.Request, tags documenttag.Store, knowledgeBaseID int64, documentIDText string) {
	documentID, err := strconv.ParseInt(documentIDText, 10, 64)
	if err != nil || documentID <= 0 {
		http.Error(w, `{"error":"invalid document ID"}`, http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := tags.ListDocumentTags(r.Context(), knowledgeBaseID, documentID)
		if err != nil {
			writeDocumentTagError(w, err)
			return
		}
		writeJSON(w, items)
	case http.MethodPut:
		var request documentTagAssignmentRequest
		if err := decodeDocumentTagJSON(w, r, &request); err != nil {
			return
		}
		tagIDs, err := documenttag.NormalizeIDs(request.TagIDs)
		if err != nil {
			writeDocumentTagError(w, err)
			return
		}
		items, err := tags.SetDocumentTags(r.Context(), knowledgeBaseID, documentID, tagIDs)
		if err != nil {
			writeDocumentTagError(w, err)
			return
		}
		writeJSON(w, items)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func NewDocumentTagItem(tags documenttag.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := parsePositivePathID(r, "id")
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		tagID, err := parsePositivePathID(r, "tagID")
		if err != nil {
			http.Error(w, `{"error":"invalid tag ID"}`, http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var request documentTagUpdateRequest
			if err := decodeDocumentTagJSON(w, r, &request); err != nil {
				return
			}
			input, err := validateDocumentTagUpdate(request)
			if err != nil {
				writeDocumentTagError(w, err)
				return
			}
			item, err := tags.Update(r.Context(), knowledgeBaseID, tagID, input)
			if err != nil {
				writeDocumentTagError(w, err)
				return
			}
			writeJSON(w, item)
		case http.MethodDelete:
			if err := tags.Delete(r.Context(), knowledgeBaseID, tagID); err != nil {
				writeDocumentTagError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func parsePositivePathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid path ID")
	}
	return id, nil
}

func decodeDocumentTagJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentTagRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		http.Error(w, `{"error":"invalid tag request"}`, http.StatusBadRequest)
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, `{"error":"invalid tag request"}`, http.StatusBadRequest)
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateDocumentTagFields(nameValue, colorValue string) (string, string, error) {
	name, err := documenttag.ValidateName(nameValue)
	if err != nil {
		return "", "", err
	}
	color, err := documenttag.ValidateColor(colorValue)
	if err != nil {
		return "", "", err
	}
	return name, color, nil
}

func validateDocumentTagUpdate(request documentTagUpdateRequest) (documenttag.UpdateInput, error) {
	if request.Name == nil && request.Color == nil {
		return documenttag.UpdateInput{}, documenttag.ErrInvalidTagName
	}
	input := documenttag.UpdateInput{}
	if request.Name != nil {
		name, err := documenttag.ValidateName(*request.Name)
		if err != nil {
			return documenttag.UpdateInput{}, err
		}
		input.Name = &name
	}
	if request.Color != nil {
		color, err := documenttag.ValidateColor(*request.Color)
		if err != nil {
			return documenttag.UpdateInput{}, err
		}
		input.Color = &color
	}
	return input, nil
}

func writeDocumentTagJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeDocumentTagError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, documenttag.ErrTagNotFound), errors.Is(err, documenttag.ErrDocumentNotFound):
		http.Error(w, `{"error":"tag or document not found"}`, http.StatusNotFound)
	case errors.Is(err, documenttag.ErrTagConflict):
		http.Error(w, `{"error":"tag already exists"}`, http.StatusConflict)
	case errors.Is(err, documenttag.ErrInvalidTagIDs), errors.Is(err, documenttag.ErrInvalidTagName), errors.Is(err, documenttag.ErrInvalidTagColor):
		http.Error(w, `{"error":"invalid tag request"}`, http.StatusBadRequest)
	default:
		http.Error(w, `{"error":"unable to update document tags"}`, http.StatusInternalServerError)
	}
}
