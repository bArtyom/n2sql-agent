package security

import (
	"regexp"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

const RedactedValue = "[REDACTED]"

var (
	connectionStringPattern = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|redis)://[^\s"'<>]+`)
	assignedSecretPattern   = regexp.MustCompile(`(?i)(\b(?:[A-Za-z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|client[_-]?secret|secret|password|passwd|token)\b\s*[:=]\s*)(["']?)[^"'\s,;}\]]+(["']?)`)
	bearerTokenPattern      = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]{12,}`)
	prefixedTokenPattern    = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9][A-Za-z0-9._-]{15,}|ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16})\b`)
)

// RedactText masks common credential shapes while leaving ordinary prose
// unchanged. It is intentionally a small boundary filter, not a full DLP
// engine.
func RedactText(text string) string {
	text = connectionStringPattern.ReplaceAllString(text, RedactedValue)
	text = assignedSecretPattern.ReplaceAllString(text, "$1$2"+RedactedValue+"$3")
	text = bearerTokenPattern.ReplaceAllString(text, "$1"+RedactedValue)
	return prefixedTokenPattern.ReplaceAllString(text, RedactedValue)
}

// RedactToolResult copies a tool result before it enters the model or an
// externally visible event. Only known public source metadata is transformed;
// private metadata is preserved but is not exported by the Agent engine.
func RedactToolResult(result agent.ToolResult) agent.ToolResult {
	redacted := result
	redacted.Content = RedactText(result.Content)
	if result.Metadata == nil {
		return redacted
	}

	redacted.Metadata = make(map[string]any, len(result.Metadata))
	for key, value := range result.Metadata {
		redacted.Metadata[key] = value
	}
	if sources, ok := result.Metadata["sources"].([]retrieval.Result); ok {
		redactedSources := append([]retrieval.Result(nil), sources...)
		for index := range redactedSources {
			redactedSources[index].Content = RedactText(redactedSources[index].Content)
		}
		redacted.Metadata["sources"] = redactedSources
	}
	return redacted
}
