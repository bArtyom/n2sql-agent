package modelruntime

import (
	"time"

	"github.com/bArtyom/n2sql-agent/internal/ops"
)

const (
	capabilityChat      = "chat"
	capabilityEmbedding = "embedding"
	capabilityOCR       = "ocr"
	capabilityRerank    = "rerank"
)

// CircuitBreakerConfig is shared by all model capabilities. The breaker still
// keeps separate state for each provider/capability pair.
type CircuitBreakerConfig struct {
	FailureLimit  int
	RecoveryAfter time.Duration
}

func newCircuitBreaker(config []CircuitBreakerConfig) *ops.CircuitBreaker {
	if len(config) == 0 {
		return ops.NewCircuitBreaker(3, 30*time.Second)
	}
	return ops.NewCircuitBreaker(config[0].FailureLimit, config[0].RecoveryAfter)
}
