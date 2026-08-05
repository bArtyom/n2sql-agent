package modelruntime_test

import (
	"context"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type ocrProviderStoreStub struct {
	provider modelprovider.Provider
}

func (s ocrProviderStoreStub) Current(context.Context) (modelprovider.Provider, error) {
	return s.provider, nil
}

func (ocrProviderStoreStub) Save(context.Context, modelprovider.Provider) (modelprovider.Provider, error) {
	panic("Save should not be called")
}

type ocrerStub struct {
	baseURL string
	apiKey  string
	request modelclient.OCRRequest
}

func (s *ocrerStub) OCR(_ context.Context, baseURL, apiKey string, request modelclient.OCRRequest) (modelclient.OCRResponse, error) {
	s.baseURL, s.apiKey, s.request = baseURL, apiKey, request
	return modelclient.OCRResponse{Text: "recognized"}, nil
}

func TestOCRServiceUsesConfiguredProviderCredentials(t *testing.T) {
	ocrer := &ocrerStub{}
	service := modelruntime.NewOCRService(
		ocrProviderStoreStub{provider: modelprovider.Provider{BaseURL: "https://llm.example/v1", APIKeyEnvVar: "DASHSCOPE_API_KEY"}},
		ocrer,
		"DASHSCOPE_API_KEY",
		func(name string) (string, bool) { return "secret", name == "DASHSCOPE_API_KEY" },
		"vision-model",
		"extract visible text",
	)

	text, err := service.Recognize(context.Background(), []byte("image"))
	if err != nil {
		t.Fatalf("Recognize() error = %v", err)
	}
	if text != "recognized" || ocrer.baseURL != "https://llm.example/v1" || ocrer.apiKey != "secret" {
		t.Fatalf("text=%q baseURL=%q apiKey=%q", text, ocrer.baseURL, ocrer.apiKey)
	}
	if ocrer.request.Model != "vision-model" || ocrer.request.Prompt != "extract visible text" || string(ocrer.request.Image) != "image" {
		t.Fatalf("OCR request = %#v", ocrer.request)
	}
}

func TestOCRServiceRequiresModel(t *testing.T) {
	service := modelruntime.NewOCRService(
		ocrProviderStoreStub{}, &ocrerStub{}, "DASHSCOPE_API_KEY",
		func(string) (string, bool) { return "secret", true }, "", "prompt",
	)
	if _, err := service.Recognize(context.Background(), []byte("image")); err == nil {
		t.Fatal("Recognize() error = nil, want missing model error")
	}
}
