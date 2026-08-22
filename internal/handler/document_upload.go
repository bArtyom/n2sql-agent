package handler

import (
	"bufio"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

const maxUploadRequestBytes int64 = document.MaxFileBytes + (1 << 20)
const maxOriginalFilenameBytes = 255

func NewDocumentUpload(uploader document.Uploader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequestBytes)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			writeDocumentUploadError(w, err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, `{"error":"file is required"}`, http.StatusBadRequest)
			return
		}
		defer file.Close()
		content := bufio.NewReader(file)
		filename := filepath.Base(header.Filename)
		if !validUploadFilename(filename) {
			http.Error(w, `{"error":"invalid file name"}`, http.StatusBadRequest)
			return
		}
		contentType, err := uploadContentType(filename, content)
		if err != nil {
			http.Error(w, `{"error":"unsupported file"}`, http.StatusUnsupportedMediaType)
			return
		}
		document, err := uploader.Upload(r.Context(), document.UploadInput{
			KnowledgeBaseID:  knowledgeBaseID,
			OriginalFilename: filename,
			ContentType:      contentType,
			Content:          content,
		})
		if err != nil {
			writeDocumentUploadError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, document)
	})
}

func validUploadFilename(filename string) bool {
	if strings.TrimSpace(filename) == "" || len(filename) > maxOriginalFilenameBytes {
		return false
	}
	return !strings.ContainsAny(filename, "\r\n\x00")
}

func uploadContentType(filename string, content *bufio.Reader) (string, error) {
	extension := strings.ToLower(filepath.Ext(filename))
	if extension == ".md" {
		return "text/markdown", nil
	}
	if extension == ".txt" {
		return "text/plain", nil
	}
	if extension == ".html" || extension == ".htm" {
		return "text/html", nil
	}
	if extension == ".docx" {
		magic, err := content.Peek(4)
		if err != nil || len(magic) != 4 || string(magic) != "PK\x03\x04" {
			return "", document.ErrUnsupportedFile
		}
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	}
	if extension == ".pptx" {
		magic, err := content.Peek(4)
		if err != nil || len(magic) != 4 || string(magic) != "PK\x03\x04" {
			return "", document.ErrUnsupportedFile
		}
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
	}
	if extension == ".xlsx" {
		magic, err := content.Peek(4)
		if err != nil || len(magic) != 4 || string(magic) != "PK\x03\x04" {
			return "", document.ErrUnsupportedFile
		}
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	}
	if extension != ".pdf" {
		return "", document.ErrUnsupportedFile
	}
	magic, err := content.Peek(5)
	if err != nil || string(magic) != "%PDF-" {
		return "", document.ErrUnsupportedFile
	}
	return "application/pdf", nil
}

func writeDocumentUploadError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesError), errors.Is(err, document.ErrFileTooLarge):
		http.Error(w, `{"error":"file is too large"}`, http.StatusRequestEntityTooLarge)
	case errors.Is(err, document.ErrKnowledgeBaseNotFound):
		http.Error(w, `{"error":"knowledge base not found"}`, http.StatusNotFound)
	case errors.Is(err, document.ErrUnsupportedFile):
		http.Error(w, `{"error":"unsupported file"}`, http.StatusUnsupportedMediaType)
	case errors.Is(err, document.ErrDuplicateDocument):
		http.Error(w, `{"error":"document already exists"}`, http.StatusConflict)
	default:
		http.Error(w, `{"error":"unable to upload document"}`, http.StatusInternalServerError)
	}
}
