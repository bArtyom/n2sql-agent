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

func TestMinerUParserEngineUsesFileParseProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file_parse" || r.Method != http.MethodPost {
			t.Fatalf("unexpected MinerU request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse MinerU multipart: %v", err)
		}
		file, _, err := r.FormFile("files")
		if err != nil {
			t.Fatalf("MinerU files field: %v", err)
		}
		defer file.Close()
		content, _ := io.ReadAll(file)
		if string(content) != "pdf" || r.FormValue("return_images") != "true" {
			t.Fatalf("MinerU form content=%q return_images=%q", content, r.FormValue("return_images"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": map[string]any{
			"scan": map[string]any{
				"md_content": "# MinerU result",
				"images":     map[string]string{"figure.png": base64.StdEncoding.EncodeToString([]byte("png"))},
			},
		}})
	}))
	defer server.Close()

	engine, err := NewMinerUParserEngine(server.URL, []string{"127.0.0.1"}, server.Client())
	if err != nil {
		t.Fatalf("new MinerU engine: %v", err)
	}
	result, err := engine.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf", Filename: "scan.pdf"})
	if err != nil {
		t.Fatalf("MinerU parse: %v", err)
	}
	if result.Markdown != "# MinerU result" || len(result.Images) != 1 || result.Metadata["parser_provider"] != "mineru" {
		t.Fatalf("MinerU result = %#v", result)
	}
}

func TestPaddleOCRVLParserEngineUsesLayoutParsingProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/layout-parsing" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected PaddleOCR-VL request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode PaddleOCR-VL request: %v", err)
		}
		if request["fileType"] != float64(0) || request["file"] == "" {
			t.Fatalf("PaddleOCR-VL payload = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errorCode": 0,
			"result": map[string]any{"layoutParsingResults": []any{
				map[string]any{"markdown": map[string]any{
					"text":   "# OCR page",
					"images": map[string]string{"chart.jpg": base64.StdEncoding.EncodeToString([]byte("jpg"))},
				}},
			}},
		})
	}))
	defer server.Close()

	engine, err := NewPaddleOCRVLParserEngine(server.URL, []string{"127.0.0.1"}, server.Client())
	if err != nil {
		t.Fatalf("new PaddleOCR-VL engine: %v", err)
	}
	result, err := engine.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf", Filename: "scan.pdf"})
	if err != nil {
		t.Fatalf("PaddleOCR-VL parse: %v", err)
	}
	if !strings.Contains(result.Markdown, "[Page 1]") || len(result.Images) != 1 || result.Images[0].Page != 1 || result.Metadata["parser_provider"] != "paddleocr_vl" {
		t.Fatalf("PaddleOCR-VL result = %#v", result)
	}
}

func TestPaddleOCRVLParserAnalyzesPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/layout-parsing" || r.Method != http.MethodPost {
			t.Fatalf("unexpected PaddleOCR-VL request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode PaddleOCR-VL request: %v", err)
		}
		if request["fileType"] != float64(1) || request["useLayoutDetection"] != true || request["restructurePages"] != true {
			t.Fatalf("PaddleOCR-VL page payload = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errorCode": 0,
			"result": map[string]any{"layoutParsingResults": []any{
				map[string]any{"markdown": map[string]any{
					"text":   "| Name | Value |\n| --- | --- |\n| Agent | RAG |",
					"images": map[string]string{"figure.png": base64.StdEncoding.EncodeToString([]byte("png"))},
				}},
			}},
		})
	}))
	defer server.Close()

	engine, err := NewPaddleOCRVLParserEngine(server.URL, []string{"127.0.0.1"}, server.Client())
	if err != nil {
		t.Fatalf("new PaddleOCR-VL engine: %v", err)
	}
	blocks, err := engine.AnalyzePage(context.Background(), PDFPage{Number: 5, Image: []byte("page")})
	if err != nil {
		t.Fatalf("AnalyzePage() error = %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v, want table and figure", blocks)
	}
	if blocks[0].Page != 5 || blocks[0].Kind != PDFBlockTable || !strings.Contains(blocks[0].Text, "Agent") {
		t.Fatalf("text block = %#v", blocks[0])
	}
	if blocks[1].Page != 5 || blocks[1].Kind != PDFBlockFigure || string(blocks[1].Image) != "png" || blocks[1].MIMEType != "image/png" {
		t.Fatalf("figure block = %#v", blocks[1])
	}
}
