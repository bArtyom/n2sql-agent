package handler_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type documentUploaderStub struct {
	input document.UploadInput
	err   error
}

func (s *documentUploaderStub) Upload(_ context.Context, input document.UploadInput) (document.Document, error) {
	s.input = input
	if s.err != nil {
		return document.Document{}, s.err
	}
	return document.Document{ID: 12, KnowledgeBaseID: input.KnowledgeBaseID, OriginalFilename: input.OriginalFilename, ContentType: input.ContentType, SizeBytes: 17, ProcessingStatus: "pending"}, nil
}

func TestDocumentUploadAcceptsTextFile(t *testing.T) {
	uploader := &documentUploaderStub{}
	endpoint := handler.NewDocumentUpload(uploader)
	body, contentType := multipartBody(t, "notes.txt", []byte("hello knowledge base"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body).WithContext(context.Background())
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusCreated)
	}
	if uploader.input.KnowledgeBaseID != 4 || uploader.input.ContentType != "text/plain" {
		t.Fatalf("upload input = %#v", uploader.input)
	}
}

func TestDocumentUploadRejectsUnsupportedFile(t *testing.T) {
	endpoint := handler.NewDocumentUpload(&documentUploaderStub{})
	body, contentType := multipartBody(t, "malware.exe", []byte("not a document"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
	}
}

func TestDocumentUploadReportsMissingKnowledgeBase(t *testing.T) {
	endpoint := handler.NewDocumentUpload(&documentUploaderStub{err: document.ErrKnowledgeBaseNotFound})
	body, contentType := multipartBody(t, "notes.txt", []byte("hello"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func multipartBody(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}
