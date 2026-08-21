package agentservice

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

func TestSourceCollectorDoesNotTreatDocumentMetadataAsGrounding(t *testing.T) {
	collector := newSourceCollector()
	collector.observe(agent.Event{
		Type: agent.EventToolFinished,
		Data: map[string]any{
			"tool_name": "document_list",
			"sources":   []retrieval.Result{{DocumentID: 7, Position: 0, Content: "文档存在"}},
		},
	})

	if collector.HasEvidence() {
		t.Fatal("document metadata should not satisfy strict knowledge grounding")
	}
}

func TestSourceCollectorTreatsDocumentContentAsGrounding(t *testing.T) {
	collector := newSourceCollector()
	collector.observe(agent.Event{
		Type: agent.EventToolFinished,
		Data: map[string]any{
			"tool_name": "document_read",
			"sources":   []retrieval.Result{{DocumentID: 7, Position: 0, Content: "正文证据"}},
		},
	})

	if !collector.HasEvidence() {
		t.Fatal("document content should satisfy strict knowledge grounding")
	}
}
