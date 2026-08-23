package documentextractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWeKnoraCloudParserEngineSignsSubmitsAndPolls(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, header := range []string{"X-APPID", "X-API-Key", "X-Request-ID", "X-Timestamp", "X-Nonce", "X-Signature"} {
			if r.Header.Get(header) == "" {
				t.Fatalf("missing cloud signature header %s", header)
			}
		}
		if r.URL.Path == "/reader" {
			var request struct {
				FileContent string `json:"file_content"`
				FileName    string `json:"file_name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode cloud submit: %v", err)
			}
			if request.FileName != "guide.pdf" || request.FileContent == "" {
				t.Fatalf("cloud submit request: %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"task_id": "task-1"})
			return
		}
		if r.URL.Path == "/task-1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "completed",
				"result": map[string]any{
					"markdown_content": "# Cloud result",
					"image_refs": []any{map[string]any{
						"filename": "chart.png", "mime_type": "image/png", "image_data": base64.StdEncoding.EncodeToString([]byte("png")), "page": 2,
					}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	engine, err := NewWeKnoraCloudParserEngine(server.URL, "app-id", "secret", []string{"127.0.0.1"}, server.Client())
	if err != nil {
		t.Fatalf("new cloud engine: %v", err)
	}
	engine.pollInitial = time.Millisecond
	engine.pollMax = time.Millisecond
	engine.pollTimeout = time.Second
	result, err := engine.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf", Filename: "guide.pdf", EngineName: "weknoracloud"})
	if err != nil {
		t.Fatalf("cloud parse: %v", err)
	}
	if result.Markdown != "# Cloud result" || len(result.Images) != 1 || result.Images[0].Page != 2 || result.Metadata["parser_provider"] != "weknoracloud" {
		t.Fatalf("cloud result: %#v", result)
	}
}

func TestWeKnoraCloudParserRequiresHTTPSAndCredentials(t *testing.T) {
	if _, err := NewWeKnoraCloudParserEngine("http://127.0.0.1", "app", "secret", []string{"127.0.0.1"}, nil); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS validation error, got %v", err)
	}
	if _, err := NewWeKnoraCloudParserEngine("https://weknora.weixin.qq.com/api/v1/doc", "", "secret", nil, nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected credential validation error, got %v", err)
	}
}
