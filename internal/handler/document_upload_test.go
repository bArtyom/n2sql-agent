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

type documentReaderStub struct {
	documents []document.Document
	err       error
}

func (s documentReaderStub) List(context.Context, int64) ([]document.Document, error) {
	return s.documents, s.err
}

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

func TestDocumentUploadAcceptsHTMLFile(t *testing.T) {
	uploader := &documentUploaderStub{}
	endpoint := handler.NewDocumentUpload(uploader)
	body, contentType := multipartBody(t, "guide.html", []byte("<h1>Guide</h1>"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || uploader.input.ContentType != "text/html" {
		t.Fatalf("status=%d upload input=%#v", response.Code, uploader.input)
	}
}

func TestDocumentUploadAcceptsDOCXFile(t *testing.T) {
	uploader := &documentUploaderStub{}
	endpoint := handler.NewDocumentUpload(uploader)
	body, contentType := multipartBody(t, "guide.docx", []byte("PK\x03\x04minimal zip header"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	want := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if response.Code != http.StatusCreated || uploader.input.ContentType != want {
		t.Fatalf("status=%d upload input=%#v", response.Code, uploader.input)
	}
}

func TestDocumentUploadAcceptsPPTXAndXLSXFiles(t *testing.T) {
	for _, testCase := range []struct {
		filename, contentType string
	}{
		{"deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"table.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	} {
		uploader := &documentUploaderStub{}
		endpoint := handler.NewDocumentUpload(uploader)
		body, requestType := multipartBody(t, testCase.filename, []byte("PK\x03\x04minimal zip header"))
		request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
		request.Header.Set("Content-Type", requestType)
		request.SetPathValue("id", "4")

		response := httptest.NewRecorder()
		endpoint.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || uploader.input.ContentType != testCase.contentType {
			t.Fatalf("%s status=%d upload input=%#v", testCase.filename, response.Code, uploader.input)
		}
	}
}

func TestDocumentUploadAcceptsImages(t *testing.T) {
	for _, testCase := range []struct {
		filename, contentType string
		content               []byte
	}{
		{"scan.png", "image/png", []byte("\x89PNG\r\n\x1a\nimage")},
		{"scan.jpg", "image/jpeg", []byte("\xff\xd8\xffimage")},
		{"scan.webp", "image/webp", []byte("RIFFxxxxWEBPimage")},
	} {
		uploader := &documentUploaderStub{}
		endpoint := handler.NewDocumentUpload(uploader)
		body, requestType := multipartBody(t, testCase.filename, testCase.content)
		request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
		request.Header.Set("Content-Type", requestType)
		request.SetPathValue("id", "4")
		response := httptest.NewRecorder()
		endpoint.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || uploader.input.ContentType != testCase.contentType {
			t.Fatalf("%s status=%d upload input=%#v", testCase.filename, response.Code, uploader.input)
		}
	}
}

func TestDocumentUploadRejectsInvalidDOCXSignature(t *testing.T) {
	endpoint := handler.NewDocumentUpload(&documentUploaderStub{})
	body, contentType := multipartBody(t, "guide.docx", []byte("not a zip"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
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

func TestDocumentUploadReportsDuplicate(t *testing.T) {
	endpoint := handler.NewDocumentUpload(&documentUploaderStub{err: document.ErrDuplicateDocument})
	body, contentType := multipartBody(t, "notes.txt", []byte("hello"))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", body)
	request.Header.Set("Content-Type", contentType)
	request.SetPathValue("id", "4")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestDocumentListReturnsDocuments(t *testing.T) {
	endpoint := handler.NewDocumentList(documentReaderStub{documents: []document.Document{
		{ID: 12, KnowledgeBaseID: 4, OriginalFilename: "notes.txt", ProcessingStatus: "processing"},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/4/documents", nil)
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); !bytes.Contains([]byte(got), []byte(`"processingStatus":"processing"`)) {
		t.Fatalf("response body = %s", got)
	}
}

func TestDocumentListRejectsInvalidKnowledgeBaseID(t *testing.T) {
	endpoint := handler.NewDocumentList(documentReaderStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/nope/documents", nil)
	request.SetPathValue("id", "nope")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
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
