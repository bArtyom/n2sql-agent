package modelruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
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
}

func NewOCRService(providers modelprovider.Store, ocrer modelclient.OCRer, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, model, prompt string) *OCRService {
	return &OCRService{
		providers:    providers,
		ocrer:        ocrer,
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
		model:        model,
		prompt:       prompt,
	}
}

func (s *OCRService) Recognize(ctx context.Context, image []byte) (string, error) {
	return s.RecognizeWithMIME(ctx, "image/jpeg", image)
}

func (s *OCRService) RecognizeWithMIME(ctx context.Context, mimeType string, image []byte) (string, error) {
	if s == nil || s.ocrer == nil {
		return "", ErrOCRModelNotConfigured
	}
	if s.model == "" {
		return "", ErrOCRModelNotConfigured
	}
	provider, err := s.providers.Current(ctx)
	if err != nil {
		return "", err
	}
	if provider.APIKeyEnvVar != s.apiKeyEnvVar {
		return "", ErrAPIKeyEnvironmentMismatch
	}
	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || apiKey == "" {
		return "", ErrAPIKeyNotConfigured
	}
	response, err := s.ocrer.OCR(ctx, provider.BaseURL, apiKey, modelclient.OCRRequest{
		Model:    s.model,
		Prompt:   s.prompt,
		MIMEType: mimeType,
		Image:    image,
	})
	if err != nil {
		return "", fmt.Errorf("OCR image: %w", err)
	}
	return response.Text, nil
}
