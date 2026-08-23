package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
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
	return s.recognizeWithPrompt(ctx, mimeType, image, s.prompt)
}

func (s *OCRService) recognizeWithPrompt(ctx context.Context, mimeType string, image []byte, prompt string) (string, error) {
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
		Prompt:   prompt,
		MIMEType: mimeType,
		Image:    image,
	})
	if err != nil {
		return "", fmt.Errorf("OCR image: %w", err)
	}
	return response.Text, nil
}

// ImageEnricherService adapts the configured vision model to the worker's
// optional image-enrichment hook. OCR and caption are intentionally separate
// outputs so retrieval can match either visible text or visual meaning.
// Captioning is opt-in; an empty prompt avoids a second model request.
type ImageEnricherService struct {
	ocr           *OCRService
	captionPrompt string
}

func NewImageEnricherService(ocr *OCRService, captionPrompt string) *ImageEnricherService {
	return &ImageEnricherService{ocr: ocr, captionPrompt: captionPrompt}
}

func (s *ImageEnricherService) EnrichImage(ctx context.Context, asset documentextractor.ImageAsset) (documentextractor.ImageEnrichment, error) {
	if s == nil || s.ocr == nil {
		return documentextractor.ImageEnrichment{}, ErrOCRModelNotConfigured
	}
	result := documentextractor.ImageEnrichment{OCRText: asset.OCRText, Caption: asset.Caption}
	if result.OCRText == "" {
		text, err := s.ocr.RecognizeWithMIME(ctx, asset.MIMEType, asset.Data)
		if err != nil {
			return documentextractor.ImageEnrichment{}, err
		}
		result.OCRText = text
	}
	if result.Caption == "" && strings.TrimSpace(s.captionPrompt) != "" {
		caption, err := s.ocr.recognizeWithPrompt(ctx, asset.MIMEType, asset.Data, s.captionPrompt)
		if err != nil {
			return documentextractor.ImageEnrichment{}, fmt.Errorf("caption image: %w", err)
		}
		result.Caption = caption
	}
	return result, nil
}
