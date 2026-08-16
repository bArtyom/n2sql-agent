package agentservice

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

const (
	MaxChatAttachments         = 3
	MaxImageAttachmentBytes    = 4 << 20
	MaxTextAttachmentBytes     = 128 << 10
	MaxAttachmentFilenameBytes = 255
	MaxAttachmentRequestBytes  = 18 << 20
)

var (
	ErrInvalidThinkingMode = errors.New("invalid thinking mode")
	ErrInvalidAttachment   = errors.New("invalid chat attachment")
	ErrAttachmentTooLarge  = errors.New("chat attachment is too large")
)

type ChatAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	DataBase64  string `json:"data_base64"`
}

// NormalizeThinkingMode validates the small, provider-neutral set exposed by
// the UI. The empty value intentionally means the default standard mode.
func NormalizeThinkingMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "standard", nil
	}
	switch value {
	case "fast", "standard", "deep":
		return value, nil
	default:
		return "", ErrInvalidThinkingMode
	}
}

func ReasoningEffortForMode(mode string) string {
	switch mode {
	case "fast":
		return "low"
	case "deep":
		return "high"
	default:
		return "medium"
	}
}

func ValidateAttachments(attachments []ChatAttachment) error {
	if len(attachments) > MaxChatAttachments {
		return fmt.Errorf("%w: too many attachments", ErrInvalidAttachment)
	}
	for _, attachment := range attachments {
		if err := validateAttachmentMetadata(attachment); err != nil {
			return err
		}
		decoded, err := decodeAttachment(attachment)
		if err != nil {
			return err
		}
		limit := MaxTextAttachmentBytes
		if strings.HasPrefix(attachment.ContentType, "image/") {
			limit = MaxImageAttachmentBytes
		}
		if len(decoded) > limit {
			return fmt.Errorf("%w: %s", ErrAttachmentTooLarge, attachment.Filename)
		}
		if strings.HasPrefix(attachment.ContentType, "image/") && !validImageSignature(attachment.ContentType, decoded) {
			return fmt.Errorf("%w: invalid image data", ErrInvalidAttachment)
		}
		if strings.HasPrefix(attachment.ContentType, "text/") && !utf8.Valid(decoded) {
			return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidAttachment, attachment.Filename)
		}
	}
	return nil
}

func ChatContentParts(message string, attachments []ChatAttachment) ([]modelclient.ChatContentPart, error) {
	if err := ValidateAttachments(attachments); err != nil {
		return nil, err
	}
	if len(attachments) == 0 {
		return nil, nil
	}
	parts := []modelclient.ChatContentPart{{Type: "text", Text: message}}
	for _, attachment := range attachments {
		decoded, err := decodeAttachment(attachment)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(attachment.ContentType, "image/") {
			parts = append(parts, modelclient.ChatContentPart{
				Type: "image_url",
				ImageURL: &modelclient.ChatImageURL{
					URL: "data:" + attachment.ContentType + ";base64," + attachment.DataBase64,
				},
			})
			continue
		}
		parts = append(parts, modelclient.ChatContentPart{
			Type: "text",
			Text: "附件 " + attachment.Filename + " 内容：\n" + string(decoded),
		})
	}
	return parts, nil
}

func validateAttachmentMetadata(attachment ChatAttachment) error {
	filename := strings.TrimSpace(attachment.Filename)
	if filename == "" || len(filename) > MaxAttachmentFilenameBytes || filepath.Base(filename) != filename || strings.ContainsAny(filename, "\r\n\x00") {
		return fmt.Errorf("%w: invalid filename", ErrInvalidAttachment)
	}
	switch attachment.ContentType {
	case "image/png", "image/jpeg", "image/webp", "text/plain", "text/markdown":
		return nil
	default:
		return fmt.Errorf("%w: unsupported content type", ErrInvalidAttachment)
	}
}

func decodeAttachment(attachment ChatAttachment) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(attachment.DataBase64)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("%w: invalid base64", ErrInvalidAttachment)
	}
	return decoded, nil
}

func validImageSignature(contentType string, data []byte) bool {
	switch contentType {
	case "image/png":
		return bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	case "image/jpeg":
		return bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff})
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	default:
		return false
	}
}
