package modelruntime_test

import (
	"context"
	"errors"
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

type enabledOCRProviderStore struct {
	current modelprovider.Provider
	enabled []modelprovider.Provider
}

func (s enabledOCRProviderStore) Current(context.Context) (modelprovider.Provider, error) {
	return s.current, nil
}
func (enabledOCRProviderStore) Save(context.Context, modelprovider.Provider) (modelprovider.Provider, error) {
	return modelprovider.Provider{}, nil
}
func (s enabledOCRProviderStore) Enabled(context.Context) ([]modelprovider.Provider, error) {
	return s.enabled, nil
}

type failoverOCRer struct {
	calls  []string
	errors []error
}

func (s *failoverOCRer) OCR(_ context.Context, baseURL, _ string, _ modelclient.OCRRequest) (modelclient.OCRResponse, error) {
	s.calls = append(s.calls, baseURL)
	index := len(s.calls) - 1
	if index < len(s.errors) && s.errors[index] != nil {
		return modelclient.OCRResponse{}, s.errors[index]
	}
	return modelclient.OCRResponse{Text: "fallback text"}, nil
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

func TestOCRServiceFailsOverAfterTransientProviderFailure(t *testing.T) {
	primary := modelprovider.Provider{Name: "primary", BaseURL: "https://primary.example/v1", APIKeyEnvVar: "OCR_API_KEY"}
	secondary := modelprovider.Provider{Name: "secondary", BaseURL: "https://secondary.example/v1", APIKeyEnvVar: "OCR_API_KEY"}
	ocrer := &failoverOCRer{errors: []error{&modelclient.HTTPStatusError{Operation: "ocr", StatusCode: 503}, nil}}
	service := modelruntime.NewOCRService(enabledOCRProviderStore{current: primary, enabled: []modelprovider.Provider{primary, secondary}}, ocrer, "OCR_API_KEY", func(string) (string, bool) {
		return "secret", true
	}, "vision-model", "prompt")

	text, err := service.Recognize(context.Background(), []byte("image"))
	if err != nil || text != "fallback text" {
		t.Fatalf("Recognize() = %q, %v", text, err)
	}
	if len(ocrer.calls) != 2 || ocrer.calls[1] != secondary.BaseURL {
		t.Fatalf("provider calls = %#v", ocrer.calls)
	}
}

func TestOCRServiceDoesNotRetryInvalidRequest(t *testing.T) {
	ocrer := &failoverOCRer{errors: []error{errors.New("invalid image")}}
	service := modelruntime.NewOCRService(ocrProviderStoreStub{provider: modelprovider.Provider{BaseURL: "https://llm.example/v1", APIKeyEnvVar: "OCR_API_KEY"}}, ocrer, "OCR_API_KEY", func(string) (string, bool) { return "secret", true }, "vision-model", "prompt")
	if _, err := service.Recognize(context.Background(), []byte("image")); err == nil {
		t.Fatal("Recognize() error = nil, want failure")
	}
	if len(ocrer.calls) != 1 {
		t.Fatalf("invalid OCR request calls = %d, want one", len(ocrer.calls))
	}
}
