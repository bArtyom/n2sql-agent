package security_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/security"
)

func TestRedactTextMasksCommonSecrets(t *testing.T) {
	input := `api_key="sk-live-12345678901234567890" DASHSCOPE_API_KEY=plain-dashscope-secret authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9 password=super-secret postgres://db-user:db-password@example.com/app`
	got := security.RedactText(input)

	for _, secret := range []string{
		"sk-live-12345678901234567890",
		"plain-dashscope-secret",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"super-secret",
		"postgres://db-user:db-password@example.com/app",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("RedactText() = %q, still contains %q", got, secret)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("RedactText() = %q, want redaction marker", got)
	}
}

func TestRedactTextKeepsOrdinaryDocumentation(t *testing.T) {
	input := "Use the token budget to limit the model response. API documentation explains authentication."
	if got := security.RedactText(input); got != input {
		t.Fatalf("RedactText() = %q, want unchanged documentation", got)
	}
}

func TestRedactToolResultCopiesAndSanitizesSources(t *testing.T) {
	result := agent.ToolResult{
		Content: `[{"content":"password=super-secret"}]`,
		Metadata: map[string]any{
			"sources":   []documentchunk.SearchResult{{Content: "api_key=sk-live-12345678901234567890"}},
			"truncated": true,
		},
	}

	redacted := security.RedactToolResult(result)
	if strings.Contains(redacted.Content, "super-secret") {
		t.Fatalf("redacted tool content = %q, contains password", redacted.Content)
	}
	var visible []map[string]string
	if err := json.Unmarshal([]byte(redacted.Content), &visible); err != nil {
		t.Fatalf("redacted tool content is invalid JSON: %v", err)
	}
	sources, ok := redacted.Metadata["sources"].([]documentchunk.SearchResult)
	if !ok || len(sources) != 1 || strings.Contains(sources[0].Content, "sk-live-12345678901234567890") {
		t.Fatalf("redacted sources = %#v, want sanitized source copy", redacted.Metadata["sources"])
	}
	originalSources := result.Metadata["sources"].([]documentchunk.SearchResult)
	if !strings.Contains(originalSources[0].Content, "sk-live-12345678901234567890") {
		t.Fatalf("RedactToolResult() mutated original source metadata: %#v", originalSources)
	}
	if result.Content == redacted.Content {
		t.Fatalf("RedactToolResult() did not sanitize tool content")
	}
	if result.Metadata["truncated"] != redacted.Metadata["truncated"] {
		t.Fatalf("redacted metadata lost non-sensitive field: %#v", redacted.Metadata)
	}
}
