package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/ops"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var ErrOCRModelNotConfigured = errors.New("OCR model is not configured")

// OCRService resolves the current model provider and uses its credentials to
// call a vision-capable model. The OCR model is intentionally separate from
// the chat and embedding model names.
type OCRService struct {
	providers    modelprovider.Store
	ocrer        modelclient.OCRer
	apiKeyEnvVar string
	lookupAPIKey APIKeyLookup
	model        string
	prompt       string
	breaker      *ops.CircuitBreaker
}

func NewOCRService(providers modelprovider.Store, ocrer modelclient.OCRer, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, model, prompt string, breakerConfig ...CircuitBreakerConfig) *OCRService {
	return &OCRService{
		providers:    providers,
		ocrer:        ocrer,
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
		model:        model,
		prompt:       prompt,
		breaker:      newCircuitBreaker(breakerConfig),
	}
}

func (s *OCRService) Recognize(ctx context.Context, image []byte) (string, error) {
	return s.RecognizeWithMIME(ctx, "image/jpeg", image)
}

func (s *OCRService) RecognizeWithMIME(ctx context.Context, mimeType string, image []byte) (string, error) {
	return s.recognizeWithPrompt(ctx, mimeType, image, s.prompt)
}

func (s *OCRService) recognizeWithPrompt(ctx context.Context, mimeType string, image []byte, prompt string) (string, error) {
	if s == nil || s.ocrer == nil {
		return "", ErrOCRModelNotConfigured
	}
	if s.model == "" {
		return "", ErrOCRModelNotConfigured
	}
	current, err := s.providers.Current(ctx)
	if err != nil {
		return "", err
	}
	providers := []modelprovider.Provider{current}
	if enabledStore, ok := s.providers.(modelprovider.EnabledStore); ok {
		if enabled, listErr := enabledStore.Enabled(ctx); listErr == nil && len(enabled) > 0 {
			providers = enabled
		}
	}
	candidates := make([]modelprovider.Provider, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if _, exists := seen[provider.Name]; exists {
			continue
		}
		seen[provider.Name] = struct{}{}
		if provider.APIKeyEnvVar == s.apiKeyEnvVar {
			candidates = append(candidates, provider)
		}
	}
	if len(candidates) == 0 {
		return "", ErrAPIKeyEnvironmentMismatch
	}
	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || apiKey == "" {
		return "", ErrAPIKeyNotConfigured
	}
	var lastErr error
	for index, provider := range candidates {
		if !s.breaker.Allow(provider.Name, capabilityOCR) {
			observeCircuitBreaker(ctx, provider.Name, capabilityOCR, usage.CircuitEventOpened)
			lastErr = fmt.Errorf("provider %s: %w", provider.Name, ops.ErrCircuitOpen)
			continue
		}
		started := time.Now()
		response, callErr := s.ocrer.OCR(ctx, provider.BaseURL, apiKey, modelclient.OCRRequest{Model: s.model, Prompt: prompt, MIMEType: mimeType, Image: image})
		observeModelCall(ctx, usage.ModelKindOCR, provider.Name, s.model, started, nil, callErr)
		if callErr == nil {
			if index > 0 {
				observeCircuitBreaker(ctx, provider.Name, capabilityOCR, usage.CircuitEventFallback)
			}
			s.breaker.RecordSuccess(provider.Name, capabilityOCR)
			return response.Text, nil
		}
		lastErr = callErr
		if !ops.IsRetryableFailure(callErr) {
			break
		}
		s.breaker.RecordFailure(provider.Name, capabilityOCR)
	}
	return "", fmt.Errorf("OCR image: %w", lastErr)
}

// ImageEnricherService adapts the configured vision model to the independent
// OCR and Caption task branches. Captioning is opt-in; an empty prompt avoids
// creating a second model task.
type ImageEnricherService struct {
	ocr           *OCRService
	captionPrompt string
}

func NewImageEnricherService(ocr *OCRService, captionPrompt string) *ImageEnricherService {
	return &ImageEnricherService{ocr: ocr, captionPrompt: captionPrompt}
}

// OCR runs only the OCR branch. It is used by the independent image task
// worker so a caption failure does not repeat a successful OCR request.
func (s *ImageEnricherService) OCR(ctx context.Context, asset documentextractor.ImageAsset) (string, error) {
	if strings.TrimSpace(asset.OCRText) != "" {
		return asset.OCRText, nil
	}
	if s == nil || s.ocr == nil {
		return "", ErrOCRModelNotConfigured
	}
	return s.ocr.RecognizeWithMIME(ctx, asset.MIMEType, asset.Data)
}

// Caption runs only the caption branch. Captioning is deliberately opt-in so
// the independent worker does not make an unexpected second vision call.
func (s *ImageEnricherService) Caption(ctx context.Context, asset documentextractor.ImageAsset) (string, error) {
	if strings.TrimSpace(asset.Caption) != "" {
		return asset.Caption, nil
	}
	if s == nil || s.ocr == nil || strings.TrimSpace(s.captionPrompt) == "" {
		return "", ErrOCRModelNotConfigured
	}
	return s.ocr.recognizeWithPrompt(ctx, asset.MIMEType, asset.Data, s.captionPrompt)
}
