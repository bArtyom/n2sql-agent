package handler

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/documenttag"
)

const maxUploadRequestBytes int64 = document.MaxFileBytes + (1 << 20)
const maxOriginalFilenameBytes = 255
const maxProcessConfigBytes = 64 << 10

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
		processConfig, err := parseProcessConfig(r.FormValue("process_config"))
		if err != nil {
			http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
			return
		}
		tagIDs, err := parseDocumentTagIDs(r.Form["tag_ids"])
		if err != nil {
			http.Error(w, `{"error":"invalid tag_ids"}`, http.StatusBadRequest)
			return
		}
		document, err := uploader.Upload(r.Context(), document.UploadInput{
			KnowledgeBaseID:  knowledgeBaseID,
			OriginalFilename: filename,
			FolderPath:       r.FormValue("folder_path"),
			ContentType:      contentType,
			Content:          content,
			TagIDs:           tagIDs,
			ProcessConfig:    processConfig,
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

func parseProcessConfig(raw string) (*documentextractor.ProcessConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len([]byte(raw)) > maxProcessConfigBytes {
		return nil, errors.New("process_config is too large")
	}
	var config documentextractor.ProcessConfig
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("process_config must contain one JSON object")
		}
		return nil, err
	}
	if err := documentextractor.ValidateProcessConfig(&config); err != nil {
		return nil, err
	}
	return &config, nil
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
	if extension == ".png" {
		magic, err := content.Peek(8)
		if err != nil || len(magic) != 8 || string(magic) != "\x89PNG\r\n\x1a\n" {
			return "", document.ErrUnsupportedFile
		}
		return "image/png", nil
	}
	if extension == ".jpg" || extension == ".jpeg" {
		magic, err := content.Peek(3)
		if err != nil || len(magic) != 3 || string(magic) != "\xff\xd8\xff" {
			return "", document.ErrUnsupportedFile
		}
		return "image/jpeg", nil
	}
	if extension == ".webp" {
		magic, err := content.Peek(12)
		if err != nil || len(magic) != 12 || string(magic[:4]) != "RIFF" || string(magic[8:]) != "WEBP" {
			return "", document.ErrUnsupportedFile
		}
		return "image/webp", nil
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
	case errors.Is(err, document.ErrInvalidFolderPath):
		http.Error(w, `{"error":"invalid folder path"}`, http.StatusBadRequest)
	case errors.Is(err, document.ErrInvalidProcessConfig):
		http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
	case errors.Is(err, document.ErrDuplicateDocument):
		http.Error(w, `{"error":"document already exists"}`, http.StatusConflict)
	case errors.Is(err, documenttag.ErrInvalidTagIDs), errors.Is(err, documenttag.ErrTagNotFound):
		http.Error(w, `{"error":"invalid tag_ids"}`, http.StatusBadRequest)
	default:
		http.Error(w, `{"error":"unable to upload document"}`, http.StatusInternalServerError)
	}
}
