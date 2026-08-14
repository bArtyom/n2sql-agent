package agentservice

import (
	"encoding/json"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

const maxResponseSources = 20

// sourceCollector observes the existing tool_finished events and turns their
// source payloads into one deduplicated response field. Keeping this at the
// service boundary means the Agent engine does not need to know about HTTP or
// conversation persistence.
type sourceCollector struct {
	sources []retrieval.Result
	seen    map[string]struct{}
}

func newSourceCollector() *sourceCollector {
	return &sourceCollector{seen: make(map[string]struct{})}
}

func (c *sourceCollector) Sink(next agentruntime.EventSink) agentruntime.EventSink {
	return func(event agent.Event) error {
		c.observe(event)
		if next == nil {
			return nil
		}
		return next(event)
	}
}

func (c *sourceCollector) observe(event agent.Event) {
	if c == nil || event.Type != agent.EventToolFinished || len(c.sources) >= maxResponseSources {
		return
	}
	data, ok := event.Data.(map[string]any)
	if !ok {
		return
	}
	sources := decodeSources(data["sources"])
	for _, source := range sources {
		if len(c.sources) >= maxResponseSources {
			return
		}
		key := fmt.Sprintf("%d:%d", source.DocumentID, source.Position)
		if _, exists := c.seen[key]; exists {
			continue
		}
		c.seen[key] = struct{}{}
		c.sources = append(c.sources, source)
	}
}

func decodeSources(value any) []retrieval.Result {
	sources, ok := value.([]retrieval.Result)
	if ok {
		return sources
	}
	// This fallback keeps the collector useful for alternate EventAnswerer
	// implementations that cross a JSON boundary before emitting events.
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded []retrieval.Result
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil
	}
	return decoded
}

func (c *sourceCollector) Sources() []retrieval.Result {
	if c == nil || len(c.sources) == 0 {
		return nil
	}
	result := make([]retrieval.Result, len(c.sources))
	copy(result, c.sources)
	return result
}
