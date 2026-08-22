package handler_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type assetReaderStub struct {
	asset document.Asset
	err   error
}

func (s assetReaderStub) OpenAsset(context.Context, int64, int64) (document.Asset, error) {
	return s.asset, s.err
}

func TestDocumentAssetServesInlineImage(t *testing.T) {
	endpoint := handler.NewDocumentAsset(assetReaderStub{asset: document.Asset{
		OriginalFilename: "scan.png",
		ContentType:      "image/png",
		SizeBytes:        int64(len("PNG bytes")),
		Content:          strings.NewReader("PNG bytes"),
		Close:            func() error { return nil },
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/4/documents/9/asset", nil)
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "9")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "inline") || response.Body.String() != "PNG bytes" {
		t.Fatalf("asset response headers/body = %q / %q", response.Header().Get("Content-Disposition"), response.Body.String())
	}
}

func TestDocumentAssetHidesMissingDocument(t *testing.T) {
	endpoint := handler.NewDocumentAsset(assetReaderStub{err: document.ErrDocumentNotFound})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/4/documents/9/asset", nil)
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "9")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
	_, _ = io.Copy(io.Discard, response.Body)
}
