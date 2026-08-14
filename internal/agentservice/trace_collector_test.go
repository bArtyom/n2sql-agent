package agentservice

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

func TestTraceCollectorAssociatesToolSourcesByStableKey(t *testing.T) {
	collector := newTraceCollector()
	source := retrieval.Result{DocumentID: 11, Position: 2, Content: "引用"}
	if err := collector.Sink(nil)(agent.Event{
		Type: agent.EventToolCalled,
		Data: map[string]any{"tool_call_id": "call-1", "tool_name": "knowledge_search"},
	}); err != nil {
		t.Fatalf("tool_called event error = %v", err)
	}
	if err := collector.Sink(nil)(agent.Event{
		Type: agent.EventToolFinished,
		Data: map[string]any{
			"tool_call_id": "call-1",
			"tool_name":    "knowledge_search",
			"sources":      []retrieval.Result{source, source},
		},
	}); err != nil {
		t.Fatalf("tool_finished event error = %v", err)
	}

	events := collector.Events()
	if len(events) != 1 || len(events[0].SourceKeys) != 1 || events[0].SourceKeys[0] != "11:2" {
		t.Fatalf("trace events = %#v, want one deduplicated source key", events)
	}
}
