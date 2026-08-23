package documentextractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPParserEngineReturnsUnifiedParseResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Parser-Engine") != "remote_http" || r.Header.Get("X-Document-Content-Type") != "application/pdf" {
			t.Fatalf("unexpected request method/headers: %s %+v", r.Method, r.Header)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("file field: %v", err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil || string(content) != "pdf-bytes" {
			t.Fatalf("uploaded content = %q, err=%v", content, err)
		}
		response := remoteParserResponse{
			Markdown: "# Parsed\ncontent",
			Images:   []remoteImage{{Filename: "page-1.png", MIMEType: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte("image")), Page: 1, Source: "remote"}},
			Metadata: map[string]string{"parser_mode": "layout"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	engine, err := NewHTTPParserEngine("remote_http", server.URL, []string{"application/pdf"}, []string{"127.0.0.1"}, server.Client())
	if err != nil {
		t.Fatalf("create remote engine: %v", err)
	}
	result, err := engine.Parse(context.Background(), ParseRequest{Content: []byte("pdf-bytes"), ContentType: "application/pdf", Filename: "scan.pdf"})
	if err != nil {
		t.Fatalf("parse remote document: %v", err)
	}
	if result.Markdown != "# Parsed\ncontent" || len(result.Images) != 1 || result.Metadata["parser_transport"] != "http" {
		t.Fatalf("unexpected parse result: %#v", result)
	}
}

func TestHTTPParserEngineRejectsUnallowedHost(t *testing.T) {
	_, err := NewHTTPParserEngine("remote_http", "http://example.com/parse", []string{"application/pdf"}, []string{"127.0.0.1"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected host rejection, got %v", err)
	}
}
